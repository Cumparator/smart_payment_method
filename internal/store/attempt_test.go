package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/Cumparator/smart_payment_method/internal/payment"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("SPM_DATABASE_URL")
	if dsn == "" {
		t.Skip("SPM_DATABASE_URL not set")
	}
	db, err := New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func newUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func newPayment(t *testing.T, db *DB) string {
	t.Helper()
	id := newUUID(t)
	_, _, err := db.CreatePayment(context.Background(), payment.Payment{
		ID: id, IdempotencyKey: "key-" + id, Amount: 100, Currency: "RUB", State: payment.Processing,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestStartAttemptOnlyOneLive(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	id := newPayment(t, db)

	a1, err := db.StartAttempt(ctx, id, "ch_balanced")
	if err != nil {
		t.Fatal(err)
	}
	if a1.AttemptNo != 1 || a1.State != payment.AttemptStarted || a1.FinishedAt != nil {
		t.Fatalf("неожиданная первая попытка: %+v", a1)
	}

	if _, err := db.StartAttempt(ctx, id, "ch_cheap_slow"); !errors.Is(err, ErrLiveAttemptExists) {
		t.Fatalf("вторая живая попытка: err = %v, want ErrLiveAttemptExists", err)
	}
	// Неудачный старт не должен «съедать» номер попытки.
	p, err := db.GetPayment(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.AttemptsCount != 1 {
		t.Fatalf("attempts_count = %d, want 1", p.AttemptsCount)
	}

	// После завершения можно начать следующую.
	ok, err := db.SetAttemptState(ctx, id, 1, payment.AttemptStarted, payment.AttemptChannelError)
	if err != nil || !ok {
		t.Fatalf("SetAttemptState: ok=%v err=%v", ok, err)
	}
	a2, err := db.StartAttempt(ctx, id, "ch_fast_expensive")
	if err != nil {
		t.Fatal(err)
	}
	if a2.AttemptNo != 2 {
		t.Fatalf("attempt_no = %d, want 2", a2.AttemptNo)
	}
}

func TestUnknownAttemptStaysLive(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	id := newPayment(t, db)

	if _, err := db.StartAttempt(ctx, id, "ch_balanced"); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SetAttemptState(ctx, id, 1, payment.AttemptStarted, payment.AttemptUnknown); err != nil || !ok {
		t.Fatalf("→UNKNOWN: ok=%v err=%v", ok, err)
	}
	live, found, err := db.GetLiveAttempt(ctx, id)
	if err != nil || !found || live.State != payment.AttemptUnknown || live.FinishedAt != nil {
		t.Fatalf("GetLiveAttempt: %+v found=%v err=%v", live, found, err)
	}
	if _, err := db.StartAttempt(ctx, id, "ch_cheap_slow"); !errors.Is(err, ErrLiveAttemptExists) {
		t.Fatalf("при UNKNOWN: err = %v, want ErrLiveAttemptExists", err)
	}

	if ok, err := db.SetAttemptState(ctx, id, 1, payment.AttemptUnknown, payment.AttemptApproved); err != nil || !ok {
		t.Fatalf("UNKNOWN→APPROVED: ok=%v err=%v", ok, err)
	}
	if _, found, _ := db.GetLiveAttempt(ctx, id); found {
		t.Fatal("после APPROVED живой попытки быть не должно")
	}
	// Повторный переход из старого состояния — CAS не срабатывает.
	if ok, err := db.SetAttemptState(ctx, id, 1, payment.AttemptUnknown, payment.AttemptDeclined); err != nil || ok {
		t.Fatalf("повторный CAS: ok=%v err=%v, want ok=false", ok, err)
	}
}

func TestStartAttemptMaxAttempts(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	id := newPayment(t, db)

	for i := 1; i <= payment.MaxAttempts; i++ {
		if _, err := db.StartAttempt(ctx, id, "ch_balanced"); err != nil {
			t.Fatalf("попытка %d: %v", i, err)
		}
		if ok, err := db.SetAttemptState(ctx, id, i, payment.AttemptStarted, payment.AttemptChannelError); err != nil || !ok {
			t.Fatalf("завершение %d: ok=%v err=%v", i, ok, err)
		}
	}
	if _, err := db.StartAttempt(ctx, id, "ch_balanced"); !errors.Is(err, ErrMaxAttempts) {
		t.Fatalf("err = %v, want ErrMaxAttempts", err)
	}
	list, err := db.ListAttempts(ctx, id)
	if err != nil || len(list) != payment.MaxAttempts {
		t.Fatalf("ListAttempts: len=%d err=%v", len(list), err)
	}
	for i, a := range list {
		if a.AttemptNo != i+1 || a.FinishedAt == nil {
			t.Fatalf("попытка %d: %+v", i+1, a)
		}
	}
}

func TestStartAttemptPaymentNotFound(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	if _, err := db.StartAttempt(ctx, newUUID(t), "ch_balanced"); !errors.Is(err, ErrPaymentNotFound) {
		t.Fatalf("err = %v, want ErrPaymentNotFound", err)
	}
}

// Параллельные воркеры: выигрывает ровно один, остальные получают ErrLiveAttemptExists.
func TestStartAttemptConcurrent(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	id := newPayment(t, db)

	const workers = 16
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		success int
		refused int
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := db.StartAttempt(ctx, id, "ch_balanced")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				success++
			case errors.Is(err, ErrLiveAttemptExists):
				refused++
			default:
				t.Errorf("неожиданная ошибка: %v", err)
			}
		}()
	}
	wg.Wait()
	if success != 1 || refused != workers-1 {
		t.Fatalf("success=%d refused=%d, want 1/%d", success, refused, workers-1)
	}
	if list, err := db.ListAttempts(ctx, id); err != nil || len(list) != 1 {
		t.Fatalf("ListAttempts: len=%d err=%v", len(list), err)
	}
}

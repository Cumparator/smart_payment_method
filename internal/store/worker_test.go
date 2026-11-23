package store

import (
	"context"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/payment"
)

func TestClaimPayment(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	id := newUUID(t)
	now := time.Now()
	_, _, err := db.CreatePayment(ctx, payment.Payment{
		ID: id, IdempotencyKey: "key-" + id, Amount: 100, Currency: "RUB",
		State: payment.Received, NextRunAt: now.Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	// В общей тестовой БД могут лежать чужие RECEIVED-платежи: берём, пока наш не найдётся.
	found := 0
	for i := 0; i < 1000; i++ {
		p, ok, err := db.ClaimPayment(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		if p.State != payment.Processing {
			t.Fatalf("взятый платёж в состоянии %s, want PROCESSING", p.State)
		}
		if p.ID == id {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("платёж взят %d раз, want 1", found)
	}
	got, err := db.GetPayment(ctx, id)
	if err != nil || got.State != payment.Processing {
		t.Fatalf("GetPayment: %+v err=%v", got, err)
	}
}

func TestClaimPaymentSkipsNotDue(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	id := newUUID(t)
	_, _, err := db.CreatePayment(ctx, payment.Payment{
		ID: id, IdempotencyKey: "key-" + id, Amount: 100, Currency: "RUB",
		State: payment.Received, NextRunAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		p, ok, err := db.ClaimPayment(ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		if p.ID == id {
			t.Fatal("платёж с next_run_at в будущем не должен браться")
		}
	}
}

func TestCompleteAttemptRetry(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	id := newPayment(t, db) // PROCESSING

	dec := &RoutingDecision{Strategy: "cheapest", Details: []byte(`{"chosen":"ch_balanced"}`)}
	a, err := db.StartAttemptWithDecision(ctx, id, "ch_balanced", dec)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM routing_decisions WHERE payment_id = $1 AND attempt_no = $2`, id, a.AttemptNo).Scan(&n); err != nil || n != 1 {
		t.Fatalf("routing_decisions: n=%d err=%v", n, err)
	}

	next := time.Now().Add(time.Minute).Truncate(time.Millisecond)
	ok, err := db.CompleteAttempt(ctx, Completion{
		PaymentID: id, AttemptNo: a.AttemptNo,
		AttemptTo: payment.AttemptChannelError, PaymentTo: payment.Received,
		NextRunAt: next, Reason: "ошибка канала",
	})
	if err != nil || !ok {
		t.Fatalf("CompleteAttempt: ok=%v err=%v", ok, err)
	}
	p, err := db.GetPayment(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.State != payment.Received || !p.NextRunAt.Equal(next) {
		t.Fatalf("payment = %+v, want RECEIVED с next_run_at=%v", p, next)
	}
	list, err := db.ListAttempts(ctx, id)
	if err != nil || len(list) != 1 || list[0].State != payment.AttemptChannelError || list[0].FinishedAt == nil {
		t.Fatalf("attempts = %+v err=%v", list, err)
	}

	// Повторное применение того же исхода — CAS не срабатывает, платёж не трогается.
	ok, err = db.CompleteAttempt(ctx, Completion{
		PaymentID: id, AttemptNo: a.AttemptNo,
		AttemptTo: payment.AttemptApproved, PaymentTo: payment.Succeeded,
	})
	if err != nil || ok {
		t.Fatalf("повторный CompleteAttempt: ok=%v err=%v, want ok=false", ok, err)
	}
	if p, _ := db.GetPayment(ctx, id); p.State != payment.Received {
		t.Fatalf("state = %s, want RECEIVED", p.State)
	}
}

func TestCompleteAttemptUnknownKeepsLive(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	id := newPayment(t, db)
	a, err := db.StartAttempt(ctx, id, "ch_balanced")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := db.CompleteAttempt(ctx, Completion{
		PaymentID: id, AttemptNo: a.AttemptNo,
		AttemptTo: payment.AttemptUnknown, PaymentTo: payment.Reconciling,
		NextRunAt: time.Now().Add(5 * time.Second), Reason: "таймаут",
	})
	if err != nil || !ok {
		t.Fatalf("CompleteAttempt: ok=%v err=%v", ok, err)
	}
	live, found, err := db.GetLiveAttempt(ctx, id)
	if err != nil || !found || live.State != payment.AttemptUnknown || live.FinishedAt != nil {
		t.Fatalf("GetLiveAttempt: %+v found=%v err=%v", live, found, err)
	}
	if p, _ := db.GetPayment(ctx, id); p.State != payment.Reconciling {
		t.Fatalf("state = %s, want RECONCILING", p.State)
	}
}

func TestCompleteAttemptRollsBackWhenPaymentMoved(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	id := newPayment(t, db)
	a, err := db.StartAttempt(ctx, id, "ch_balanced")
	if err != nil {
		t.Fatal(err)
	}
	// Платёж кто-то уже перевёл из PROCESSING: итог попытки не должен применяться наполовину.
	if ok, err := db.TransitionStatus(ctx, id, payment.Processing, payment.Reconciling, "test"); err != nil || !ok {
		t.Fatalf("TransitionStatus: ok=%v err=%v", ok, err)
	}
	ok, err := db.CompleteAttempt(ctx, Completion{
		PaymentID: id, AttemptNo: a.AttemptNo,
		AttemptTo: payment.AttemptApproved, PaymentTo: payment.Succeeded,
	})
	if err == nil || ok {
		t.Fatalf("CompleteAttempt: ok=%v err=%v, want ErrStateChanged", ok, err)
	}
	live, found, _ := db.GetLiveAttempt(ctx, id)
	if !found || live.State != payment.AttemptStarted {
		t.Fatalf("попытка должна остаться STARTED: %+v found=%v", live, found)
	}
}

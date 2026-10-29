package store

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"

	"github.com/Cumparator/smart_payment_method/internal/payment"
)

func testUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func TestCreatePaymentIdempotent(t *testing.T) {
	dsn := os.Getenv("SPM_DATABASE_URL")
	if dsn == "" {
		t.Skip("SPM_DATABASE_URL not set")
	}
	db, err := New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := "key-" + testUUID()
	p := payment.Payment{ID: testUUID(), IdempotencyKey: key, Amount: 100, Currency: "RUB", State: payment.Received}
	_, _, err = db.CreatePayment(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	p2 := payment.Payment{ID: testUUID(), IdempotencyKey: key, Amount: 200, Currency: "RUB", State: payment.Received}
	got, exists, err := db.CreatePayment(context.Background(), p2)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected existing")
	}
	if got.Amount != 100 {
		t.Fatalf("amount mismatch: %d", got.Amount)
	}
}

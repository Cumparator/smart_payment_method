package store

import (
	"context"
	"os"
	"testing"

	"github.com/Cumparator/smart_payment_method/internal/payment"
)

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
	p := payment.Payment{ID: "t1", IdempotencyKey: "key-t", Amount: 100, Currency: "RUB", State: payment.Received}
	_, _, err = db.CreatePayment(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	p2 := payment.Payment{ID: "t2", IdempotencyKey: "key-t", Amount: 200, Currency: "RUB", State: payment.Received}
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

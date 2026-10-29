package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/simulator"
)

func TestLedgerChargeOnce(t *testing.T) {
	dsn := os.Getenv("SPM_DATABASE_URL")
	if dsn == "" {
		t.Skip("SPM_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l := NewLedger(db)
	key := "ledger-test:" + time.Now().Format(time.RFC3339Nano)

	first, err := l.Charge(ctx, simulator.Entry{ChannelID: "test", Key: key, Amount: 100, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := l.Charge(ctx, simulator.Entry{ChannelID: "test", Key: key, Amount: 500, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if second.Amount != first.Amount {
		t.Fatalf("amount changed: %d -> %d", first.Amount, second.Amount)
	}
	e, err := l.Cancel(ctx, "test", key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != simulator.Cancelled {
		t.Fatalf("status %s", e.Status)
	}
}

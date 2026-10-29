package simulator

import (
	"context"
	"testing"
	"time"
)

func TestMemLedgerChargeOnce(t *testing.T) {
	ctx := context.Background()
	l := NewMemLedger()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	first, err := l.Charge(ctx, Entry{ChannelID: "a", Key: "p1:1", Amount: 100, At: at})
	if err != nil {
		t.Fatal(err)
	}
	second, err := l.Charge(ctx, Entry{ChannelID: "a", Key: "p1:1", Amount: 500, At: at.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || second.Amount != 100 {
		t.Fatalf("повтор изменил запись: %+v -> %+v", first, second)
	}

	entries, _ := l.Entries(ctx, "a")
	if len(entries) != 1 {
		t.Fatalf("ожидалась 1 запись, получили %d", len(entries))
	}
}

func TestMemLedgerCancel(t *testing.T) {
	ctx := context.Background()
	l := NewMemLedger()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if _, err := l.Charge(ctx, Entry{ChannelID: "a", Key: "p1:1", Amount: 100, At: at}); err != nil {
		t.Fatal(err)
	}
	e, _ := l.Cancel(ctx, "a", "p1:1", at)
	if e.Status != Cancelled || e.Amount != 100 {
		t.Fatalf("отмена: %+v", e)
	}

	// отмена до списания блокирует ключ
	if _, err := l.Cancel(ctx, "a", "p2:1", at); err != nil {
		t.Fatal(err)
	}
	e, _ = l.Charge(ctx, Entry{ChannelID: "a", Key: "p2:1", Amount: 100, At: at})
	if e.Status != Cancelled || e.Amount != 0 {
		t.Fatalf("списание по отменённому ключу: %+v", e)
	}
}

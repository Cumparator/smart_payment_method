package simulator

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/clock"
)

func newTestChannel(t *testing.T, cfg Config) *Channel {
	t.Helper()
	if cfg.ID == "" {
		cfg.ID = "sim"
	}
	ch, err := New(cfg, NewMemLedger(), clock.NewVirtual(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func TestConfigValidate(t *testing.T) {
	if _, err := New(Config{ID: "x", ErrorRate: 0.6, TimeoutRate: 0.6}, NewMemLedger(), clock.Real{}); err == nil {
		t.Fatal("ожидалась ошибка: сумма долей больше 1")
	}
	if _, err := New(Config{ID: "x", DeclineRate: -0.1}, NewMemLedger(), clock.Real{}); err == nil {
		t.Fatal("ожидалась ошибка: отрицательная доля")
	}
}

func TestChargeApprovedOnce(t *testing.T) {
	ctx := context.Background()
	ch := newTestChannel(t, Config{Tariff: channel.Tariff{Fixed: 10, Percent: 0.02}})

	for range 3 {
		res, err := ch.Charge(ctx, "p1:1", 1000, "RUB")
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != channel.Approved {
			t.Fatalf("исход %s", res.Outcome)
		}
	}
	s, _ := ch.Summary(ctx)
	if s.Charges != 1 || s.Amount != 1000 || s.Fees != 30 {
		t.Fatalf("итог %+v", s)
	}
}

func TestOutcomeRates(t *testing.T) {
	ctx := context.Background()
	cfg := Config{Seed: 42, DeclineRate: 0.1, ErrorRate: 0.2, TimeoutRate: 0.05}
	ch := newTestChannel(t, cfg)

	const n = 20000
	counts := map[channel.ChargeOutcome]int{}
	for i := range n {
		res, err := ch.Charge(ctx, fmt.Sprintf("p%d:1", i), 100, "RUB")
		if err != nil {
			t.Fatal(err)
		}
		counts[res.Outcome]++
	}
	want := map[channel.ChargeOutcome]float64{
		channel.Declined:     0.1,
		channel.ChannelError: 0.2,
		channel.Unknown:      0.05,
		channel.Approved:     0.65,
	}
	for o, p := range want {
		got := float64(counts[o]) / n
		if math.Abs(got-p) > 0.015 {
			t.Errorf("%s: доля %.3f, ожидали %.3f", o, got, p)
		}
	}
}

func TestDeterministic(t *testing.T) {
	ctx := context.Background()
	cfg := Config{Seed: 7, DeclineRate: 0.3, ErrorRate: 0.3, TimeoutRate: 0.3}
	a, b := newTestChannel(t, cfg), newTestChannel(t, cfg)
	for i := range 200 {
		key := fmt.Sprintf("p%d:1", i)
		ra, _ := a.Charge(ctx, key, 100, "RUB")
		rb, _ := b.Charge(ctx, key, 100, "RUB")
		if ra != rb {
			t.Fatalf("%s: %v != %v", key, ra, rb)
		}
	}
}

func TestTimeoutThenStatus(t *testing.T) {
	ctx := context.Background()
	ch := newTestChannel(t, Config{TimeoutRate: 1, TimeoutChargeRate: 1})

	res, _ := ch.Charge(ctx, "p1:1", 100, "RUB")
	if res.Outcome != channel.Unknown {
		t.Fatalf("исход %s", res.Outcome)
	}
	st, _ := ch.Status(ctx, "p1:1")
	if st.Outcome != channel.StatusApproved {
		t.Fatalf("статус %s: деньги списаны, ответ потерян", st.Outcome)
	}
	// повтор по тому же ключу не списывает второй раз
	res, _ = ch.Charge(ctx, "p1:1", 100, "RUB")
	if res.Outcome != channel.Approved {
		t.Fatalf("повтор %s", res.Outcome)
	}
	if s, _ := ch.Summary(ctx); s.Charges != 1 {
		t.Fatalf("итог %+v", s)
	}
}

func TestTimeoutWithoutCharge(t *testing.T) {
	ctx := context.Background()
	ch := newTestChannel(t, Config{TimeoutRate: 1})

	ch.Charge(ctx, "p1:1", 100, "RUB")
	st, _ := ch.Status(ctx, "p1:1")
	if st.Outcome != channel.StatusNotFound {
		t.Fatalf("статус %s", st.Outcome)
	}
}

func TestDeclinedStatus(t *testing.T) {
	ctx := context.Background()
	ch := newTestChannel(t, Config{DeclineRate: 1})

	ch.Charge(ctx, "p1:1", 100, "RUB")
	st, _ := ch.Status(ctx, "p1:1")
	if st.Outcome != channel.StatusDeclined {
		t.Fatalf("статус %s", st.Outcome)
	}
}

func TestCancel(t *testing.T) {
	ctx := context.Background()
	ch := newTestChannel(t, Config{})

	ch.Charge(ctx, "p1:1", 100, "RUB")
	if res, _ := ch.Cancel(ctx, "p1:1"); !res.Confirmed {
		t.Fatal("отмена не подтверждена")
	}
	st, _ := ch.Status(ctx, "p1:1")
	if st.Outcome != channel.StatusDeclined {
		t.Fatalf("статус %s", st.Outcome)
	}

	// отменённый заранее ключ не списывается
	ch.Cancel(ctx, "p2:1")
	res, _ := ch.Charge(ctx, "p2:1", 100, "RUB")
	if res.Outcome != channel.Declined {
		t.Fatalf("исход %s", res.Outcome)
	}

	s, _ := ch.Summary(ctx)
	if s.Charges != 0 || s.Cancelled != 2 {
		t.Fatalf("итог %+v", s)
	}
}

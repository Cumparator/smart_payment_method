package health

import (
	"sync"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/clock"
)

func newBreaker(t *testing.T, cfg BreakerConfig) (*Breaker, *clock.Virtual, *[]Transition) {
	t.Helper()
	clk := clock.NewVirtual(time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC))
	var log []Transition
	b := NewBreaker("A", cfg, clk, func(tr Transition) { log = append(log, tr) })
	return b, clk, &log
}

func fail(b *Breaker, n int) {
	for range n {
		b.Record(channel.ChannelError)
	}
}

func TestBreakerTripsOnConsecutiveFailures(t *testing.T) {
	b, _, _ := newBreaker(t, BreakerConfig{ConsecutiveFailures: 5})

	fail(b, 4)
	if b.State() != Closed || !b.Allow() {
		t.Fatal("после 4 неудач канал должен работать")
	}
	b.Record(channel.Unknown) // таймаут — тоже неудача
	if b.State() != Open || b.Allow() {
		t.Fatalf("после 5 неудач подряд состояние %s, ожидалось OPEN", b.State())
	}
}

func TestBreakerDeclinedMeansChannelIsAlive(t *testing.T) {
	b, _, _ := newBreaker(t, BreakerConfig{ConsecutiveFailures: 3, FailureRate: 0.99})

	for range 10 {
		fail(b, 2)
		b.Record(channel.Declined) // бизнес-отказ сбрасывает серию
	}
	if b.State() != Closed {
		t.Fatalf("состояние %s: DECLINED не должен считаться неудачей", b.State())
	}
}

func TestBreakerTripsOnFailureRate(t *testing.T) {
	b, _, log := newBreaker(t, BreakerConfig{ConsecutiveFailures: 100, Window: 20, MinAttempts: 10, FailureRate: 0.5})

	// неудачи через одну: серии нет, но половина исходов — неудачи
	for i := range 9 {
		if i%2 == 0 {
			b.Record(channel.ChannelError)
		} else {
			b.Record(channel.Approved)
		}
	}
	if b.State() != Closed {
		t.Fatal("до MinAttempts исходов по доле судить рано")
	}
	b.Record(channel.Approved) // 10-й исход: 5 неудач из 10
	if b.State() != Open {
		t.Fatalf("состояние %s, ожидалось OPEN: 5 неудач из 10", b.State())
	}
	if len(*log) != 1 || (*log)[0].Reason != "неудач 5 из последних 10" {
		t.Fatalf("переходы: %+v", *log)
	}
}

func TestBreakerOldFailuresLeaveWindow(t *testing.T) {
	b, _, _ := newBreaker(t, BreakerConfig{ConsecutiveFailures: 100, Window: 10, MinAttempts: 10, FailureRate: 0.5})

	fail(b, 4)
	for range 10 {
		b.Record(channel.Approved) // неудачи вытеснены из окна
	}
	fail(b, 4) // в окне 4 неудачи из 10 — меньше половины
	if b.State() != Closed {
		t.Fatalf("состояние %s, ожидалось CLOSED", b.State())
	}
}

func TestBreakerOpenUntilPauseThenOneProbe(t *testing.T) {
	b, clk, _ := newBreaker(t, BreakerConfig{ConsecutiveFailures: 2, OpenFor: 30 * time.Second})
	fail(b, 2)

	clk.Advance(29 * time.Second)
	if b.State() != Open || b.Allow() {
		t.Fatal("пауза ещё не прошла, канал должен быть отключён")
	}
	clk.Advance(time.Second)
	if b.State() != HalfOpen {
		t.Fatalf("после паузы состояние %s, ожидалось HALF_OPEN", b.State())
	}
	if !b.Allow() {
		t.Fatal("пробный запрос должен пройти")
	}
	if b.Allow() {
		t.Fatal("второй запрос во время пробы пускать нельзя")
	}
}

func TestBreakerProbeSuccessCloses(t *testing.T) {
	b, clk, _ := newBreaker(t, BreakerConfig{ConsecutiveFailures: 2, OpenFor: time.Minute})
	fail(b, 2)
	clk.Advance(time.Minute)

	b.Allow()
	b.Record(channel.Approved)
	if b.State() != Closed || !b.Allow() {
		t.Fatalf("состояние %s, ожидалось CLOSED", b.State())
	}
	// после включения счётчики начинаются заново
	b.Record(channel.ChannelError)
	if b.State() != Closed {
		t.Fatal("одна неудача после включения не должна снова отключать канал")
	}
}

func TestBreakerProbeFailureReopens(t *testing.T) {
	b, clk, _ := newBreaker(t, BreakerConfig{ConsecutiveFailures: 2, OpenFor: time.Minute})
	fail(b, 2)
	clk.Advance(time.Minute)

	b.Allow()
	b.Record(channel.ChannelError)
	if b.State() != Open {
		t.Fatalf("состояние %s, ожидалось OPEN", b.State())
	}
	clk.Advance(59 * time.Second)
	if b.State() != Open {
		t.Fatal("пауза после неудачной пробы отсчитывается заново")
	}
}

func TestBreakerReportsTransitionsInOrder(t *testing.T) {
	b, clk, log := newBreaker(t, BreakerConfig{ConsecutiveFailures: 2, OpenFor: time.Minute})
	fail(b, 2)
	clk.Advance(time.Minute)
	b.Allow()
	b.Record(channel.Approved)

	want := []struct{ from, to State }{{Closed, Open}, {Open, HalfOpen}, {HalfOpen, Closed}}
	if len(*log) != len(want) {
		t.Fatalf("переходов %d, ожидалось %d: %+v", len(*log), len(want), *log)
	}
	for i, w := range want {
		got := (*log)[i]
		if got.From != w.from || got.To != w.to || got.ChannelID != "A" || got.Reason == "" {
			t.Fatalf("переход %d: %+v, ожидалось %s→%s", i, got, w.from, w.to)
		}
	}
}

func TestBreakerConcurrentUse(t *testing.T) {
	b := NewBreaker("A", BreakerConfig{ConsecutiveFailures: 3, OpenFor: time.Millisecond}, nil, nil)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				if b.Allow() {
					if (i+g)%3 == 0 {
						b.Record(channel.Approved)
					} else {
						b.Record(channel.ChannelError)
					}
				}
				_ = b.State()
			}
		}()
	}
	wg.Wait()
}

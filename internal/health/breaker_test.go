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

// probe пускает n удачных пробных запросов по одному.
func probe(b *Breaker, n int) {
	for range n {
		if !b.Allow() {
			panic("проба не прошла Allow")
		}
		b.Record(channel.Approved)
	}
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

	probe(b, 2)
	if b.State() != HalfOpen {
		t.Fatalf("после 2 проб из 3 состояние %s, ожидалось HALF_OPEN", b.State())
	}
	probe(b, 1)
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
	probe(b, 3)

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

func TestBreakerFailedProbeDoublesPauseUpToMax(t *testing.T) {
	b, clk, _ := newBreaker(t, BreakerConfig{ConsecutiveFailures: 1, OpenFor: 10 * time.Second, MaxOpenFor: 35 * time.Second})
	fail(b, 1)

	for _, pause := range []time.Duration{10, 20, 35, 35} { // 10 → 20 → 40, но не больше 35
		clk.Advance(pause*time.Second - time.Second)
		if b.State() != Open {
			t.Fatalf("за секунду до конца паузы %s канал уже не OPEN", pause*time.Second)
		}
		clk.Advance(time.Second)
		if b.State() != HalfOpen {
			t.Fatalf("после паузы %s канал не HALF_OPEN", pause*time.Second)
		}
		b.Allow()
		b.Record(channel.ChannelError)
	}
}

func TestBreakerClosingResetsPause(t *testing.T) {
	b, clk, _ := newBreaker(t, BreakerConfig{ConsecutiveFailures: 1, OpenFor: 10 * time.Second, ProbeSuccesses: 1})
	fail(b, 1)
	clk.Advance(10 * time.Second)
	b.Allow()
	b.Record(channel.ChannelError) // пауза выросла до 20 с
	clk.Advance(20 * time.Second)
	probe(b, 1) // канал снова включён

	fail(b, 1)
	clk.Advance(10 * time.Second)
	if b.State() != HalfOpen {
		t.Fatalf("состояние %s: после включения пауза должна вернуться к 10 с", b.State())
	}
}

func TestBreakerFailureBreaksProbeStreak(t *testing.T) {
	b, clk, _ := newBreaker(t, BreakerConfig{ConsecutiveFailures: 1, OpenFor: time.Second, ProbeSuccesses: 3})
	fail(b, 1)
	clk.Advance(time.Second)
	probe(b, 2)
	b.Allow()
	b.Record(channel.Unknown) // третья проба — таймаут: серия сорвана
	if b.State() != Open {
		t.Fatalf("состояние %s, ожидалось OPEN", b.State())
	}
	clk.Advance(2 * time.Second)
	probe(b, 2)
	if b.State() != HalfOpen {
		t.Fatal("серия проб должна начинаться заново")
	}
}

func TestBreakerLimitsConcurrentProbes(t *testing.T) {
	b, clk, _ := newBreaker(t, BreakerConfig{ConsecutiveFailures: 1, OpenFor: time.Second, Probes: 2})
	fail(b, 1)
	clk.Advance(time.Second)

	first, second := b.Allow(), b.Allow()
	if !first || !second {
		t.Fatal("две пробы должны пройти")
	}
	if b.Allow() || b.CanAdmit() {
		t.Fatal("третью пробу пускать нельзя")
	}
	b.Record(channel.Approved) // одна проба закончилась, место освободилось
	if !b.CanAdmit() {
		t.Fatal("после ответа должно быть место для новой пробы")
	}
}

func TestBreakerCancelReturnsProbe(t *testing.T) {
	b, clk, _ := newBreaker(t, BreakerConfig{ConsecutiveFailures: 1, OpenFor: time.Second})
	fail(b, 1)
	clk.Advance(time.Second)

	if !b.Allow() {
		t.Fatal("проба должна пройти")
	}
	b.Cancel() // запрос так и не ушёл в канал
	if !b.Allow() {
		t.Fatal("после Cancel проба должна снова быть доступна")
	}
	b.Cancel()
	b.Cancel() // лишний Cancel не уводит счётчик в минус
	if !b.Allow() || b.Allow() {
		t.Fatal("после лишних Cancel должна быть ровно одна проба")
	}
}

func TestBreakerCanAdmitTakesNothing(t *testing.T) {
	b, clk, _ := newBreaker(t, BreakerConfig{ConsecutiveFailures: 1, OpenFor: time.Second})
	fail(b, 1)
	if b.CanAdmit() {
		t.Fatal("в OPEN запрос пускать нельзя")
	}
	clk.Advance(time.Second)
	for range 5 {
		if !b.CanAdmit() {
			t.Fatal("CanAdmit не должен занимать пробу")
		}
	}
	if !b.Allow() {
		t.Fatal("проба должна быть свободна после CanAdmit")
	}
}

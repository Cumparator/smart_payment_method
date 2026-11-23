package health

import (
	"math"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/strategy"
)

func TestLatencyDefaultThenSmoothed(t *testing.T) {
	r := New(10)
	r.Register("A", 500*time.Millisecond, 0)
	if got := r.Snapshot("A").LatencyS; got != 0.5 {
		t.Fatalf("до замеров %.3f, ожидалось 0.5", got)
	}

	r.Record("A", channel.Approved, time.Second) // первый замер заменяет значение по умолчанию
	if got := r.Snapshot("A").LatencyS; got != 1 {
		t.Fatalf("после первого замера %.3f, ожидалось 1", got)
	}
	r.Record("A", channel.Approved, 2*time.Second)
	want := 1 + latencyAlpha*(2-1)
	if got := r.Snapshot("A").LatencyS; math.Abs(got-want) > 1e-9 {
		t.Fatalf("после второго %.3f, ожидалось %.3f", got, want)
	}
}

func TestTimeoutKeepsLatency(t *testing.T) {
	r := New(10)
	r.Record("A", channel.Approved, time.Second)
	r.Record("A", channel.Unknown, time.Minute)

	snap := r.Snapshot("A")
	if snap.LatencyS != 1 {
		t.Fatalf("таймаут изменил задержку: %.3f", snap.LatencyS)
	}
	if snap.Successes != 1 || snap.Attempts != 2 {
		t.Fatalf("таймаут должен быть в окне как неудача: %d/%d", snap.Successes, snap.Attempts)
	}
}

func TestLoadFromInflight(t *testing.T) {
	r := New(10)
	r.Register("A", 0, 4)

	f1 := r.Begin("A")
	f2 := r.Begin("A")
	if got := r.Snapshot("A").Load; got != 0.5 {
		t.Fatalf("загрузка %.2f, ожидалось 0.5", got)
	}
	f1(channel.Approved, time.Second)
	f1(channel.Approved, time.Second) // повтор игнорируется
	if got := r.Snapshot("A").Load; got != 0.25 {
		t.Fatalf("после ответа %.2f, ожидалось 0.25", got)
	}
	f2(channel.Declined, time.Second)
	if got := r.Snapshot("A").Load; got != 0 {
		t.Fatalf("в конце %.2f, ожидалось 0", got)
	}
	if s, n := r.Counts("A"); s != 1 || n != 1 {
		t.Fatalf("окно %d/%d: повтор finish не должен дублировать исход", s, n)
	}
}

func TestNoLimitNoLoad(t *testing.T) {
	r := New(10)
	r.Begin("A")
	if got := r.Snapshot("A").Load; got != 0 {
		t.Fatalf("без лимита загрузка %.2f", got)
	}
}

func TestCandidate(t *testing.T) {
	r := New(10)
	r.Register("A", 300*time.Millisecond, 10)
	r.Record("A", channel.Approved, 400*time.Millisecond)
	r.Record("A", channel.ChannelError, 400*time.Millisecond)

	tariff := channel.Tariff{Fixed: 5, Percent: 0.01}
	c := r.Candidate("A", tariff)
	if c.ChannelID != "A" || c.Tariff != tariff || c.Successes != 1 || c.Attempts != 2 {
		t.Fatalf("кандидат %+v", c)
	}
	if math.Abs(c.LatencyS-0.4) > 1e-9 {
		t.Fatalf("задержка %.3f", c.LatencyS)
	}
}

func TestCandidateSuccessProbability(t *testing.T) {
	r := New(10)
	for range 3 {
		r.Record("A", channel.Approved, time.Second)
	}
	r.Record("A", channel.ChannelError, time.Second)
	r.Record("A", channel.Declined, time.Second) // бизнес-отказ в статистику не входит

	// 3 успеха из 4 технических исходов, сглаживание Лапласа: (3+1)/(4+2)
	if got := r.Candidate("A", channel.Tariff{}).PSuccess(); math.Abs(got-4.0/6) > 1e-9 {
		t.Fatalf("p_success %.4f, ожидалось %.4f", got, 4.0/6)
	}
	// у канала без статистики вероятность нейтральная
	if got := r.Candidate("B", channel.Tariff{}).PSuccess(); got != 0.5 {
		t.Fatalf("p_success без статистики %.2f, ожидалось 0.5", got)
	}
}

// Снимки из реестра — то, что получают стратегии: ненадёжный дешёвый канал
// проигрывает надёжному дорогому у ExpectedCost, но не у Cheapest.
func TestCandidatesFeedStrategies(t *testing.T) {
	r := New(100)
	r.Register("cheap", 500*time.Millisecond, 0)
	r.Register("solid", 500*time.Millisecond, 0)
	for range 19 {
		r.Record("cheap", channel.ChannelError, 500*time.Millisecond)
	}
	r.Record("cheap", channel.Approved, 500*time.Millisecond)
	for range 20 {
		r.Record("solid", channel.Approved, 500*time.Millisecond)
	}
	cands := []strategy.Candidate{
		r.Candidate("cheap", channel.Tariff{Percent: 0.010}),
		r.Candidate("solid", channel.Tariff{Percent: 0.015}),
	}

	if got := strategy.Rank(strategy.Cheapest{}, 100_000, cands)[0].ChannelID; got != "cheap" {
		t.Fatalf("cheapest выбрал %s, ожидался cheap", got)
	}
	ec := strategy.ExpectedCost{FailurePenalty: 5_000, SecondCost: 50, LoadThreshold: 0.7, LoadPenalty: 1_000}
	if got := strategy.Rank(ec, 100_000, cands)[0].ChannelID; got != "solid" {
		t.Fatalf("expected_cost выбрал %s, ожидался solid", got)
	}
}

package health

import (
	"math"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
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

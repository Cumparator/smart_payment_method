package health

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/clock"
)

func newGuarded(t *testing.T, cfg BreakerConfig) (*Registry, *clock.Virtual, *[]Transition) {
	t.Helper()
	clk := clock.NewVirtual(time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC))
	var log []Transition
	r := New(100, WithBreaker(cfg), WithClock(clk), OnStateChange(func(tr Transition) { log = append(log, tr) }))
	return r, clk, &log
}

// charge проводит один запрос через слот и закрывает его исходом o.
func charge(t *testing.T, r *Registry, id string, o channel.ChargeOutcome) {
	t.Helper()
	slot, why := r.TryBegin(id)
	if why != "" {
		t.Fatalf("канал %s недоступен: %s", id, why)
	}
	slot.Done(o, 100*time.Millisecond)
}

func TestRegistryWithoutBreakerAlwaysAdmits(t *testing.T) {
	r := New(10)
	for range 20 {
		charge(t, r, "A", channel.ChannelError)
	}
	if r.State("A") != Closed || r.Unavailable("A") != "" {
		t.Fatal("без предохранителей канал не отключается")
	}
}

func TestRegistryOpensAfterFailuresAndRejects(t *testing.T) {
	r, _, log := newGuarded(t, BreakerConfig{ConsecutiveFailures: 3})

	for range 3 {
		charge(t, r, "A", channel.ChannelError)
	}
	if r.State("A") != Open || r.Unavailable("A") != BreakerOpen {
		t.Fatalf("состояние %s, причина %q", r.State("A"), r.Unavailable("A"))
	}
	if slot, why := r.TryBegin("A"); slot != nil || why != BreakerOpen {
		t.Fatalf("TryBegin пустил запрос в отключённый канал: %v %q", slot, why)
	}
	if len(*log) != 1 || (*log)[0].ChannelID != "A" || (*log)[0].To != Open {
		t.Fatalf("уведомления: %+v", *log)
	}
	// другие каналы не затронуты
	if r.Unavailable("B") != "" {
		t.Fatal("канал B не должен быть отключён")
	}
}

func TestRecordFeedsBreaker(t *testing.T) {
	r, _, _ := newGuarded(t, BreakerConfig{ConsecutiveFailures: 2})
	r.Record("A", channel.ChannelError, time.Second)
	r.Record("A", channel.Unknown, time.Second)
	if r.State("A") != Open {
		t.Fatalf("состояние %s: Record должен кормить предохранитель", r.State("A"))
	}
}

func TestRegistryRecoversThroughProbes(t *testing.T) {
	r, clk, _ := newGuarded(t, BreakerConfig{ConsecutiveFailures: 2, OpenFor: time.Minute, ProbeSuccesses: 2})
	charge(t, r, "A", channel.ChannelError)
	charge(t, r, "A", channel.ChannelError)

	clk.Advance(time.Minute)
	if r.State("A") != HalfOpen || r.Unavailable("A") != "" {
		t.Fatalf("после паузы состояние %s, причина %q", r.State("A"), r.Unavailable("A"))
	}
	slot, _ := r.TryBegin("A")
	if _, why := r.TryBegin("A"); why != BreakerOpen {
		t.Fatal("пока идёт проба, остальные запросы не пускаем")
	}
	if r.Unavailable("A") != BreakerOpen {
		t.Fatal("во время пробы канал выбирать нельзя")
	}
	slot.Done(channel.Approved, time.Second)
	charge(t, r, "A", channel.Approved)
	if r.State("A") != Closed {
		t.Fatalf("состояние %s, ожидалось CLOSED после двух проб", r.State("A"))
	}
}

func TestSlotCancelReturnsProbeAndSkipsStats(t *testing.T) {
	r, clk, _ := newGuarded(t, BreakerConfig{ConsecutiveFailures: 1, OpenFor: time.Second})
	charge(t, r, "A", channel.ChannelError)
	clk.Advance(time.Second)

	slot, why := r.TryBegin("A")
	if why != "" {
		t.Fatal("проба должна пройти")
	}
	_, before := r.Counts("A")
	slot.Cancel()
	slot.Cancel()
	slot.Done(channel.Approved, time.Second) // после Cancel слот закрыт
	if _, after := r.Counts("A"); after != before {
		t.Fatalf("Cancel изменил статистику: %d → %d", before, after)
	}
	if _, why := r.TryBegin("A"); why != "" {
		t.Fatal("после Cancel проба должна быть свободна")
	}
}

func TestSlotDoneIsIdempotent(t *testing.T) {
	r := New(10)
	slot, _ := r.TryBegin("A")
	slot.Done(channel.Approved, time.Second)
	slot.Done(channel.ChannelError, time.Second)
	if s, n := r.Counts("A"); s != 1 || n != 1 {
		t.Fatalf("окно %d/%d: повторный Done не должен ничего менять", s, n)
	}
}

func TestBeginFeedsBreaker(t *testing.T) {
	r, _, _ := newGuarded(t, BreakerConfig{ConsecutiveFailures: 1})
	r.Begin("A")(channel.ChannelError, time.Second)
	if r.State("A") != Open {
		t.Fatalf("состояние %s: исход через Begin тоже должен учитываться", r.State("A"))
	}
}

func TestOnStateChangeMayCallRegistry(t *testing.T) {
	var r *Registry
	r = New(10, WithBreaker(BreakerConfig{ConsecutiveFailures: 1}), OnStateChange(func(Transition) {
		_ = r.Snapshot("A") // не должно зависнуть: уведомление идёт вне блокировок
		_ = r.Unavailable("A")
	}))
	charge(t, r, "A", channel.ChannelError)
}

func TestTryBeginLimitsConcurrentRequests(t *testing.T) {
	r := New(10)
	r.Register("A", 0, 2)

	s1, why1 := r.TryBegin("A")
	s2, why2 := r.TryBegin("A")
	if why1 != "" || why2 != "" {
		t.Fatalf("два запроса должны пройти: %q %q", why1, why2)
	}
	if slot, why := r.TryBegin("A"); slot != nil || why != Overloaded {
		t.Fatalf("третий запрос: %v %q, ожидался отказ Overloaded", slot, why)
	}
	if r.Unavailable("A") != Overloaded {
		t.Fatalf("Unavailable = %q, ожидалось overloaded", r.Unavailable("A"))
	}
	if got := r.Snapshot("A").Load; got != 1 {
		t.Fatalf("загрузка %.2f, ожидалась 1", got)
	}

	s1.Done(channel.Approved, time.Second)
	if _, why := r.TryBegin("A"); why != "" {
		t.Fatal("после ответа место должно освободиться")
	}
	s2.Cancel()
	if r.Unavailable("A") != "" {
		t.Fatal("после Cancel место должно освободиться")
	}
}

func TestLimitIsPerChannel(t *testing.T) {
	r := New(10)
	r.Register("A", 0, 1)
	r.Register("B", 0, 1)
	if _, why := r.TryBegin("A"); why != "" {
		t.Fatal(why)
	}
	if _, why := r.TryBegin("B"); why != "" {
		t.Fatalf("лимит канала A не должен занимать места в B: %q", why)
	}
}

func TestNoLimitMeansUnlimited(t *testing.T) {
	r := New(10)
	for range 100 {
		if _, why := r.TryBegin("A"); why != "" {
			t.Fatalf("канал без лимита отказал: %q", why)
		}
	}
}

func TestBreakerRefusalDoesNotLeakPlace(t *testing.T) {
	r, _, _ := newGuarded(t, BreakerConfig{ConsecutiveFailures: 1})
	r.Register("A", 0, 1)
	charge(t, r, "A", channel.ChannelError) // предохранитель открыт

	for range 5 {
		if _, why := r.TryBegin("A"); why != BreakerOpen {
			t.Fatalf("причина %q, ожидалось breaker_open", why)
		}
	}
	if got := r.Snapshot("A").Load; got != 0 {
		t.Fatalf("загрузка %.2f: отказанные запросы не должны занимать места", got)
	}
}

func TestBreakerReportedBeforeOverload(t *testing.T) {
	r, _, _ := newGuarded(t, BreakerConfig{ConsecutiveFailures: 1})
	r.Register("A", 0, 1)
	slot, _ := r.TryBegin("A")
	slot.Done(channel.ChannelError, time.Second) // открыт
	r.Begin("A")                                 // и все места заняты
	if got := r.Unavailable("A"); got != BreakerOpen {
		t.Fatalf("причина %q: отключение важнее загрузки", got)
	}
}

func TestTryBeginConcurrentNeverExceedsLimit(t *testing.T) {
	const limit = 5
	r := New(10)
	r.Register("A", 0, limit)

	var wg sync.WaitGroup
	var admitted atomic.Int32
	start := make(chan struct{})
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, why := r.TryBegin("A"); why == "" {
				admitted.Add(1) // слот не закрываем: места остаются занятыми
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := admitted.Load(); got != limit {
		t.Fatalf("пущено %d запросов при лимите %d", got, limit)
	}
}

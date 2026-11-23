package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/clock"
	"github.com/Cumparator/smart_payment_method/internal/payment"
	"github.com/Cumparator/smart_payment_method/internal/router"
	"github.com/Cumparator/smart_payment_method/internal/store"
	"github.com/Cumparator/smart_payment_method/internal/strategy"
)

// fakeStore — хранилище в памяти с теми же правилами, что и store.DB.
type fakeStore struct {
	mu        sync.Mutex
	pays      map[string]*payment.Payment
	attempts  map[string][]payment.Attempt
	decisions []store.RoutingDecision
	events    []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{pays: map[string]*payment.Payment{}, attempts: map[string][]payment.Attempt{}}
}

func (f *fakeStore) add(id string, amount int64, at time.Time) {
	f.pays[id] = &payment.Payment{ID: id, Amount: amount, Currency: "RUB", State: payment.Received, NextRunAt: at}
}

func (f *fakeStore) pay(id string) payment.Payment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.pays[id]
}

func (f *fakeStore) att(id string) []payment.Attempt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]payment.Attempt(nil), f.attempts[id]...)
}

func (f *fakeStore) ClaimPayment(_ context.Context, now time.Time) (payment.Payment, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best *payment.Payment
	for _, p := range f.pays {
		if p.State == payment.Received && !p.NextRunAt.After(now) && (best == nil || p.NextRunAt.Before(best.NextRunAt)) {
			best = p
		}
	}
	if best == nil {
		return payment.Payment{}, false, nil
	}
	best.State = payment.Processing
	return *best, true, nil
}

func (f *fakeStore) ListAttempts(_ context.Context, id string) ([]payment.Attempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]payment.Attempt(nil), f.attempts[id]...), nil
}

func (f *fakeStore) StartAttemptWithDecision(_ context.Context, id, channelID string, dec *store.RoutingDecision) (payment.Attempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pays[id]
	if !ok {
		return payment.Attempt{}, store.ErrPaymentNotFound
	}
	for _, a := range f.attempts[id] {
		if a.IsLive() {
			return payment.Attempt{}, store.ErrLiveAttemptExists
		}
	}
	if p.AttemptsCount >= payment.MaxAttempts {
		return payment.Attempt{}, store.ErrMaxAttempts
	}
	p.AttemptsCount++
	a := payment.Attempt{PaymentID: id, AttemptNo: p.AttemptsCount, ChannelID: channelID, State: payment.AttemptStarted}
	f.attempts[id] = append(f.attempts[id], a)
	if dec != nil {
		f.decisions = append(f.decisions, *dec)
	}
	return a, nil
}

func (f *fakeStore) CompleteAttempt(_ context.Context, c store.Completion) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.attempts[c.PaymentID]
	for i := range list {
		if list[i].AttemptNo != c.AttemptNo {
			continue
		}
		if list[i].State != payment.AttemptStarted {
			return false, nil
		}
		p := f.pays[c.PaymentID]
		if p.State != payment.Processing {
			return false, store.ErrStateChanged
		}
		list[i].State = c.AttemptTo
		p.State = c.PaymentTo
		if !c.NextRunAt.IsZero() {
			p.NextRunAt = c.NextRunAt
		}
		f.events = append(f.events, c.Reason)
		return true, nil
	}
	return false, nil
}

func (f *fakeStore) TransitionStatusAt(_ context.Context, id string, from, to payment.State, next time.Time, reason string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.pays[id]
	if p.State != from {
		return false, nil
	}
	p.State = to
	if !next.IsZero() {
		p.NextRunAt = next
	}
	f.events = append(f.events, reason)
	return true, nil
}

// fakeChannel отдаёт заготовленные ответы по очереди; когда они кончились — APPROVED.
type fakeChannel struct {
	mu       sync.Mutex
	id       string
	results  []channel.ChargeResult
	err      error
	onCharge func(key string)
	keys     []string
}

func (c *fakeChannel) ID() string { return c.id }
func (c *fakeChannel) Charge(_ context.Context, key string, _ int64, _ string) (channel.ChargeResult, error) {
	c.mu.Lock()
	c.keys = append(c.keys, key)
	c.mu.Unlock()
	if c.onCharge != nil {
		c.onCharge(key)
	}
	if c.err != nil {
		return channel.ChargeResult{}, c.err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.results) == 0 {
		return channel.ChargeResult{Outcome: channel.Approved}, nil
	}
	r := c.results[0]
	c.results = c.results[1:]
	return r, nil
}
func (c *fakeChannel) Status(context.Context, string) (channel.StatusResult, error) {
	return channel.StatusResult{}, nil
}
func (c *fakeChannel) Cancel(context.Context, string) (channel.CancelResult, error) {
	return channel.CancelResult{}, nil
}

type fixture struct {
	st  *fakeStore
	clk *clock.Virtual
	w   *Worker
	chs map[string]*fakeChannel
}

func newFixture(t *testing.T, chs ...*fakeChannel) *fixture {
	t.Helper()
	clk := clock.NewVirtual(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	st := newFakeStore()
	// «cheap» дешевле «mid», «mid» дешевле «fast»: порядок выбора предсказуем.
	src := router.StaticSource{
		{ChannelID: "cheap", Tariff: channel.Tariff{Fixed: 3, Percent: 0.002}},
		{ChannelID: "mid", Tariff: channel.Tariff{Fixed: 6, Percent: 0.005}},
		{ChannelID: "fast", Tariff: channel.Tariff{Fixed: 12, Percent: 0.010}},
	}
	var list []channel.PaymentChannel
	m := map[string]*fakeChannel{}
	for _, c := range chs {
		list = append(list, c)
		m[c.id] = c
	}
	w := New(st, router.New(strategy.Cheapest{}, src), list, clk, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.jitter = func() float64 { return 0.5 } // множитель ровно 1.0
	return &fixture{st: st, clk: clk, w: w, chs: m}
}

func (f *fixture) run(t *testing.T) bool {
	t.Helper()
	did, err := f.w.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}
	return did
}

func chErr() channel.ChargeResult {
	return channel.ChargeResult{Outcome: channel.ChannelError, Reason: "provider error"}
}

func TestApproved(t *testing.T) {
	f := newFixture(t, &fakeChannel{id: "cheap"}, &fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	f.st.add("p1", 10000, f.clk.Now())

	if !f.run(t) {
		t.Fatal("платёж должен быть обработан")
	}
	if p := f.st.pay("p1"); p.State != payment.Succeeded {
		t.Fatalf("state = %s, want SUCCEEDED", p.State)
	}
	a := f.st.att("p1")
	if len(a) != 1 || a[0].State != payment.AttemptApproved || a[0].ChannelID != "cheap" {
		t.Fatalf("attempts = %+v", a)
	}
	if got := f.chs["cheap"].keys; len(got) != 1 || got[0] != "p1:1" {
		t.Fatalf("ключ попытки в канале = %v, want [p1:1]", got)
	}
	if len(f.st.decisions) != 1 || f.st.decisions[0].Strategy != "cheapest" || len(f.st.decisions[0].Details) == 0 {
		t.Fatalf("решение маршрутизатора не сохранено: %+v", f.st.decisions)
	}
	if f.run(t) {
		t.Fatal("очередь должна быть пуста")
	}
}

func TestAttemptIsStoredBeforeChannelCall(t *testing.T) {
	var f *fixture
	seen := false
	cheap := &fakeChannel{id: "cheap"}
	f = newFixture(t, cheap, &fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	cheap.onCharge = func(key string) {
		a := f.st.att("p1")
		seen = len(a) == 1 && a[0].State == payment.AttemptStarted && a[0].ChannelKey() == key
	}
	f.st.add("p1", 10000, f.clk.Now())
	f.run(t)
	if !seen {
		t.Fatal("к моменту вызова канала в БД должна быть попытка в состоянии STARTED")
	}
}

func TestDeclinedFailsPayment(t *testing.T) {
	f := newFixture(t,
		&fakeChannel{id: "cheap", results: []channel.ChargeResult{{Outcome: channel.Declined, Reason: "insufficient funds"}}},
		&fakeChannel{id: "mid"})
	f.st.add("p1", 10000, f.clk.Now())
	f.run(t)

	if p := f.st.pay("p1"); p.State != payment.Failed {
		t.Fatalf("state = %s, want FAILED", p.State)
	}
	if a := f.st.att("p1"); len(a) != 1 || a[0].State != payment.AttemptDeclined {
		t.Fatalf("attempts = %+v", a)
	}
}

func TestChannelErrorRetriesInAnotherChannel(t *testing.T) {
	f := newFixture(t,
		&fakeChannel{id: "cheap", results: []channel.ChargeResult{chErr()}},
		&fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	f.st.add("p1", 10000, f.clk.Now())

	f.run(t)
	p := f.st.pay("p1")
	if p.State != payment.Received {
		t.Fatalf("после CHANNEL_ERROR state = %s, want RECEIVED", p.State)
	}
	if want := f.clk.Now().Add(time.Second); !p.NextRunAt.Equal(want) {
		t.Fatalf("next_run_at = %v, want %v", p.NextRunAt, want)
	}
	if f.run(t) {
		t.Fatal("повтор не должен начаться раньше next_run_at")
	}

	f.clk.Advance(2 * time.Second)
	if !f.run(t) {
		t.Fatal("после паузы платёж должен быть взят снова")
	}
	if p := f.st.pay("p1"); p.State != payment.Succeeded {
		t.Fatalf("state = %s, want SUCCEEDED", p.State)
	}
	a := f.st.att("p1")
	if len(a) != 2 || a[0].State != payment.AttemptChannelError || a[1].State != payment.AttemptApproved {
		t.Fatalf("attempts = %+v", a)
	}
	if a[1].ChannelID != "mid" {
		t.Fatalf("повтор ушёл в %s, want mid (другой канал)", a[1].ChannelID)
	}
	if got := f.chs["mid"].keys; len(got) != 1 || got[0] != "p1:2" {
		t.Fatalf("ключ второй попытки = %v, want [p1:2]", got)
	}
}

func TestUnknownGoesToReconciling(t *testing.T) {
	f := newFixture(t,
		&fakeChannel{id: "cheap", results: []channel.ChargeResult{{Outcome: channel.Unknown, Reason: "timeout"}}},
		&fakeChannel{id: "mid"})
	f.st.add("p1", 10000, f.clk.Now())
	f.run(t)

	p := f.st.pay("p1")
	if p.State != payment.Reconciling {
		t.Fatalf("state = %s, want RECONCILING", p.State)
	}
	if want := f.clk.Now().Add(5 * time.Second); !p.NextRunAt.Equal(want) {
		t.Fatalf("next_run_at = %v, want %v", p.NextRunAt, want)
	}
	if a := f.st.att("p1"); len(a) != 1 || a[0].State != payment.AttemptUnknown || !a[0].IsLive() {
		t.Fatalf("attempts = %+v", a)
	}
	if len(f.chs["mid"].keys) != 0 {
		t.Fatal("при UNKNOWN нельзя переключаться на другой канал")
	}
	f.clk.Advance(time.Hour)
	if f.run(t) {
		t.Fatal("воркер не должен брать платежи в RECONCILING")
	}
}

func TestChargeErrorIsUnknown(t *testing.T) {
	f := newFixture(t, &fakeChannel{id: "cheap", err: errors.New("connection reset")}, &fakeChannel{id: "mid"})
	f.st.add("p1", 10000, f.clk.Now())
	f.run(t)

	if p := f.st.pay("p1"); p.State != payment.Reconciling {
		t.Fatalf("state = %s, want RECONCILING", p.State)
	}
	if a := f.st.att("p1"); a[0].State != payment.AttemptUnknown {
		t.Fatalf("attempt = %+v", a[0])
	}
}

func TestAttemptsExhausted(t *testing.T) {
	f := newFixture(t,
		&fakeChannel{id: "cheap", results: []channel.ChargeResult{chErr()}},
		&fakeChannel{id: "mid", results: []channel.ChargeResult{chErr()}},
		&fakeChannel{id: "fast", results: []channel.ChargeResult{chErr()}})
	f.st.add("p1", 10000, f.clk.Now())

	for i := 0; i < payment.MaxAttempts; i++ {
		if !f.run(t) {
			t.Fatalf("итерация %d: платёж не взят", i)
		}
		f.clk.Advance(time.Minute)
	}
	if p := f.st.pay("p1"); p.State != payment.Failed || p.AttemptsCount != payment.MaxAttempts {
		t.Fatalf("state = %s attempts = %d, want FAILED/%d", p.State, p.AttemptsCount, payment.MaxAttempts)
	}
	if f.run(t) {
		t.Fatal("после FAILED платёж брать нельзя")
	}
}

func TestUnregisteredChannelFailsPayment(t *testing.T) {
	f := newFixture(t) // ни один канал не зарегистрирован, но роутер что-то выберет
	f.st.add("p1", 10000, f.clk.Now())
	f.run(t)

	if p := f.st.pay("p1"); p.State != payment.Failed {
		t.Fatalf("state = %s, want FAILED", p.State)
	}
	if len(f.st.att("p1")) != 0 {
		t.Fatal("попытка не должна создаваться, если канал нельзя вызвать")
	}
}

func TestRetryDelay(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		attempt int
		want    time.Duration
	}{{1, time.Second}, {2, 2 * time.Second}, {3, 4 * time.Second}, {10, 30 * time.Second}}
	for _, c := range cases {
		if got := f.w.retryDelay(c.attempt); got != c.want {
			t.Errorf("retryDelay(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
	f.w.jitter = func() float64 { return 0 }
	if got := f.w.retryDelay(1); got != 800*time.Millisecond {
		t.Errorf("с минимальным jitter retryDelay(1) = %v, want 800ms", got)
	}
}

func TestParallelWorkersDoNotShareAPayment(t *testing.T) {
	f := newFixture(t, &fakeChannel{id: "cheap"}, &fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	const n = 20
	for i := 0; i < n; i++ {
		f.st.add(fmt.Sprintf("p%d", i), 10000, f.clk.Now())
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				did, err := f.w.ProcessOne(context.Background())
				if err != nil {
					t.Errorf("ProcessOne: %v", err)
					return
				}
				if !did {
					return
				}
			}
		}()
	}
	wg.Wait()
	total := 0
	for _, c := range f.chs {
		total += len(c.keys)
	}
	if total != n {
		t.Fatalf("канал вызван %d раз, want %d (по одному на платёж)", total, n)
	}
	for i := 0; i < n; i++ {
		if p := f.st.pay(fmt.Sprintf("p%d", i)); p.State != payment.Succeeded {
			t.Fatalf("p%d: state = %s", i, p.State)
		}
	}
}

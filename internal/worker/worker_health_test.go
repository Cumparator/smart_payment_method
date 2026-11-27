package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/health"
	"github.com/Cumparator/smart_payment_method/internal/payment"
	"github.com/Cumparator/smart_payment_method/internal/router"
	"github.com/Cumparator/smart_payment_method/internal/store"
	"github.com/Cumparator/smart_payment_method/internal/strategy"
)

// guard подключает к воркеру реестр здоровья на часах фикстуры.
func (f *fixture) guard(cfg health.BreakerConfig, budget *health.RetryBudget) *health.Registry {
	reg := health.New(100, health.WithBreaker(cfg), health.WithClock(f.clk))
	f.w.WithHealth(reg, budget)
	return reg
}

func TestHealthRecordsOutcomes(t *testing.T) {
	f := newFixture(t, &fakeChannel{id: "cheap"}, &fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	reg := f.guard(health.BreakerConfig{}, nil)
	f.st.add("p1", 10000, f.clk.Now())

	f.run(t)
	if s, n := reg.Counts("cheap"); s != 1 || n != 1 {
		t.Fatalf("окно канала cheap %d/%d, ожидалось 1/1", s, n)
	}
	if got := reg.Snapshot("cheap").Load; got != 0 {
		t.Fatalf("загрузка %.2f: слот после ответа должен быть закрыт", got)
	}
}

func TestChannelErrorsOpenBreaker(t *testing.T) {
	f := newFixture(t,
		&fakeChannel{id: "cheap", results: []channel.ChargeResult{chErr()}},
		&fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	reg := f.guard(health.BreakerConfig{ConsecutiveFailures: 1}, nil)
	f.st.add("p1", 10000, f.clk.Now())

	f.run(t)
	if reg.State("cheap") != health.Open {
		t.Fatalf("состояние cheap %s, ожидалось OPEN после ошибки канала", reg.State("cheap"))
	}
}

func TestBusyChannelRequeuesPaymentWithoutAttempt(t *testing.T) {
	f := newFixture(t, &fakeChannel{id: "cheap"}, &fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	reg := f.guard(health.BreakerConfig{}, nil)
	reg.Register("cheap", 0, 1)
	if _, why := reg.TryBegin("cheap"); why != "" { // единственное место занято другим запросом
		t.Fatal(why)
	}
	f.st.add("p1", 10000, f.clk.Now())

	f.run(t)
	p := f.st.pay("p1")
	if p.State != payment.Received || p.AttemptsCount != 0 {
		t.Fatalf("state %s, попыток %d: платёж должен вернуться в очередь без попытки", p.State, p.AttemptsCount)
	}
	if want := f.clk.Now().Add(200 * time.Millisecond); !p.NextRunAt.Equal(want) {
		t.Fatalf("next_run_at %v, ожидалось %v", p.NextRunAt, want)
	}
	if len(f.chs["cheap"].keys) != 0 {
		t.Fatal("в занятый канал запрос уйти не должен")
	}
}

func TestPlaceIsReleasedWhenAttemptCannotStart(t *testing.T) {
	f := newFixture(t, &fakeChannel{id: "cheap"}, &fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	reg := f.guard(health.BreakerConfig{}, nil)
	reg.Register("cheap", 0, 1)
	f.w.store = refuseStart{f.st}
	f.st.add("p1", 10000, f.clk.Now())

	f.run(t)
	if got := reg.Unavailable("cheap"); got != "" {
		t.Fatalf("канал cheap %q: место должно вернуться, если попытка не началась", got)
	}
}

// refuseStart не даёт начать попытку: попытки платежа исчерпаны.
type refuseStart struct{ *fakeStore }

func (refuseStart) StartAttemptWithDecision(context.Context, string, string, *store.RoutingDecision) (payment.Attempt, error) {
	return payment.Attempt{}, store.ErrMaxAttempts
}

// lastEvent — причина последнего перехода, записанная фиктивным хранилищем.
func lastEvent(f *fixture) string {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	return f.st.events[len(f.st.events)-1]
}

func TestRetryBudgetStopsRetries(t *testing.T) {
	f := newFixture(t,
		&fakeChannel{id: "cheap", results: []channel.ChargeResult{chErr(), chErr()}},
		&fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	f.guard(health.BreakerConfig{ConsecutiveFailures: 100}, health.NewRetryBudget(0.2, 10, 5)) // на старте один повтор
	f.st.add("p1", 10000, f.clk.Now())
	f.run(t) // p1 занимает единственный повтор
	f.st.add("p2", 10000, f.clk.Now())
	f.run(t)
	p1, p2 := f.st.pay("p1"), f.st.pay("p2")
	if p1.State != payment.Received {
		t.Fatalf("p1: state %s, ожидался повтор (RECEIVED)", p1.State)
	}
	if p2.State != payment.Failed || p2.AttemptsCount != 1 {
		t.Fatalf("p2: state %s, попыток %d: бюджет исчерпан, повтора быть не должно", p2.State, p2.AttemptsCount)
	}
	a := f.st.att("p2")
	if len(a) != 1 || a[0].State != payment.AttemptChannelError {
		t.Fatalf("попытки p2: %+v", a)
	}
	if reason := lastEvent(f); !strings.Contains(reason, "бюджет повторов") {
		t.Fatalf("причина %q", reason)
	}
}

func TestRetryBudgetCountsOnlyFirstAttempts(t *testing.T) {
	f := newFixture(t,
		&fakeChannel{id: "cheap", results: []channel.ChargeResult{chErr()}},
		&fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	budget := health.NewRetryBudget(0.2, 10, 5)
	f.guard(health.BreakerConfig{ConsecutiveFailures: 100}, budget)
	f.st.add("p1", 10000, f.clk.Now())

	f.run(t) // первая попытка, ошибка канала, повтор занял бюджет
	f.clk.Advance(2 * time.Second)
	f.run(t) // вторая попытка проходит; в бюджет она как «первая» попасть не должна
	if p := f.st.pay("p1"); p.State != payment.Succeeded {
		t.Fatalf("state %s, ожидался SUCCEEDED", p.State)
	}
	// в окне одна первая попытка и один повтор: ещё одного повтора на базе 5 нет
	if budget.TryRetry() {
		t.Fatal("вторая попытка p1 ошибочно записана как первая")
	}
}

func TestWithoutHealthNothingChanges(t *testing.T) {
	f := newFixture(t,
		&fakeChannel{id: "cheap", results: []channel.ChargeResult{chErr()}},
		&fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	f.st.add("p1", 10000, f.clk.Now())
	f.run(t)
	if p := f.st.pay("p1"); p.State != payment.Received {
		t.Fatalf("state %s: без health повтор должен работать как раньше", p.State)
	}
}

func threeChannels() router.StaticSource {
	return router.StaticSource{
		{ChannelID: "cheap", Tariff: channel.Tariff{Fixed: 3, Percent: 0.002}},
		{ChannelID: "mid", Tariff: channel.Tariff{Fixed: 6, Percent: 0.005}},
		{ChannelID: "fast", Tariff: channel.Tariff{Fixed: 12, Percent: 0.010}},
	}
}

// Роутер с тем же реестром не отправляет платёж в занятый канал, и воркеру
// не приходится возвращать его в очередь.
func TestHealthAwareRouterAvoidsBusyChannel(t *testing.T) {
	f := newFixture(t, &fakeChannel{id: "cheap"}, &fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	reg := f.guard(health.BreakerConfig{}, nil)
	f.w.router = router.New(strategy.Cheapest{}, threeChannels()).WithHealth(reg)
	reg.Register("cheap", 0, 1)
	reg.TryBegin("cheap") // место в cheap занято
	f.st.add("p1", 10000, f.clk.Now())

	f.run(t)
	a := f.st.att("p1")
	if p := f.st.pay("p1"); p.State != payment.Succeeded || len(a) != 1 || a[0].ChannelID != "mid" {
		t.Fatalf("state %s, попытки %+v: ожидался успех через mid", p.State, a)
	}
}

func TestAllChannelsBusyRequeuesPayment(t *testing.T) {
	f := newFixture(t, &fakeChannel{id: "cheap"}, &fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	reg := f.guard(health.BreakerConfig{}, nil)
	f.w.router = router.New(strategy.Cheapest{}, threeChannels()).WithHealth(reg)
	for _, id := range []string{"cheap", "mid", "fast"} {
		reg.Register(id, 0, 1)
		reg.TryBegin(id)
	}
	f.st.add("p1", 10000, f.clk.Now())

	f.run(t)
	p := f.st.pay("p1")
	if p.State != payment.Received || p.AttemptsCount != 0 {
		t.Fatalf("state %s, попыток %d: при занятых каналах платёж должен ждать, а не падать", p.State, p.AttemptsCount)
	}
	if want := f.clk.Now().Add(200 * time.Millisecond); !p.NextRunAt.Equal(want) {
		t.Fatalf("next_run_at %v, ожидалось %v", p.NextRunAt, want)
	}
}

func TestPaymentProceedsWhenChannelsFreeUp(t *testing.T) {
	f := newFixture(t, &fakeChannel{id: "cheap"}, &fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	reg := f.guard(health.BreakerConfig{}, nil)
	f.w.router = router.New(strategy.Cheapest{}, threeChannels()).WithHealth(reg)
	reg.Register("cheap", 0, 1)
	slot, _ := reg.TryBegin("cheap")
	reg.Register("mid", 0, 1)
	reg.TryBegin("mid")
	reg.Register("fast", 0, 1)
	reg.TryBegin("fast")
	f.st.add("p1", 10000, f.clk.Now())

	f.run(t)
	slot.Done(channel.Approved, 0) // cheap освободился
	f.clk.Advance(300 * time.Millisecond)
	if !f.run(t) {
		t.Fatal("после паузы платёж должен быть взят снова")
	}
	if p := f.st.pay("p1"); p.State != payment.Succeeded {
		t.Fatalf("state %s, ожидался SUCCEEDED", p.State)
	}
}

// Каналы без места — не то же самое, что нет каналов: «нет канала» заканчивается неудачей.
func TestNoChannelStillFailsPayment(t *testing.T) {
	f := newFixture(t, &fakeChannel{id: "cheap"}, &fakeChannel{id: "mid"}, &fakeChannel{id: "fast"})
	reg := f.guard(health.BreakerConfig{ConsecutiveFailures: 1}, nil)
	f.w.router = router.New(strategy.Cheapest{}, threeChannels()).WithHealth(reg)
	for _, id := range []string{"cheap", "mid", "fast"} {
		reg.Record(id, channel.ChannelError, 0) // предохранители открыты
	}
	f.st.add("p1", 10000, f.clk.Now())

	f.run(t)
	if p := f.st.pay("p1"); p.State != payment.Failed {
		t.Fatalf("state %s, ожидался FAILED: все каналы отключены", p.State)
	}
}

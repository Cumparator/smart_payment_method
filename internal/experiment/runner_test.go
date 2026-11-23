package experiment

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/payment"
	"github.com/Cumparator/smart_payment_method/internal/strategy"
)

var start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// expectedCost — вторая стратегия; параметры те же, что в strategy_test.go.
var expectedCost = strategy.ExpectedCost{FailurePenalty: 5_000, SecondCost: 50, LoadThreshold: 0.7, LoadPenalty: 1_000}

// handmade собирает сценарий из заданных каналов и одного платежа на 1000 ₽.
func handmade(payments int, chs ...ChannelSpec) Scenario {
	s := Scenario{Header: Header{Start: start, Duration: Dur(time.Hour), Channels: chs}}
	for i := range payments {
		s.Payments = append(s.Payments, Payment{
			ID: "p" + string(rune('a'+i)), At: Dur(time.Duration(i) * time.Minute), Amount: 100_000, Currency: "RUB",
		})
	}
	return s
}

func spec(id string, percent float64) ChannelSpec {
	return ChannelSpec{ID: id, Percent: percent, Latency: Dur(100 * time.Millisecond)}
}

func mustRun(t *testing.T, sc Scenario, strat strategy.Strategy, seed int64) Result {
	t.Helper()
	r, err := Run(context.Background(), sc, strat, seed)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunApproved(t *testing.T) {
	r := mustRun(t, handmade(3, spec("A", 0.01)), strategy.Cheapest{}, 1)
	for _, p := range r.Payments {
		if p.Final != payment.Succeeded || p.ChannelID != "A" || len(p.Attempts) != 1 || p.Fee != 1000 {
			t.Fatalf("%+v", p)
		}
		if p.Duration != 100*time.Millisecond {
			t.Fatalf("длительность %s", p.Duration)
		}
	}
}

func TestRunChannelErrorSwitchesChannel(t *testing.T) {
	broken := spec("A", 0.001) // самый дешёвый, но всегда отвечает ошибкой
	broken.ErrorRate = 1
	r := mustRun(t, handmade(1, broken, spec("B", 0.02)), strategy.Cheapest{}, 1)

	p := r.Payments[0]
	if p.Final != payment.Succeeded || p.ChannelID != "B" {
		t.Fatalf("%+v", p)
	}
	if len(p.Attempts) != 2 || p.Attempts[0].ChannelID != "A" || p.Attempts[0].Outcome != channel.ChannelError {
		t.Fatalf("попытки %+v", p.Attempts)
	}
	if p.Reconciled {
		t.Fatal("ошибка канала не должна отправлять платёж на сверку")
	}
}

func TestRunAllChannelsFailed(t *testing.T) {
	broken := spec("A", 0.01)
	broken.ErrorRate = 1
	p := mustRun(t, handmade(1, broken), strategy.Cheapest{}, 1).Payments[0]
	if p.Final != payment.Failed || len(p.Attempts) != 1 {
		t.Fatalf("%+v: после ошибки единственного канала повторять некуда", p)
	}
}

func TestRunDeclinedIsFinal(t *testing.T) {
	a := spec("A", 0.001)
	a.DeclineRate = 1
	p := mustRun(t, handmade(1, a, spec("B", 0.02)), strategy.Cheapest{}, 1).Payments[0]
	if p.Final != payment.Failed || len(p.Attempts) != 1 {
		t.Fatalf("%+v: бизнес-отказ нельзя повторять в другом канале", p)
	}
}

func TestRunTimeoutChargedIsSettledByReconciliation(t *testing.T) {
	a := spec("A", 0.01)
	a.TimeoutRate, a.TimeoutChargeRate = 1, 1
	r := mustRun(t, handmade(1, a, spec("B", 0.02)), strategy.Cheapest{}, 1)

	p := r.Payments[0]
	if p.Final != payment.Succeeded || p.ChannelID != "A" || !p.Reconciled || len(p.Attempts) != 1 {
		t.Fatalf("%+v: деньги списаны, платёж должен закрыться сверкой без смены канала", p)
	}
	if p.Duration < ReconcileDelay {
		t.Fatalf("длительность %s не включает сверку", p.Duration)
	}
	if got := r.chargedByPayment()[p.ID]; got != 1 {
		t.Fatalf("списаний %d, ожидалось 1", got)
	}
}

func TestRunTimeoutNotChargedIsRetried(t *testing.T) {
	a := spec("A", 0.01)
	a.TimeoutRate = 1 // таймаут без списания
	r := mustRun(t, handmade(1, a), strategy.Cheapest{}, 1)

	p := r.Payments[0]
	if p.Final != payment.Failed || len(p.Attempts) != payment.MaxAttempts || !p.Reconciled {
		t.Fatalf("%+v: ожидалось %d попыток через сверку", p, payment.MaxAttempts)
	}
	if got := r.chargedByPayment()[p.ID]; got != 0 {
		t.Fatalf("списаний %d у проваленного платежа", got)
	}
}

func TestRunNoMoneyLost(t *testing.T) {
	sc, err := Generate(Options{Seed: 3, Payments: 3000, Duration: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for _, strat := range []strategy.Strategy{strategy.Cheapest{}, expectedCost} {
		name := strat.Name()
		r := mustRun(t, sc, strat, 11)
		charged := r.chargedByPayment()
		for _, p := range r.Payments {
			want := 0
			if p.Final == payment.Succeeded {
				want = 1
			}
			if charged[p.ID] != want {
				t.Fatalf("%s: платёж %s завершён как %s, но списан %d раз", name, p.ID, p.Final, charged[p.ID])
			}
			if len(p.Attempts) == 0 || len(p.Attempts) > payment.MaxAttempts {
				t.Fatalf("%s: платёж %s, попыток %d", name, p.ID, len(p.Attempts))
			}
		}
	}
}

func TestRunDeterministic(t *testing.T) {
	sc, _ := Generate(Options{Seed: 5, Payments: 1500, Duration: 24 * time.Hour})
	a := mustRun(t, sc, expectedCost, 9)
	b := mustRun(t, sc, expectedCost, 9)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("повторный прогон с тем же seed дал другой результат")
	}
	c := mustRun(t, sc, expectedCost, 10)
	if reflect.DeepEqual(a.Payments, c.Payments) {
		t.Fatal("другой seed дал тот же результат")
	}
}

func TestRunRejectsUnsortedScenario(t *testing.T) {
	sc := handmade(2, spec("A", 0.01))
	sc.Payments[0].At, sc.Payments[1].At = sc.Payments[1].At, sc.Payments[0].At
	if _, err := Run(context.Background(), sc, strategy.Cheapest{}, 1); err == nil {
		t.Fatal("ожидалась ошибка для неупорядоченного сценария")
	}
	if _, err := Run(context.Background(), Scenario{}, strategy.Cheapest{}, 1); err == nil {
		t.Fatal("ожидалась ошибка для сценария без каналов")
	}
}

func TestRunRejectsInvalidChannel(t *testing.T) {
	a := spec("A", 0.01)
	a.Outages = []OutageSpec{{From: Dur(time.Hour), To: Dur(time.Minute)}}
	if _, err := Run(context.Background(), handmade(1, a), strategy.Cheapest{}, 1); err == nil {
		t.Fatal("ожидалась ошибка для канала с некорректной аварией")
	}
}

func TestRunOutageStopsApprovals(t *testing.T) {
	a := spec("A", 0.001)
	a.Outages = []OutageSpec{{From: Dur(10 * time.Minute), To: Dur(20 * time.Minute)}}
	sc := handmade(30, a, spec("B", 0.02))
	r := mustRun(t, sc, strategy.Cheapest{}, 1)

	for _, p := range r.Payments {
		inOutage := p.At >= 10*time.Minute && p.At < 20*time.Minute
		first := p.Attempts[0]
		if first.ChannelID != "A" {
			t.Fatalf("%s: дешёвый канал A должен быть выбран первым", p.ID)
		}
		if inOutage && first.Outcome != channel.ChannelError {
			t.Fatalf("%s: в аварии A ответил %s", p.ID, first.Outcome)
		}
		if !inOutage && first.Outcome != channel.Approved {
			t.Fatalf("%s: вне аварии A ответил %s", p.ID, first.Outcome)
		}
		if inOutage && p.ChannelID != "B" {
			t.Fatalf("%s: в аварии платёж должен уйти в B, ушёл в %q", p.ID, p.ChannelID)
		}
	}
}

package router

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/clock"
	"github.com/Cumparator/smart_payment_method/internal/health"
	"github.com/Cumparator/smart_payment_method/internal/payment"
	"github.com/Cumparator/smart_payment_method/internal/store"
	"github.com/Cumparator/smart_payment_method/internal/strategy"
)

var healthNow = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// twoChannels — «cheap» дешевле «solid», оба без ограничений.
func twoChannels() *fakeRepository {
	return &fakeRepository{channels: []store.ChannelRecord{
		channelRecord("cheap", true, `{"tariff":{"percent":0.010}}`, 500),
		channelRecord("solid", true, `{"tariff":{"percent":0.015}}`, 500),
	}}
}

func ranked(t *testing.T, r *Router) []strategy.Scored {
	t.Helper()
	d, err := r.Select(context.Background(), payment.Payment{ID: "p1", Amount: 100_000})
	if err != nil {
		t.Fatal(err)
	}
	return d.Ranked
}

var expectedCost = strategy.ExpectedCost{FailurePenalty: 5_000, SecondCost: 50, LoadThreshold: 0.7, LoadPenalty: 1_000}

func TestHealthStatsChangeRanking(t *testing.T) {
	reg := health.New(100)
	for range 19 {
		reg.Record("cheap", channel.ChannelError, 500*time.Millisecond)
	}
	reg.Record("cheap", channel.Approved, 500*time.Millisecond)
	for range 20 {
		reg.Record("solid", channel.Approved, 500*time.Millisecond)
	}

	neutral := NewRepository(twoChannels(), clock.NewVirtual(healthNow), expectedCost)
	if got := ranked(t, neutral)[0].ChannelID; got != "cheap" {
		t.Fatalf("без health выбран %s: статистика нейтральна, ожидался более дешёвый cheap", got)
	}

	informed := NewRepository(twoChannels(), clock.NewVirtual(healthNow), expectedCost).WithHealth(reg)
	if got := ranked(t, informed)[0].ChannelID; got != "solid" {
		t.Fatalf("с health выбран %s, ожидался надёжный solid", got)
	}
}

func TestHealthFillsCandidateFields(t *testing.T) {
	reg := health.New(100)
	reg.Register("cheap", 0, 4)
	reg.Record("cheap", channel.Approved, 2*time.Second)
	reg.Record("cheap", channel.ChannelError, 2*time.Second)
	reg.Begin("cheap") // один запрос в полёте из четырёх мест
	r := NewRepository(twoChannels(), clock.NewVirtual(healthNow), strategy.Cheapest{}).WithHealth(reg)

	var cheap strategy.Candidate
	for _, s := range ranked(t, r) {
		if s.ChannelID == "cheap" {
			cheap = s.Candidate
		}
	}
	if cheap.Successes != 1 || cheap.Attempts != 2 || cheap.Load != 0.25 || cheap.LatencyS != 2 {
		t.Fatalf("кандидат %+v: ожидалось 1/2, загрузка 0.25, задержка 2 с", cheap)
	}
}

func TestLatencyFallsBackToChannelConfig(t *testing.T) {
	r := NewRepository(twoChannels(), clock.NewVirtual(healthNow), strategy.Cheapest{}).WithHealth(health.New(100))
	for _, s := range ranked(t, r) {
		if s.LatencyS != 0.5 {
			t.Fatalf("%s: задержка %.2f, ожидалась 0.5 из настроек канала, пока замеров нет", s.ChannelID, s.LatencyS)
		}
	}
}

// guarded — реестр с предохранителями на часах теста.
func guarded(clk clock.Clock) *health.Registry {
	return health.New(100, health.WithBreaker(health.BreakerConfig{ConsecutiveFailures: 1, OpenFor: time.Minute}), health.WithClock(clk))
}

func openBreaker(reg *health.Registry, id string) { reg.Record(id, channel.ChannelError, 0) }

func fillPlaces(t *testing.T, reg *health.Registry, id string) {
	t.Helper()
	reg.Register(id, 0, 1)
	if _, why := reg.TryBegin(id); why != "" {
		t.Fatal(why)
	}
}

func choose(r *Router) (Decision, error) {
	return r.ChoosePayment(context.Background(), payment.Payment{ID: "p1", Amount: 100_000}, nil)
}

func TestRouterSkipsChannelWithOpenBreaker(t *testing.T) {
	clk := clock.NewVirtual(healthNow)
	reg := guarded(clk)
	openBreaker(reg, "cheap")
	r := NewRepository(twoChannels(), clk, strategy.Cheapest{}).WithHealth(reg)

	d, err := r.Select(context.Background(), payment.Payment{ID: "p1", Amount: 100_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Ranked) != 1 || d.Ranked[0].ChannelID != "solid" {
		t.Fatalf("ranked %+v, ожидался только solid", d.Ranked)
	}
	if reason, ok := exclusionFor(d, "cheap"); !ok || reason != ReasonBreakerOpen {
		t.Fatalf("причина для cheap %q (%v), ожидалась breaker_open", reason, ok)
	}
}

func TestRouterSkipsOverloadedChannel(t *testing.T) {
	clk := clock.NewVirtual(healthNow)
	reg := guarded(clk)
	fillPlaces(t, reg, "cheap")
	r := NewRepository(twoChannels(), clk, strategy.Cheapest{}).WithHealth(reg)

	d, err := choose(r)
	if err != nil {
		t.Fatal(err)
	}
	if d.ChannelID != "solid" {
		t.Fatalf("выбран %s, ожидался solid: cheap перегружен", d.ChannelID)
	}
}

func TestRouterUsesChannelAgainWhenPlaceFrees(t *testing.T) {
	clk := clock.NewVirtual(healthNow)
	reg := guarded(clk)
	reg.Register("cheap", 0, 1)
	slot, _ := reg.TryBegin("cheap")
	r := NewRepository(twoChannels(), clk, strategy.Cheapest{}).WithHealth(reg)

	if d, _ := choose(r); d.ChannelID != "solid" {
		t.Fatalf("пока cheap занят, выбран %s", d.ChannelID)
	}
	slot.Done(channel.Approved, 0)
	if d, _ := choose(r); d.ChannelID != "cheap" {
		t.Fatalf("после освобождения выбран %s, ожидался cheap", d.ChannelID)
	}
}

func TestRouterReturnsToChannelAfterBreakerPause(t *testing.T) {
	clk := clock.NewVirtual(healthNow)
	reg := guarded(clk)
	openBreaker(reg, "cheap")
	r := NewRepository(twoChannels(), clk, strategy.Cheapest{}).WithHealth(reg)

	if d, _ := choose(r); d.ChannelID != "solid" {
		t.Fatalf("пока предохранитель открыт, выбран %s", d.ChannelID)
	}
	clk.Advance(time.Minute) // пауза прошла: канал получает пробу
	if d, _ := choose(r); d.ChannelID != "cheap" {
		t.Fatalf("после паузы выбран %s, ожидался cheap для пробы", d.ChannelID)
	}
}

func TestAllChannelsOverloadedIsBusyNotNoChannel(t *testing.T) {
	clk := clock.NewVirtual(healthNow)
	reg := guarded(clk)
	fillPlaces(t, reg, "cheap")
	fillPlaces(t, reg, "solid")
	r := NewRepository(twoChannels(), clk, strategy.Cheapest{}).WithHealth(reg)

	if _, err := choose(r); !errors.Is(err, ErrChannelsBusy) || errors.Is(err, ErrNoChannel) {
		t.Fatalf("ошибка %v, ожидалась ErrChannelsBusy", err)
	}
}

func TestAllChannelsDownIsNoChannel(t *testing.T) {
	clk := clock.NewVirtual(healthNow)
	reg := guarded(clk)
	openBreaker(reg, "cheap")
	openBreaker(reg, "solid")
	r := NewRepository(twoChannels(), clk, strategy.Cheapest{}).WithHealth(reg)

	if _, err := choose(r); !errors.Is(err, ErrNoChannel) || errors.Is(err, ErrChannelsBusy) {
		t.Fatalf("ошибка %v, ожидалась ErrNoChannel: отключённые каналы сами не освободятся", err)
	}
}

func TestOverloadedAndDownIsBusy(t *testing.T) {
	clk := clock.NewVirtual(healthNow)
	reg := guarded(clk)
	openBreaker(reg, "cheap")
	fillPlaces(t, reg, "solid")
	r := NewRepository(twoChannels(), clk, strategy.Cheapest{}).WithHealth(reg)

	if _, err := choose(r); !errors.Is(err, ErrChannelsBusy) {
		t.Fatalf("ошибка %v: solid освободится, платёж надо подождать", err)
	}
}

func TestSourcePathSkipsUnavailableAndExplains(t *testing.T) {
	clk := clock.NewVirtual(healthNow)
	reg := guarded(clk)
	openBreaker(reg, "cheap")
	fillPlaces(t, reg, "mid")
	r := New(strategy.Cheapest{}, cands()).WithHealth(reg)

	d, err := r.Choose(context.Background(), 10_000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.ChannelID != "fast" {
		t.Fatalf("выбран %s, ожидался fast", d.ChannelID)
	}
	var det details
	if err := json.Unmarshal(d.Details, &det); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range det.Excluded {
		got[e.ChannelID] = e.Reason
	}
	if got["cheap"] != "breaker_open" || got["mid"] != "overloaded" || len(got) != 2 {
		t.Fatalf("исключены %+v, ожидалось cheap=breaker_open, mid=overloaded", got)
	}
}

func TestSourcePathAllBusy(t *testing.T) {
	clk := clock.NewVirtual(healthNow)
	reg := guarded(clk)
	for _, id := range []string{"cheap", "mid", "fast"} {
		fillPlaces(t, reg, id)
	}
	r := New(strategy.Cheapest{}, cands()).WithHealth(reg)

	if _, err := r.Choose(context.Background(), 10_000, nil); !errors.Is(err, ErrChannelsBusy) {
		t.Fatalf("ошибка %v, ожидалась ErrChannelsBusy", err)
	}
}

func TestTriedChannelsAreNotBusy(t *testing.T) {
	reg := guarded(clock.NewVirtual(healthNow))
	r := New(strategy.Cheapest{}, cands()).WithHealth(reg)
	if _, err := r.Choose(context.Background(), 10_000, []string{"cheap", "mid", "fast"}); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("ошибка %v: исчерпанные каналы — это ErrNoChannel, а не «заняты»", err)
	}
}

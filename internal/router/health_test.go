package router

import (
	"context"
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

package router

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/clock"
	"github.com/Cumparator/smart_payment_method/internal/payment"
	"github.com/Cumparator/smart_payment_method/internal/store"
	"github.com/Cumparator/smart_payment_method/internal/strategy"
)

type fakeRepository struct {
	channels  []store.ChannelRecord
	attempts  []payment.Attempt
	live      *payment.Attempt
	liveCalls int
}

func (f *fakeRepository) ListChannels(context.Context) ([]store.ChannelRecord, error) {
	return f.channels, nil
}

func (f *fakeRepository) GetLiveAttempt(context.Context, string) (payment.Attempt, bool, error) {
	f.liveCalls++
	if f.live == nil {
		return payment.Attempt{}, false, nil
	}
	return *f.live, true, nil
}

func (f *fakeRepository) ListAttempts(context.Context, string) ([]payment.Attempt, error) {
	return f.attempts, nil
}

func channelRecord(id string, enabled bool, cfg string, latencyMS int) store.ChannelRecord {
	return store.ChannelRecord{
		ID: id, Name: id, IsEnabled: enabled, AvgLatencyMS: latencyMS,
		Config: json.RawMessage(cfg),
	}
}

func exclusionFor(d Decision, id string) (ExclusionReason, bool) {
	for _, exclusion := range d.Excluded {
		if exclusion.ChannelID == id {
			return exclusion.Reason, true
		}
	}
	return "", false
}

func TestSelectFiltersAmountLimitsInclusive(t *testing.T) {
	repo := &fakeRepository{channels: []store.ChannelRecord{
		channelRecord("min-boundary", true, `{"min_amount":1000}`, 100),
		channelRecord("max-boundary", true, `{"max_amount":1000}`, 100),
		channelRecord("too-expensive", true, `{"min_amount":1001}`, 100),
		channelRecord("too-cheap", true, `{"max_amount":999}`, 100),
		channelRecord("no-limits", true, `{}`, 100),
	}}
	r := New(repo, clock.NewVirtual(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)), strategy.Cheapest{})

	got, err := r.Select(context.Background(), payment.Payment{ID: "p1", Amount: 1000})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"min-boundary": true, "max-boundary": true, "no-limits": true}
	if len(got.Ranked) != len(want) {
		t.Fatalf("ranked %d candidates, want %d: %+v", len(got.Ranked), len(want), got.Ranked)
	}
	for _, candidate := range got.Ranked {
		if !want[candidate.ChannelID] {
			t.Errorf("unexpected candidate %q", candidate.ChannelID)
		}
	}
	if reason, ok := exclusionFor(got, "too-expensive"); !ok || reason != ReasonBelowMinAmount {
		t.Errorf("too-expensive exclusion = %q, %v", reason, ok)
	}
	if reason, ok := exclusionFor(got, "too-cheap"); !ok || reason != ReasonAboveMaxAmount {
		t.Errorf("too-cheap exclusion = %q, %v", reason, ok)
	}
}

func TestSelectUsesUTCForSchedule(t *testing.T) {
	// 22:30 in UTC-7 is 05:30 UTC on the following day: inside 22:00-06:00.
	local := time.Date(2026, 11, 5, 22, 30, 0, 0, time.FixedZone("UTC-7", -7*60*60))
	repo := &fakeRepository{channels: []store.ChannelRecord{
		channelRecord("night", true, `{"schedule":"22:00-06:00"}`, 100),
		channelRecord("day", true, `{"schedule":"06:00-22:00"}`, 100),
	}}
	r := New(repo, clock.NewVirtual(local), strategy.Cheapest{})

	got, err := r.Select(context.Background(), payment.Payment{ID: "p1", Amount: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ranked) != 1 || got.Ranked[0].ChannelID != "night" {
		t.Fatalf("UTC schedule selected %+v, want only night", got.Ranked)
	}
	if reason, ok := exclusionFor(got, "day"); !ok || reason != ReasonOutsideSchedule {
		t.Errorf("day exclusion = %q, %v", reason, ok)
	}
}

func TestSelectExcludesChannelErrorButNotDecline(t *testing.T) {
	repo := &fakeRepository{
		channels: []store.ChannelRecord{
			channelRecord("technical-failure", true, `{}`, 100),
			channelRecord("business-decline", true, `{}`, 100),
			channelRecord("unused", true, `{}`, 100),
		},
		attempts: []payment.Attempt{
			{PaymentID: "p1", AttemptNo: 1, ChannelID: "technical-failure", State: payment.AttemptChannelError},
			{PaymentID: "p1", AttemptNo: 2, ChannelID: "business-decline", State: payment.AttemptDeclined},
		},
	}
	r := New(repo, clock.NewVirtual(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)), strategy.Cheapest{})

	got, err := r.Select(context.Background(), payment.Payment{ID: "p1", Amount: 100})
	if err != nil {
		t.Fatal(err)
	}
	if reason, ok := exclusionFor(got, "technical-failure"); !ok || reason != ReasonPreviousChannelErr {
		t.Errorf("technical-failure exclusion = %q, %v", reason, ok)
	}
	seen := make(map[string]bool)
	for _, candidate := range got.Ranked {
		seen[candidate.ChannelID] = true
	}
	if !seen["business-decline"] || !seen["unused"] || seen["technical-failure"] {
		t.Fatalf("wrong candidates after history filter: %+v", got.Ranked)
	}
}

func TestSelectStopsForLiveAttempt(t *testing.T) {
	for _, state := range []payment.AttemptState{payment.AttemptStarted, payment.AttemptUnknown} {
		t.Run(string(state), func(t *testing.T) {
			live := payment.Attempt{PaymentID: "p1", AttemptNo: 1, ChannelID: "ch1", State: state}
			repo := &fakeRepository{live: &live}
			r := New(repo, nil, strategy.Cheapest{})
			got, err := r.Select(context.Background(), payment.Payment{ID: "p1", Amount: 100})
			if err != nil {
				t.Fatal(err)
			}
			if got.LiveAttempt == nil || got.LiveAttempt.State != state {
				t.Fatalf("live attempt = %+v, want %s", got.LiveAttempt, state)
			}
			if len(got.Ranked) != 0 || len(got.Excluded) != 0 {
				t.Fatalf("router continued despite live attempt: %+v", got)
			}
		})
	}
}

func TestSelectSkipsDisabledChannel(t *testing.T) {
	repo := &fakeRepository{channels: []store.ChannelRecord{
		channelRecord("disabled", false, `{}`, 100),
		channelRecord("enabled", true, `{}`, 100),
	}}
	r := New(repo, nil, nil)
	got, err := r.Select(context.Background(), payment.Payment{ID: "p1", Amount: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ranked) != 1 || got.Ranked[0].ChannelID != "enabled" {
		t.Fatalf("ranked = %+v, want only enabled", got.Ranked)
	}
	if reason, ok := exclusionFor(got, "disabled"); !ok || reason != ReasonDisabled {
		t.Errorf("disabled exclusion = %q, %v", reason, ok)
	}
}

func TestCandidateUsesNeutralStatsAndConfiguredLatency(t *testing.T) {
	repo := &fakeRepository{channels: []store.ChannelRecord{
		channelRecord("ch1", true, `{"tariff":{"fixed":3,"percent":0.01}}`, 1250),
	}}
	r := New(repo, nil, strategy.Cheapest{})
	got, err := r.Select(context.Background(), payment.Payment{ID: "p1", Amount: 100})
	if err != nil {
		t.Fatal(err)
	}
	candidate := got.Ranked[0].Candidate
	if candidate.Successes != 0 || candidate.Attempts != 0 || candidate.Load != 0 || candidate.LatencyS != 1.25 {
		t.Fatalf("candidate stats = %+v, want neutral stats and 1.25s latency", candidate)
	}
	if candidate.Tariff.Fixed != 3 || candidate.Tariff.Percent != 0.01 {
		t.Fatalf("candidate tariff = %+v", candidate.Tariff)
	}
}

func TestIsWithinScheduleBoundariesAndInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		now  string
		want bool
	}{
		{name: "start inclusive", now: "22:00", want: true},
		{name: "before end", now: "05:59", want: true},
		{name: "end exclusive", now: "06:00", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := time.Parse("15:04", tc.now)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 11, 5, parsed.Hour(), parsed.Minute(), 0, 0, time.UTC)
			got, err := isWithinSchedule("22:00-06:00", now)
			if err != nil || got != tc.want {
				t.Fatalf("isWithinSchedule = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if _, err := isWithinSchedule("not-a-schedule", time.Now()); err == nil {
		t.Fatal("expected malformed schedule error")
	}
}

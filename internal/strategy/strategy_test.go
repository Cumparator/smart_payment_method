package strategy

import (
	"testing"

	"github.com/Cumparator/smart_payment_method/internal/channel"
)

var candidates = []Candidate{
	// дешёвый, но ненадёжный
	{ChannelID: "A", Tariff: channel.Tariff{Fixed: 0, Percent: 0.010}, Successes: 5, Attempts: 50, LatencyS: 2},
	// чуть дороже, но надёжный
	{ChannelID: "B", Tariff: channel.Tariff{Fixed: 0, Percent: 0.015}, Successes: 49, Attempts: 50, LatencyS: 0.3},
}

func TestCheapestPicksLowestFee(t *testing.T) {
	if got := Rank(Cheapest{}, 100_000, candidates)[0].ChannelID; got != "A" {
		t.Fatalf("cheapest выбрал %s, ожидался A", got)
	}
}

func TestExpectedCostPrefersReliable(t *testing.T) {
	s := ExpectedCost{FailurePenalty: 5_000, SecondCost: 50, LoadThreshold: 0.7, LoadPenalty: 1_000}
	if got := Rank(s, 100_000, candidates)[0].ChannelID; got != "B" {
		t.Fatalf("expected_cost выбрал %s, ожидался B", got)
	}
}

func TestPSuccessLaplace(t *testing.T) {
	if p := (Candidate{}).PSuccess(); p != 0.5 {
		t.Fatalf("без статистики p = %v, ожидалось 0.5", p)
	}
}

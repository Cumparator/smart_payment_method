package router

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/strategy"
)

func cands() StaticSource {
	return StaticSource{
		{ChannelID: "cheap", Tariff: channel.Tariff{Fixed: 3, Percent: 0.002}},
		{ChannelID: "mid", Tariff: channel.Tariff{Fixed: 6, Percent: 0.005}},
		{ChannelID: "fast", Tariff: channel.Tariff{Fixed: 12, Percent: 0.010}},
	}
}

func TestChooseCheapest(t *testing.T) {
	r := New(strategy.Cheapest{}, cands())
	d, err := r.Choose(context.Background(), 10000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.ChannelID != "cheap" || d.Strategy != "cheapest" {
		t.Fatalf("decision = %+v", d)
	}
	var det details
	if err := json.Unmarshal(d.Details, &det); err != nil {
		t.Fatal(err)
	}
	if len(det.Candidates) != 3 || det.Chosen != "cheap" || det.Candidates[0].ChannelID != "cheap" {
		t.Fatalf("details = %+v", det)
	}
}

func TestChooseExcludesTried(t *testing.T) {
	r := New(strategy.Cheapest{}, cands())
	d, err := r.Choose(context.Background(), 10000, []string{"cheap"})
	if err != nil {
		t.Fatal(err)
	}
	if d.ChannelID != "mid" {
		t.Fatalf("channel = %s, want mid", d.ChannelID)
	}
	var det details
	if err := json.Unmarshal(d.Details, &det); err != nil {
		t.Fatal(err)
	}
	if len(det.Excluded) != 1 || det.Excluded[0].ChannelID != "cheap" {
		t.Fatalf("excluded = %+v", det.Excluded)
	}
}

func TestChooseNoChannel(t *testing.T) {
	r := New(strategy.Cheapest{}, cands())
	_, err := r.Choose(context.Background(), 10000, []string{"cheap", "mid", "fast"})
	if !errors.Is(err, ErrNoChannel) {
		t.Fatalf("err = %v, want ErrNoChannel", err)
	}
}

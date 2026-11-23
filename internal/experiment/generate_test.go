package experiment

import (
	"math"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/clock"
	"github.com/Cumparator/smart_payment_method/internal/simulator"
)

func opts() Options {
	return Options{Seed: 1, Payments: 20_000, Duration: 24 * time.Hour}
}

func TestGenerateDeterministic(t *testing.T) {
	a, err := Generate(opts())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Generate(opts())
	if !reflect.DeepEqual(a, b) {
		t.Fatal("один seed дал разные сценарии")
	}
	o := opts()
	o.Seed = 2
	c, _ := Generate(o)
	if reflect.DeepEqual(a.Payments, c.Payments) {
		t.Fatal("разные seed дали одинаковые платежи")
	}
}

func TestGeneratePayments(t *testing.T) {
	s, err := Generate(opts())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Payments) != 20_000 {
		t.Fatalf("платежей %d", len(s.Payments))
	}
	ids := map[string]bool{}
	amounts := make([]int64, len(s.Payments))
	for i, p := range s.Payments {
		if ids[p.ID] {
			t.Fatalf("повторный id %s", p.ID)
		}
		ids[p.ID] = true
		if p.At < 0 || p.At >= s.Header.Duration {
			t.Fatalf("%s: время %s вне сценария", p.ID, time.Duration(p.At))
		}
		if i > 0 && p.At < s.Payments[i-1].At {
			t.Fatalf("платежи не упорядочены по времени на %s", p.ID)
		}
		if p.Amount < minAmount || p.Amount > maxAmount {
			t.Fatalf("%s: сумма %d вне границ", p.ID, p.Amount)
		}
		amounts[i] = p.Amount
	}
	sort.Slice(amounts, func(i, j int) bool { return amounts[i] < amounts[j] })
	if med := float64(amounts[len(amounts)/2]); math.Abs(med-150_000)/150_000 > 0.05 {
		t.Fatalf("медиана суммы %.0f, ожидалось около 150000", med)
	}
}

func TestGenerateDiurnalLoad(t *testing.T) {
	s, _ := Generate(opts())
	var day, night int
	for _, p := range s.Payments {
		switch h := time.Duration(p.At).Hours(); {
		case h >= 12 && h < 18:
			day++
		case h < 6:
			night++
		}
	}
	if day < 2*night {
		t.Fatalf("днём %d платежей, ночью %d: ожидался перекос минимум вдвое", day, night)
	}
}

func TestGenerateKinds(t *testing.T) {
	base := opts()
	base.Kind = KindBaseline
	s, err := Generate(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range s.Header.Channels {
		if len(c.Drift) != 0 || len(c.Outages) != 0 {
			t.Fatalf("%s: в baseline есть события", c.ID)
		}
	}

	m, _ := Generate(opts())
	events := map[string]ChannelSpec{}
	for _, c := range m.Header.Channels {
		events[c.ID] = c
	}
	if len(events[ChFastExpensive].Outages) != 1 || len(events[ChBalanced].Drift) == 0 || len(events[ChCheapSlow].Drift) == 0 {
		t.Fatalf("в mixed не хватает событий: %+v", m.Header.Channels)
	}
	if len(events[ChNightOnly].Drift) != 0 {
		t.Fatal("у ночного канала не должно быть событий")
	}
}

func TestGeneratedChannelsAcceptedBySimulator(t *testing.T) {
	for _, kind := range []string{KindBaseline, KindMixed} {
		for seed := int64(1); seed <= 20; seed++ {
			o := opts()
			o.Payments, o.Kind, o.Seed = 10, kind, seed
			s, err := Generate(o)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range s.Header.Channels {
				if err := c.Validate(); err != nil {
					t.Fatalf("%s seed=%d: %v", kind, seed, err)
				}
				cfg := c.SimConfig(seed, s.Header.Start)
				if _, err := simulator.New(cfg, simulator.NewMemLedger(), clock.NewVirtual(s.Header.Start)); err != nil {
					t.Fatalf("%s seed=%d канал %s: %v", kind, seed, c.ID, err)
				}
			}
		}
	}
}

func TestGenerateOptionsValidation(t *testing.T) {
	bad := []Options{
		{Payments: 0, Duration: time.Hour},
		{Payments: 10, Duration: 0},
		{Payments: 10, Duration: time.Hour, Kind: "chaos"},
	}
	for i, o := range bad {
		if _, err := Generate(o); err == nil {
			t.Errorf("случай %d: ожидалась ошибка", i)
		}
	}
}

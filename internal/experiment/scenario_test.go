package experiment

import (
	"bytes"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/clock"
	"github.com/Cumparator/smart_payment_method/internal/simulator"
)

func sampleScenario() Scenario {
	return Scenario{
		Header: Header{
			Seed:     7,
			Start:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Duration: Dur(24 * time.Hour),
			Channels: []ChannelSpec{{
				ID: "A", Fixed: 5, Percent: 0.01, Latency: Dur(300 * time.Millisecond), Jitter: Dur(100 * time.Millisecond),
				DeclineRate: 0.02, ErrorRate: 0.03, TimeoutRate: 0.01, TimeoutChargeRate: 0.5,
				Drift:   []DriftSpec{{At: 0, ErrorRate: 0.03}, {At: Dur(time.Hour), ErrorRate: 0.2}},
				Outages: []OutageSpec{{From: Dur(2 * time.Hour), To: Dur(3 * time.Hour)}},
			}},
		},
		Payments: []Payment{
			{ID: "p000001", At: Dur(time.Minute), Amount: 150000, Currency: "RUB"},
			{ID: "p000002", At: Dur(90 * time.Second), Amount: 99900, Currency: "RUB"},
		},
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	want := sampleScenario()
	var buf bytes.Buffer
	if err := Write(&buf, want); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(buf.String(), "\n"); lines != 3 {
		t.Fatalf("строк %d, ожидалось 3 (header + 2 платежа)", lines)
	}
	got, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("после чтения:\n%+v\nожидалось:\n%+v", got, want)
	}
}

func TestReadErrors(t *testing.T) {
	cases := map[string]string{
		"нет header":      `{"type":"payment","id":"p1","at":"1s","amount":1,"currency":"RUB"}` + "\n",
		"два header":      `{"type":"header"}` + "\n" + `{"type":"header"}` + "\n",
		"неизвестный тип": `{"type":"header"}` + "\n" + `{"type":"x"}` + "\n",
		"битый json":      `{"type":"header"` + "\n",
		"плохая duration": `{"type":"header","duration":"сутки"}` + "\n",
	}
	for name, in := range cases {
		if _, err := Read(strings.NewReader(in)); err == nil {
			t.Errorf("%s: ожидалась ошибка", name)
		}
	}
}

func TestSimConfigDrift(t *testing.T) {
	spec := sampleScenario().Header.Channels[0]
	spec.Outages = nil
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := spec.SimConfig(42, start)

	if cfg.Seed != 42 || cfg.Tariff.Fixed != 5 || cfg.Latency != 300*time.Millisecond {
		t.Fatalf("конфиг %+v", cfg)
	}
	if len(cfg.Drift) != 2 || cfg.Drift[1].ErrorRate != 0.2 {
		t.Fatalf("дрейф: %+v", cfg.Drift)
	}
	if _, err := simulator.New(cfg, simulator.NewMemLedger(), clock.NewVirtual(start)); err != nil {
		t.Fatalf("имитатор не принял конфиг: %v", err)
	}
}

func TestSimConfigOutage(t *testing.T) {
	spec := sampleScenario().Header.Channels[0]
	spec.Drift = nil
	spec.Outages = []OutageSpec{{From: Dur(2 * time.Hour), To: Dur(3 * time.Hour)}}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	ch, err := simulator.New(spec.SimConfig(1, start), simulator.NewMemLedger(), clock.NewVirtual(start))
	if err != nil {
		t.Fatalf("имитатор не принял конфиг: %v", err)
	}
	down := 1 - spec.DeclineRate - spec.TimeoutRate
	for _, c := range []struct {
		at   time.Duration
		want float64
	}{
		{0, spec.ErrorRate},
		{2*time.Hour - time.Second, spec.ErrorRate},
		{2 * time.Hour, down},
		{3*time.Hour - time.Second, down},
		{3 * time.Hour, spec.ErrorRate}, // конец окна: канал работает
		{10 * time.Hour, spec.ErrorRate},
	} {
		if got := ch.ErrorRateAt(start.Add(c.at)); math.Abs(got-c.want) > 1e-6 {
			t.Errorf("через %s доля ошибок %.4f, ожидалась %.4f", c.at, got, c.want)
		}
	}
}

func TestChannelSpecValidate(t *testing.T) {
	ok := ChannelSpec{ID: "A", Outages: []OutageSpec{{From: Dur(time.Hour), To: Dur(2 * time.Hour)}, {From: Dur(2 * time.Hour), To: Dur(3 * time.Hour)}}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("корректный канал: %v", err)
	}
	bad := map[string]ChannelSpec{
		"пустое окно":    {ID: "A", Outages: []OutageSpec{{From: Dur(time.Hour), To: Dur(time.Hour)}}},
		"до начала":      {ID: "A", Outages: []OutageSpec{{From: Dur(-time.Second), To: Dur(time.Hour)}}},
		"пересечение":    {ID: "A", Outages: []OutageSpec{{From: 0, To: Dur(2 * time.Hour)}, {From: Dur(time.Hour), To: Dur(3 * time.Hour)}}},
		"аварии и дрейф": sampleScenario().Header.Channels[0],
	}
	for name, spec := range bad {
		if err := spec.Validate(); err == nil {
			t.Errorf("%s: ожидалась ошибка", name)
		}
	}
}

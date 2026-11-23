package experiment

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"time"
)

// Виды сценариев.
const (
	KindBaseline = "baseline" // каналы стабильны: ни дрейфа, ни аварий
	KindMixed    = "mixed"    // авария лучшего канала, деградация и улучшение других
)

const (
	minAmount = 1_000       // 10 ₽
	maxAmount = 100_000_000 // 1 млн ₽
)

// Options — параметры генератора.
type Options struct {
	Seed     int64
	Payments int
	Duration time.Duration
	Start    time.Time // по умолчанию 2026-01-01 UTC
	Kind     string    // по умолчанию KindMixed

	MedianAmount int64   // медиана суммы, копейки; по умолчанию 150 000
	Sigma        float64 // разброс логнормального распределения; по умолчанию 1
}

// Generate строит сценарий. Один seed даёт один и тот же сценарий.
func Generate(o Options) (Scenario, error) {
	if o.Payments <= 0 {
		return Scenario{}, fmt.Errorf("generate: число платежей должно быть положительным")
	}
	if o.Duration <= 0 {
		return Scenario{}, fmt.Errorf("generate: длительность должна быть положительной")
	}
	if o.Start.IsZero() {
		o.Start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if o.Kind == "" {
		o.Kind = KindMixed
	}
	if o.Kind != KindBaseline && o.Kind != KindMixed {
		return Scenario{}, fmt.Errorf("generate: неизвестный вид сценария %q", o.Kind)
	}
	if o.MedianAmount <= 0 {
		o.MedianAmount = 150_000
	}
	if o.Sigma <= 0 {
		o.Sigma = 1
	}

	rng := rand.New(rand.NewPCG(uint64(o.Seed), 0x5eed))

	channels := DefaultChannels()
	if o.Kind == KindMixed {
		addEvents(channels, o.Duration, rng)
	}

	return Scenario{
		Header:   Header{Seed: o.Seed, Start: o.Start, Duration: Dur(o.Duration), Channels: channels},
		Payments: genPayments(o, rng),
	}, nil
}

// intensity — относительная частота платежей в течение суток: пик около 15:00,
// провал около 03:00.
func intensity(offset time.Duration) float64 {
	h := math.Mod(offset.Hours(), 24)
	return 1 + 0.6*math.Sin(2*math.Pi*(h-9)/24)
}

const maxIntensity = 1.6

func genPayments(o Options, rng *rand.Rand) []Payment {
	offsets := make([]time.Duration, 0, o.Payments)
	for len(offsets) < o.Payments {
		t := time.Duration(rng.Float64() * float64(o.Duration)).Truncate(time.Millisecond)
		if rng.Float64()*maxIntensity < intensity(t) {
			offsets = append(offsets, t)
		}
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })

	mu := math.Log(float64(o.MedianAmount))
	out := make([]Payment, o.Payments)
	for i, at := range offsets {
		amount := int64(math.Round(math.Exp(mu + o.Sigma*rng.NormFloat64())))
		out[i] = Payment{
			ID:       fmt.Sprintf("p%06d", i+1),
			At:       Dur(at),
			Amount:   min(max(amount, minAmount), maxAmount),
			Currency: "RUB",
		}
	}
	return out
}

// addEvents добавляет в каналы события, на которых стратегии должны различаться:
//   - лучший канал падает на ~6% времени сценария;
//   - сбалансированный плавно деградирует, затем его чинят скачком;
//   - дешёвый медленный во второй половине суток становится надёжнее.
//
// Моменты сдвигаются случайно на ±3% длительности, чтобы сценарии разных seed не совпадали.
func addEvents(chs []ChannelSpec, dur time.Duration, rng *rand.Rand) {
	at := func(frac float64) Dur {
		return Dur(time.Duration(frac * float64(dur)).Truncate(time.Second))
	}
	shift := func() float64 { return (rng.Float64() - 0.5) * 0.06 }

	for i := range chs {
		c := &chs[i]
		switch c.ID {
		case ChFastExpensive:
			from := 0.35 + shift()
			c.Outages = []OutageSpec{{From: at(from), To: at(from + 0.06)}}
		case ChBalanced:
			base, bad := c.ErrorRate, 0.35
			start, peak, fix := 0.50+shift(), 0.70+shift(), 0.80+shift()
			c.Drift = []DriftSpec{
				{At: 0, ErrorRate: base},
				{At: at(start), ErrorRate: base},
				{At: at(peak), ErrorRate: bad},
				{At: at(fix), ErrorRate: bad},
				{At: at(fix), ErrorRate: base}, // скачок: канал починили
			}
		case ChCheapSlow:
			base, better := c.ErrorRate, 0.01
			from, to := 0.55+shift(), 0.75+shift()
			c.Drift = []DriftSpec{
				{At: 0, ErrorRate: base},
				{At: at(from), ErrorRate: base},
				{At: at(to), ErrorRate: better},
			}
		}
	}
}

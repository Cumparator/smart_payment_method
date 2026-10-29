package simulator

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/clock"
)

func TestErrorRateAt(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := Config{Start: start, Drift: []DriftPoint{
		{At: time.Hour, ErrorRate: 0.1},
		{At: 3 * time.Hour, ErrorRate: 0.5}, // плавная деградация
		{At: 5 * time.Hour, ErrorRate: 0.5},
		{At: 5 * time.Hour, ErrorRate: 0}, // скачок: канал починили
	}}
	cases := []struct {
		after time.Duration
		want  float64
	}{
		{0, 0.1},
		{time.Hour, 0.1},
		{2 * time.Hour, 0.3},
		{3 * time.Hour, 0.5},
		{5*time.Hour - time.Second, 0.5},
		{5 * time.Hour, 0},
		{24 * time.Hour, 0},
	}
	for _, c := range cases {
		if got := cfg.errorRateAt(start.Add(c.after)); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("через %s: %.3f, ожидали %.3f", c.after, got, c.want)
		}
	}
}

func TestDriftValidate(t *testing.T) {
	bad := []Config{
		{ID: "x", Drift: []DriftPoint{{At: time.Hour}, {At: time.Minute}}},
		{ID: "x", Drift: []DriftPoint{{ErrorRate: 1.5}}},
		{ID: "x", TimeoutRate: 0.5, Drift: []DriftPoint{{ErrorRate: 0.6}}},
	}
	for i, cfg := range bad {
		if _, err := New(cfg, NewMemLedger(), clock.Real{}); err == nil {
			t.Errorf("конфиг %d: ожидалась ошибка", i)
		}
	}
}

func TestDriftChangesOutcomes(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewVirtual(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ch, err := New(Config{ID: "sim", Seed: 1, Drift: []DriftPoint{
		{At: 0, ErrorRate: 0},
		{At: time.Hour, ErrorRate: 0.6},
	}}, NewMemLedger(), clk)
	if err != nil {
		t.Fatal(err)
	}

	errorShare := func(prefix string) float64 {
		const n = 5000
		errs := 0
		for i := range n {
			res, _ := ch.Charge(ctx, fmt.Sprintf("%s%d:1", prefix, i), 100, "RUB")
			if res.Outcome == channel.ChannelError {
				errs++
			}
		}
		return float64(errs) / n
	}

	if got := errorShare("a"); got != 0 {
		t.Fatalf("в начале ошибок %.3f, ожидали 0", got)
	}
	clk.Advance(30 * time.Minute)
	if got := errorShare("b"); math.Abs(got-0.3) > 0.03 {
		t.Fatalf("через 30 минут ошибок %.3f, ожидали ~0.3", got)
	}
	clk.Advance(time.Hour)
	if got := errorShare("c"); math.Abs(got-0.6) > 0.03 {
		t.Fatalf("через 1.5 часа ошибок %.3f, ожидали ~0.6", got)
	}
	if got := ch.ErrorRateAt(clk.Now()); got != 0.6 {
		t.Fatalf("ErrorRateAt %.3f", got)
	}
}

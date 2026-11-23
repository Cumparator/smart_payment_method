package experiment

import (
	"math"
	"time"
)

// Идентификаторы каналов из migrations/0002_channels.sql.
const (
	ChCheapSlow     = "ch_cheap_slow"
	ChFastExpensive = "ch_fast_expensive"
	ChBalanced      = "ch_balanced"
	ChNightOnly     = "ch_night_only"
)

// DefaultChannels — те же четыре канала, что засеяны в БД. Из config.success_rate
// и error_rate остаток 1 − success − error делится поровну между бизнес-отказами
// и таймаутами; половина таймаутов всё же списывает деньги. Если seed-данные в
// миграции поменяются, обновить и здесь.
func DefaultChannels() []ChannelSpec {
	mk := func(id string, fixed int64, percent float64, latency time.Duration, success, errRate float64) ChannelSpec {
		rest := math.Round((1-success-errRate)/2*1e4) / 1e4
		return ChannelSpec{
			ID: id, Fixed: fixed, Percent: percent,
			Latency: Dur(latency), Jitter: Dur(latency * 3 / 10),
			DeclineRate: rest, ErrorRate: errRate, TimeoutRate: rest, TimeoutChargeRate: 0.5,
		}
	}
	return []ChannelSpec{
		mk(ChCheapSlow, 3, 0.002, 1200*time.Millisecond, 0.86, 0.06),
		mk(ChFastExpensive, 12, 0.010, 250*time.Millisecond, 0.97, 0.01),
		mk(ChBalanced, 6, 0.005, 500*time.Millisecond, 0.92, 0.03),
		mk(ChNightOnly, 4, 0.003, 700*time.Millisecond, 0.78, 0.12),
	}
}

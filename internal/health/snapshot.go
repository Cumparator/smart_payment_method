package health

import (
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/strategy"
)

// latencyAlpha — вес нового замера в сглаженной задержке.
const latencyAlpha = 0.2

// Snapshot — состояние канала для стратегий в данный момент.
type Snapshot struct {
	Successes int     // технические успехи в окне
	Attempts  int     // технические исходы в окне
	LatencyS  float64 // типичная задержка ответа, секунды
	Load      float64 // запросы в полёте / лимит, 0..1; 0, если лимит не задан
}

// Register задаёт параметры канала: задержку до первых замеров и лимит
// одновременных запросов (0 — без лимита). Повторный вызов обновляет параметры.
func (r *Registry) Register(channelID string, defaultLatency time.Duration, maxInflight int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats(channelID)
	s.defaultLatencyS = defaultLatency.Seconds()
	s.maxInflight = maxInflight
}

// Record учитывает завершённую попытку: исход попадает в окно, а время ответа —
// в сглаженную задержку. Таймаут (UNKNOWN) задержку не меняет: ответа не было.
func (r *Registry) Record(channelID string, o channel.ChargeOutcome, latency time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordLocked(channelID, o, latency)
}

func (r *Registry) recordLocked(channelID string, o channel.ChargeOutcome, latency time.Duration) {
	r.observeLocked(channelID, o)
	if o == channel.Unknown {
		return
	}
	s := r.stats(channelID)
	l := latency.Seconds()
	if !s.hasLatency {
		s.latencyS, s.hasLatency = l, true
		return
	}
	s.latencyS += latencyAlpha * (l - s.latencyS)
}

// Begin отмечает запрос в полёте. Вызвав finish с ответом канала, вызывающий
// снимает отметку и записывает исход; повторный вызов finish ничего не делает.
func (r *Registry) Begin(channelID string) (finish func(o channel.ChargeOutcome, latency time.Duration)) {
	r.mu.Lock()
	r.stats(channelID).inflight++
	r.mu.Unlock()

	done := false
	return func(o channel.ChargeOutcome, latency time.Duration) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if done {
			return
		}
		done = true
		r.stats(channelID).inflight--
		r.recordLocked(channelID, o, latency)
	}
}

// Snapshot возвращает текущее состояние канала.
func (r *Registry) Snapshot(channelID string) Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats(channelID)
	snap := Snapshot{Successes: s.successes, Attempts: s.attempts, LatencyS: s.defaultLatencyS}
	if s.hasLatency {
		snap.LatencyS = s.latencyS
	}
	if s.maxInflight > 0 {
		snap.Load = min(1, float64(s.inflight)/float64(s.maxInflight))
	}
	return snap
}

// Candidate собирает кандидата для стратегий из снимка канала и его тарифа.
func (r *Registry) Candidate(channelID string, tariff channel.Tariff) strategy.Candidate {
	snap := r.Snapshot(channelID)
	return strategy.Candidate{
		ChannelID: channelID,
		Tariff:    tariff,
		Successes: snap.Successes,
		Attempts:  snap.Attempts,
		LatencyS:  snap.LatencyS,
		Load:      snap.Load,
	}
}

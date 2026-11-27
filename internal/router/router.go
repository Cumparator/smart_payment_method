package router

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Cumparator/smart_payment_method/internal/clock"
	"github.com/Cumparator/smart_payment_method/internal/health"
	"github.com/Cumparator/smart_payment_method/internal/payment"
	"github.com/Cumparator/smart_payment_method/internal/store"

	"github.com/Cumparator/smart_payment_method/internal/strategy"
)

// ErrNoChannel — не осталось каналов, в которые можно отправить платёж.
var ErrNoChannel = errors.New("router: нет доступных каналов")

// ErrChannelsBusy — подходящие каналы есть, но все перегружены. Это временно:
// платёж стоит вернуть в очередь, а не завершать неудачей.
var ErrChannelsBusy = errors.New("router: все подходящие каналы заняты")

// Decision — выбранный канал и объяснение выбора (пишется в routing_decisions).
type Decision struct {
	ChannelID   string
	Strategy    string
	Details     json.RawMessage
	Ranked      []strategy.Scored
	Excluded    []Exclusion
	LiveAttempt *payment.Attempt
}

// Source отдаёт снимок каналов, допущенных к выбору (включённых, с живым breaker).
type Source interface {
	Candidates(ctx context.Context) ([]strategy.Candidate, error)
}

// StaticSource — фиксированный список кандидатов для тестов и экспериментов.
type StaticSource []strategy.Candidate

func (s StaticSource) Candidates(context.Context) ([]strategy.Candidate, error) {
	return append([]strategy.Candidate(nil), s...), nil
}

// Health — состояние каналов, которое учитывает роутер. Реализует *health.Registry.
type Health interface {
	Snapshot(channelID string) health.Snapshot
	Unavailable(channelID string) health.Unavailable
}

var _ Health = (*health.Registry)(nil)

// Router выбирает канал в два этапа: фильтрация, затем оценка стратегией.
type Router struct {
	strategy strategy.Strategy
	source   Source
	repo     Repository
	clock    clock.Clock
	health   Health
}

func New(s strategy.Strategy, src Source) *Router {
	return &Router{strategy: s, source: src}
}

// WithHealth подключает состояние каналов: вероятность успеха, задержку и загрузку
// стратегии берут из него, а каналы с открытым предохранителем и перегруженные
// роутер пропускает. Без него статистика нейтральна и пропусков нет.
func (r *Router) WithHealth(h Health) *Router {
	r.health = h
	return r
}

type scoredInfo struct {
	ChannelID string  `json:"channel_id"`
	Score     float64 `json:"score"`
	PSuccess  float64 `json:"p_success"`
	Fee       float64 `json:"fee"`
}

type excludedInfo struct {
	ChannelID string `json:"channel_id"`
	Reason    string `json:"reason"`
}

type details struct {
	Amount     int64          `json:"amount"`
	Chosen     string         `json:"chosen"`
	Candidates []scoredInfo   `json:"candidates"`
	Excluded   []excludedInfo `json:"excluded,omitempty"`
}

// Choose исключает каналы из exclude (уже пробовавшиеся для этого платежа),
// оценивает остальных стратегией и берёт лучшего.
func (r *Router) Choose(ctx context.Context, amount int64, exclude []string) (Decision, error) {
	all, err := r.source.Candidates(ctx)
	if err != nil {
		return Decision{}, err
	}
	skip := make(map[string]bool, len(exclude))
	for _, id := range exclude {
		skip[id] = true
	}
	var cands []strategy.Candidate
	var excluded []excludedInfo
	busy := false
	for _, c := range all {
		if skip[c.ChannelID] {
			excluded = append(excluded, excludedInfo{ChannelID: c.ChannelID, Reason: "уже использовался для этого платежа"})
			continue
		}
		if why := r.unavailable(c.ChannelID); why != "" {
			excluded = append(excluded, excludedInfo{ChannelID: c.ChannelID, Reason: string(why)})
			busy = busy || why == health.Overloaded
			continue
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		if busy {
			return Decision{}, ErrChannelsBusy
		}
		return Decision{}, ErrNoChannel
	}

	return r.explain(amount, strategy.Rank(r.strategy, amount, cands), excluded)
}

func (r *Router) explain(amount int64, ranked []strategy.Scored, excluded []excludedInfo) (Decision, error) {
	d := details{Amount: amount, Chosen: ranked[0].ChannelID, Excluded: excluded}
	for _, s := range ranked {
		d.Candidates = append(d.Candidates, scoredInfo{
			ChannelID: s.ChannelID, Score: s.Score, PSuccess: s.PSuccess(), Fee: s.Tariff.Fee(amount),
		})
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return Decision{}, err
	}
	return Decision{ChannelID: ranked[0].ChannelID, Strategy: r.strategy.Name(), Details: raw}, nil
}

// ChoosePayment routes with the payment history already loaded by the worker.
func (r *Router) ChoosePayment(ctx context.Context, p payment.Payment, attempts []payment.Attempt) (Decision, error) {
	tried := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt.IsLive() {
			return Decision{}, store.ErrLiveAttemptExists
		}
		tried = append(tried, attempt.ChannelID)
	}
	if r.source != nil {
		return r.Choose(ctx, p.Amount, tried)
	}
	decision, err := r.selectChannels(ctx, p, attempts, Decision{Strategy: r.strategy.Name()})
	if err != nil {
		return Decision{}, err
	}
	if len(decision.Ranked) == 0 {
		for _, e := range decision.Excluded {
			if e.Reason == ReasonOverloaded {
				return Decision{}, ErrChannelsBusy
			}
		}
		return Decision{}, ErrNoChannel
	}
	excluded := make([]excludedInfo, 0, len(decision.Excluded))
	for _, entry := range decision.Excluded {
		excluded = append(excluded, excludedInfo{ChannelID: entry.ChannelID, Reason: string(entry.Reason)})
	}
	return r.explain(p.Amount, decision.Ranked, excluded)
}

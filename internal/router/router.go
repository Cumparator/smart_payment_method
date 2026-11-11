package router

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Cumparator/smart_payment_method/internal/strategy"
)

// ErrNoChannel — не осталось каналов, в которые можно отправить платёж.
var ErrNoChannel = errors.New("router: нет доступных каналов")

// Decision — выбранный канал и объяснение выбора (пишется в routing_decisions).
type Decision struct {
	ChannelID string
	Strategy  string
	Details   json.RawMessage
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

// Router выбирает канал в два этапа: фильтрация, затем оценка стратегией.
type Router struct {
	strategy strategy.Strategy
	source   Source
}

func New(s strategy.Strategy, src Source) *Router {
	return &Router{strategy: s, source: src}
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
	for _, c := range all {
		if skip[c.ChannelID] {
			excluded = append(excluded, excludedInfo{ChannelID: c.ChannelID, Reason: "уже использовался для этого платежа"})
			continue
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return Decision{}, ErrNoChannel
	}

	ranked := strategy.Rank(r.strategy, amount, cands)
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

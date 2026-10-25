// Package strategy — оценка допустимых каналов. Стратегии — чистые функции
// от снимка статистики, поэтому одинаково работают в живом режиме и в эксперименте.
package strategy

import (
	"sort"

	"github.com/Cumparator/smart_payment_method/internal/channel"
)

// Candidate — снимок канала, прошедшего фильтрацию маршрутизатора.
type Candidate struct {
	ChannelID string
	Tariff    channel.Tariff
	Successes int     // технические успехи в скользящем окне
	Attempts  int     // технические исходы в окне (DECLINED не считается)
	LatencyS  float64 // типичная задержка ответа, секунды
	Load      float64 // загрузка канала, 0..1
}

// PSuccess — вероятность успеха со сглаживанием Лапласа: (S + 1) / (N + 2).
func (c Candidate) PSuccess() float64 {
	return float64(c.Successes+1) / float64(c.Attempts+2)
}

type Scored struct {
	Candidate
	Score float64 // меньше — лучше
}

type Strategy interface {
	Name() string
	Score(amount int64, candidates []Candidate) []Scored
}

// Rank оценивает кандидатов и сортирует их по возрастанию оценки.
func Rank(s Strategy, amount int64, candidates []Candidate) []Scored {
	scored := s.Score(amount, candidates)
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score < scored[j].Score })
	return scored
}

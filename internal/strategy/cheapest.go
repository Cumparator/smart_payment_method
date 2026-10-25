package strategy

// Cheapest — Стратегия 1, контрольная группа: минимальная комиссия,
// надёжность и задержки не учитываются.
type Cheapest struct{}

func (Cheapest) Name() string { return "cheapest" }

func (Cheapest) Score(amount int64, candidates []Candidate) []Scored {
	out := make([]Scored, len(candidates))
	for i, c := range candidates {
		out[i] = Scored{Candidate: c, Score: c.Tariff.Fee(amount)}
	}
	return out
}

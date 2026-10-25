package strategy

// ExpectedCost — Стратегия 2:
// Цена = Комиссия + (1 − p_success)·Штраф + Задержка·ЦенаСекунды (+ Добавка за загрузку).
// Все денежные параметры — в копейках.
type ExpectedCost struct {
	FailurePenalty float64 // условная стоимость неудачной попытки
	SecondCost     float64 // стоимость секунды ожидания клиента
	LoadThreshold  float64 // порог загрузки, после которого начисляется добавка (0.7)
	LoadPenalty    float64 // добавка за каждую долю загрузки сверх порога
}

func (ExpectedCost) Name() string { return "expected_cost" }

func (e ExpectedCost) Score(amount int64, candidates []Candidate) []Scored {
	out := make([]Scored, len(candidates))
	for i, c := range candidates {
		score := c.Tariff.Fee(amount) +
			(1-c.PSuccess())*e.FailurePenalty +
			c.LatencyS*e.SecondCost
		if c.Load > e.LoadThreshold {
			score += (c.Load - e.LoadThreshold) * e.LoadPenalty
		}
		out[i] = Scored{Candidate: c, Score: score}
	}
	return out
}

// Package payment — доменная модель платежа и автомат состояний.
// Это единственный пакет, которому разрешено менять статус платежа.
package payment

import "fmt"

type State string

const (
	Received     State = "RECEIVED"
	Processing   State = "PROCESSING"
	Reconciling  State = "RECONCILING"
	Succeeded    State = "SUCCEEDED"
	Failed       State = "FAILED"
	ManualReview State = "MANUAL_REVIEW"
)

// transitions — допустимые переходы. Processing → Received нужен для повтора
// в другой канал после CHANNEL_ERROR (см. wiki, «Открытые вопросы», п. 1).
var transitions = map[State][]State{
	Received:    {Processing},
	Processing:  {Succeeded, Failed, Received, Reconciling},
	Reconciling: {Succeeded, Failed, Received, ManualReview},
}

func (s State) IsFinal() bool {
	return s == Succeeded || s == Failed || s == ManualReview
}

func CanTransition(from, to State) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// ValidateTransition проверяет переход до CAS-обновления в БД:
// UPDATE payments SET state = $to WHERE id = $id AND state = $from.
func ValidateTransition(from, to State) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("payment: недопустимый переход %s → %s", from, to)
	}
	return nil
}

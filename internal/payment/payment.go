package payment

import (
	"fmt"
	"time"
)

// Payment — платёж. Сумма хранится в копейках.
type Payment struct {
	ID             string
	IdempotencyKey string
	Amount         int64
	Currency       string
	State          State
	AttemptsCount  int
	NextRunAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type AttemptState string

const (
	AttemptStarted      AttemptState = "STARTED"
	AttemptApproved     AttemptState = "APPROVED"
	AttemptDeclined     AttemptState = "DECLINED"
	AttemptChannelError AttemptState = "CHANNEL_ERROR"
	AttemptUnknown      AttemptState = "UNKNOWN"
	AttemptCancelled    AttemptState = "CANCELLED"
)

// IsLive — попытка не завершена: канал ещё может списать деньги.
// UNKNOWN тоже «живая»: исход неизвестен, пока его не выяснит сверщик.
// Список состояний должен совпадать с частичным индексом attempts_one_live_idx.
func (s AttemptState) IsLive() bool {
	return s == AttemptStarted || s == AttemptUnknown
}

// attemptTransitions — допустимые переходы состояния попытки.
var attemptTransitions = map[AttemptState][]AttemptState{
	AttemptStarted: {AttemptApproved, AttemptDeclined, AttemptChannelError, AttemptUnknown, AttemptCancelled},
	AttemptUnknown: {AttemptApproved, AttemptDeclined, AttemptCancelled},
}

// ValidateAttemptTransition проверяет переход попытки до CAS-обновления в БД.
func ValidateAttemptTransition(from, to AttemptState) error {
	for _, s := range attemptTransitions[from] {
		if s == to {
			return nil
		}
	}
	return fmt.Errorf("payment: недопустимый переход попытки %s → %s", from, to)
}

// MaxAttempts — не более 3 попыток на платёж.
const MaxAttempts = 3

// Attempt — одна попытка провести платёж через канал.
type Attempt struct {
	PaymentID  string
	AttemptNo  int
	ChannelID  string
	State      AttemptState
	StartedAt  time.Time
	FinishedAt *time.Time
}

// IsLive — см. AttemptState.IsLive.
func (a Attempt) IsLive() bool { return a.State.IsLive() }

// ChannelKey — ключ идемпотентности попытки, передаваемый в канал.
func (a Attempt) ChannelKey() string {
	return fmt.Sprintf("%s:%d", a.PaymentID, a.AttemptNo)
}

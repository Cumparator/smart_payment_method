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

// ChannelKey — ключ идемпотентности попытки, передаваемый в канал.
func (a Attempt) ChannelKey() string {
	return fmt.Sprintf("%s:%d", a.PaymentID, a.AttemptNo)
}

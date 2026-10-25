// Package channel — единый контракт платёжного канала. Ничего не знает о БД
// и состояниях платежа, поэтому имитатор заменяется реальным провайдером.
package channel

import "context"

type ChargeOutcome string

const (
	Approved     ChargeOutcome = "APPROVED"      // деньги списаны → SUCCEEDED
	Declined     ChargeOutcome = "DECLINED"      // бизнес-отказ → FAILED
	ChannelError ChargeOutcome = "CHANNEL_ERROR" // техническая ошибка → можно в другой канал
	Unknown      ChargeOutcome = "UNKNOWN"       // таймаут/сеть → RECONCILING, переключаться нельзя
)

type StatusOutcome string

const (
	StatusApproved StatusOutcome = "APPROVED"
	StatusDeclined StatusOutcome = "DECLINED"
	StatusNotFound StatusOutcome = "NOT_FOUND"
)

type ChargeResult struct {
	Outcome ChargeOutcome
	Reason  string
}

type StatusResult struct {
	Outcome StatusOutcome
}

type CancelResult struct {
	Confirmed bool
}

// Tariff — комиссия канала: Fixed + Percent * сумма. Fixed в копейках.
type Tariff struct {
	Fixed   int64
	Percent float64
}

func (t Tariff) Fee(amount int64) float64 {
	return float64(t.Fixed) + t.Percent*float64(amount)
}

// PaymentChannel реализуют все каналы. key — ключ попытки payment_id:attempt_no.
type PaymentChannel interface {
	ID() string
	Charge(ctx context.Context, key string, amount int64, currency string) (ChargeResult, error)
	Status(ctx context.Context, key string) (StatusResult, error)
	Cancel(ctx context.Context, key string) (CancelResult, error)
}

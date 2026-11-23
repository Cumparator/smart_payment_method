package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/payment"
	"github.com/jackc/pgx/v5"
)

// ErrStateChanged — платёж уже не в том состоянии, из которого его переводили.
var ErrStateChanged = errors.New("store: состояние платежа изменилось")

// RoutingDecision — решение маршрутизатора для строки routing_decisions.
type RoutingDecision struct {
	Strategy string
	Details  json.RawMessage // кандидаты, причины исключения, оценки
}

func (r RoutingDecision) detailsOrEmpty() json.RawMessage {
	if len(r.Details) == 0 {
		return json.RawMessage("{}")
	}
	return r.Details
}

// Completion — итог попытки и связанный с ним переход платежа.
type Completion struct {
	PaymentID string
	AttemptNo int
	AttemptTo payment.AttemptState // из STARTED
	PaymentTo payment.State        // из PROCESSING
	NextRunAt time.Time            // нулевое значение — next_run_at не меняется
	Reason    string
}

func nextRunArg(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// moveState — CAS-переход платежа с записью в payment_events. Вызывается внутри транзакции.
func moveState(ctx context.Context, tx pgx.Tx, id string, from, to payment.State, nextRunAt time.Time, reason string) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE payments
		SET state = $1, next_run_at = COALESCE($4::timestamptz, next_run_at), updated_at = now()
		WHERE id = $2 AND state = $3`,
		string(to), id, string(from), nextRunArg(nextRunAt))
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO payment_events (payment_id, from_state, to_state, reason)
		VALUES ($1, $2, $3, $4)`, id, string(from), string(to), reason)
	return err == nil, err
}

// ClaimPayment забирает один готовый к обработке платёж: RECEIVED с next_run_at <= now
// переводится в PROCESSING. SKIP LOCKED позволяет нескольким воркерам брать
// разные платежи, не мешая друг другу. Второго результата false — очередь пуста.
func (d *DB) ClaimPayment(ctx context.Context, now time.Time) (payment.Payment, bool, error) {
	var out payment.Payment
	found := false
	err := d.Tx(ctx, func(tx pgx.Tx) error {
		var row PaymentRow
		err := tx.QueryRow(ctx, `
			SELECT id, idempotency_key, amount, currency, state, attempts_count, next_run_at, created_at, updated_at
			FROM payments
			WHERE state = 'RECEIVED' AND next_run_at <= $1
			ORDER BY next_run_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED`, now).Scan(
			&row.ID, &row.IdempotencyKey, &row.Amount, &row.Currency, &row.State,
			&row.AttemptsCount, &row.NextRunAt, &row.CreatedAt, &row.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		ok, err := moveState(ctx, tx, row.ID, payment.Received, payment.Processing, time.Time{}, "взят воркером")
		if err != nil {
			return err
		}
		if !ok {
			return ErrStateChanged
		}
		row.State = payment.Processing
		out = toPayment(row)
		found = true
		return nil
	})
	if err != nil {
		return payment.Payment{}, false, err
	}
	return out, found, nil
}

// TransitionStatusAt — как TransitionStatus, но дополнительно задаёт next_run_at
// (нулевое время — не менять). Нужен для отложенного повтора и сверки.
func (d *DB) TransitionStatusAt(ctx context.Context, id string, from, to payment.State, nextRunAt time.Time, reason string) (bool, error) {
	if err := payment.ValidateTransition(from, to); err != nil {
		return false, err
	}
	var ok bool
	err := d.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		ok, err = moveState(ctx, tx, id, from, to, nextRunAt, reason)
		return err
	})
	return ok && err == nil, err
}

// CompleteAttempt в одной транзакции фиксирует исход попытки (STARTED → AttemptTo)
// и переводит платёж (PROCESSING → PaymentTo). Результат либо сохранён целиком,
// либо нет: платёж не останется в PROCESSING при уже завершённой попытке.
// false — попытку успел изменить кто-то другой (например, уборщик зависших);
// платёж при этом не трогается.
func (d *DB) CompleteAttempt(ctx context.Context, c Completion) (bool, error) {
	if err := payment.ValidateAttemptTransition(payment.AttemptStarted, c.AttemptTo); err != nil {
		return false, err
	}
	if err := payment.ValidateTransition(payment.Processing, c.PaymentTo); err != nil {
		return false, err
	}
	done := false
	err := d.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE attempts
			SET state = $1,
			    finished_at = CASE WHEN $1::text IN ('STARTED', 'UNKNOWN') THEN NULL ELSE now() END
			WHERE payment_id = $2 AND attempt_no = $3 AND state = 'STARTED'`,
			string(c.AttemptTo), c.PaymentID, c.AttemptNo)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		ok, err := moveState(ctx, tx, c.PaymentID, payment.Processing, c.PaymentTo, c.NextRunAt, c.Reason)
		if err != nil {
			return err
		}
		if !ok {
			return ErrStateChanged // откатит и обновление попытки
		}
		done = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return done, nil
}

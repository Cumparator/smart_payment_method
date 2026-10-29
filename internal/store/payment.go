package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/Cumparator/smart_payment_method/internal/payment"
)

// PaymentRow — строка payments.
type PaymentRow struct {
	ID             string
	IdempotencyKey string
	Amount         int64
	Currency       string
	State          payment.State
	AttemptsCount  int
	NextRunAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func toPayment(r PaymentRow) payment.Payment {
	return payment.Payment{
		ID: r.ID, IdempotencyKey: r.IdempotencyKey, Amount: r.Amount,
		Currency: r.Currency, State: r.State, AttemptsCount: r.AttemptsCount,
		NextRunAt: r.NextRunAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// CreatePayment inserts payment; returns existing if idempotency key exists.
func (d *DB) CreatePayment(ctx context.Context, p payment.Payment) (payment.Payment, bool, error) {
	var row PaymentRow
	err := d.Pool.QueryRow(ctx, `
		INSERT INTO payments (id, idempotency_key, amount, currency, state, attempts_count, next_run_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id, idempotency_key, amount, currency, state, attempts_count, next_run_at, created_at, updated_at`,
		p.ID, p.IdempotencyKey, p.Amount, p.Currency, string(p.State), p.AttemptsCount, p.NextRunAt,
	).Scan(&row.ID, &row.IdempotencyKey, &row.Amount, &row.Currency, &row.State, &row.AttemptsCount, &row.NextRunAt, &row.CreatedAt, &row.UpdatedAt)
	if err == pgx.ErrNoRows {
		// conflict: load existing
		err2 := d.Pool.QueryRow(ctx, `
			SELECT id, idempotency_key, amount, currency, state, attempts_count, next_run_at, created_at, updated_at
			FROM payments WHERE idempotency_key=$1`, p.IdempotencyKey).Scan(
			&row.ID, &row.IdempotencyKey, &row.Amount, &row.Currency, &row.State, &row.AttemptsCount, &row.NextRunAt, &row.CreatedAt, &row.UpdatedAt)
		if err2 != nil {
			return payment.Payment{}, false, err2
		}
		return toPayment(row), true, nil
	}
	if err != nil {
		return payment.Payment{}, false, err
	}
	return toPayment(row), false, nil
}

// GetPayment loads payment by id.
func (d *DB) GetPayment(ctx context.Context, id string) (payment.Payment, error) {
	var row PaymentRow
	err := d.Pool.QueryRow(ctx, `
		SELECT id, idempotency_key, amount, currency, state, attempts_count, next_run_at, created_at, updated_at
		FROM payments WHERE id=$1`, id).Scan(
		&row.ID, &row.IdempotencyKey, &row.Amount, &row.Currency, &row.State, &row.AttemptsCount, &row.NextRunAt, &row.CreatedAt, &row.UpdatedAt)
	if err != nil {
		return payment.Payment{}, err
	}
	return toPayment(row), nil
}

// TransitionStatus updates state if old matches, appends event.
func (d *DB) TransitionStatus(ctx context.Context, id string, from, to payment.State, reason string) (bool, error) {
	var ok bool
	err := d.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE payments SET state=$1, updated_at=now() WHERE id=$2 AND state=$3`, string(to), id, string(from))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			ok = false
			return nil
		}
		ok = true
		_, err = tx.Exec(ctx, `INSERT INTO payment_events (payment_id, from_state, to_state, reason) VALUES ($1,$2,$3,$4)`, id, string(from), string(to), reason)
		return err
	})
	return ok, err
}

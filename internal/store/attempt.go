package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/payment"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrLiveAttemptExists — у платежа уже есть незавершённая (STARTED/UNKNOWN) попытка.
	ErrLiveAttemptExists = errors.New("store: у платежа уже есть незавершённая попытка")
	// ErrMaxAttempts — исчерпан лимит payment.MaxAttempts.
	ErrMaxAttempts = errors.New("store: исчерпан лимит попыток платежа")
	// ErrPaymentNotFound — платежа с таким id нет.
	ErrPaymentNotFound = errors.New("store: платёж не найден")
)

const (
	pgUniqueViolation = "23505"
	// Частичный уникальный индекс из migrations/0001_init.sql:
	// UNIQUE (payment_id) WHERE state IN ('STARTED', 'UNKNOWN').
	liveAttemptIndex = "attempts_one_live_idx"
)

const attemptColumns = `payment_id, attempt_no, channel_id, state, started_at, finished_at`

func scanAttempt(row pgx.Row) (payment.Attempt, error) {
	var a payment.Attempt
	var state string
	var finished *time.Time
	if err := row.Scan(&a.PaymentID, &a.AttemptNo, &a.ChannelID, &state, &a.StartedAt, &finished); err != nil {
		return payment.Attempt{}, err
	}
	a.State = payment.AttemptState(state)
	a.FinishedAt = finished
	return a, nil
}

// StartAttempt создаёт новую попытку в состоянии STARTED. Вызывается ДО обращения к каналу.
//
// В одной транзакции: атомарно занимает следующий номер попытки (и блокирует строку
// платежа от конкурентных стартов) и вставляет строку в attempts. Одну живую попытку
// на платёж гарантирует частичный уникальный индекс, а не проверка в коде, поэтому
// гонка двух воркеров невозможна: проигравший получит ErrLiveAttemptExists,
// а его транзакция (вместе с увеличением attempts_count) откатится.
func (d *DB) StartAttempt(ctx context.Context, paymentID, channelID string) (payment.Attempt, error) {
	var att payment.Attempt
	err := d.Tx(ctx, func(tx pgx.Tx) error {
		var no int
		err := tx.QueryRow(ctx, `
			UPDATE payments SET attempts_count = attempts_count + 1, updated_at = now()
			WHERE id = $1 AND attempts_count < $2
			RETURNING attempts_count`, paymentID, payment.MaxAttempts).Scan(&no)
		if errors.Is(err, pgx.ErrNoRows) {
			return classifyStartRefusal(ctx, tx, paymentID)
		}
		if err != nil {
			return err
		}

		a := payment.Attempt{PaymentID: paymentID, AttemptNo: no, ChannelID: channelID, State: payment.AttemptStarted}
		att, err = scanAttempt(tx.QueryRow(ctx, `
			INSERT INTO attempts (payment_id, attempt_no, channel_id, channel_key, state)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING `+attemptColumns,
			a.PaymentID, a.AttemptNo, a.ChannelID, a.ChannelKey(), string(a.State)))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == liveAttemptIndex {
			return ErrLiveAttemptExists
		}
		return err
	})
	if err != nil {
		return payment.Attempt{}, err
	}
	return att, nil
}

// classifyStartRefusal объясняет, почему не удалось занять номер попытки.
func classifyStartRefusal(ctx context.Context, tx pgx.Tx, paymentID string) error {
	var count int
	var live bool
	err := tx.QueryRow(ctx, `
		SELECT p.attempts_count,
		       EXISTS (SELECT 1 FROM attempts a
		               WHERE a.payment_id = p.id AND a.state IN ('STARTED', 'UNKNOWN'))
		FROM payments p WHERE p.id = $1`, paymentID).Scan(&count, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPaymentNotFound
	}
	if err != nil {
		return err
	}
	if live {
		return ErrLiveAttemptExists
	}
	return fmt.Errorf("%w: %d из %d", ErrMaxAttempts, count, payment.MaxAttempts)
}

// SetAttemptState переводит попытку from → to (CAS по текущему состоянию).
// Возвращает false, если попытка уже не в состоянии from (кто-то успел раньше).
// finished_at ставится только при переходе в завершённое состояние;
// UNKNOWN остаётся «живым», поэтому finished_at у него пустой.
func (d *DB) SetAttemptState(ctx context.Context, paymentID string, attemptNo int, from, to payment.AttemptState) (bool, error) {
	if err := payment.ValidateAttemptTransition(from, to); err != nil {
		return false, err
	}
	var finishedAt *time.Time
	if !to.IsLive() {
		now := time.Now()
		finishedAt = &now
	}
	tag, err := d.Pool.Exec(ctx, `
		UPDATE attempts SET state = $1, finished_at = $2
		WHERE payment_id = $3 AND attempt_no = $4 AND state = $5`,
		string(to), finishedAt, paymentID, attemptNo, string(from))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// GetLiveAttempt возвращает незавершённую попытку платежа, если она есть.
func (d *DB) GetLiveAttempt(ctx context.Context, paymentID string) (payment.Attempt, bool, error) {
	a, err := scanAttempt(d.Pool.QueryRow(ctx, `
		SELECT `+attemptColumns+` FROM attempts
		WHERE payment_id = $1 AND state IN ('STARTED', 'UNKNOWN')`, paymentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.Attempt{}, false, nil
	}
	if err != nil {
		return payment.Attempt{}, false, err
	}
	return a, true, nil
}

// ListAttempts возвращает все попытки платежа по возрастанию номера.
func (d *DB) ListAttempts(ctx context.Context, paymentID string) ([]payment.Attempt, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT `+attemptColumns+` FROM attempts WHERE payment_id = $1 ORDER BY attempt_no`, paymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []payment.Attempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

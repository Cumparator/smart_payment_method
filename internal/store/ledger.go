package store

import (
	"context"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/simulator"
	"github.com/jackc/pgx/v5"
)

// Ledger — книга списаний имитатора в таблице channel_ledger.
type Ledger struct {
	db *DB
}

var _ simulator.Ledger = (*Ledger)(nil)

func NewLedger(db *DB) *Ledger { return &Ledger{db: db} }

const ledgerCols = `channel_id, channel_key, amount, status, at`

func scanEntry(row pgx.Row) (simulator.Entry, error) {
	var e simulator.Entry
	err := row.Scan(&e.ChannelID, &e.Key, &e.Amount, &e.Status, &e.At)
	return e, err
}

// Get loads entry by channel and key.
func (l *Ledger) Get(ctx context.Context, channelID, key string) (simulator.Entry, bool, error) {
	e, err := scanEntry(l.db.Pool.QueryRow(ctx,
		`SELECT `+ledgerCols+` FROM channel_ledger WHERE channel_id=$1 AND channel_key=$2`, channelID, key))
	if err == pgx.ErrNoRows {
		return simulator.Entry{}, false, nil
	}
	if err != nil {
		return simulator.Entry{}, false, err
	}
	return e, true, nil
}

// Charge inserts CHARGED entry; returns existing if key already used.
func (l *Ledger) Charge(ctx context.Context, e simulator.Entry) (simulator.Entry, error) {
	got, err := scanEntry(l.db.Pool.QueryRow(ctx, `
		INSERT INTO channel_ledger (channel_id, channel_key, amount, status, at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (channel_id, channel_key) DO NOTHING
		RETURNING `+ledgerCols,
		e.ChannelID, e.Key, e.Amount, string(simulator.Charged), e.At))
	if err == pgx.ErrNoRows {
		got, _, err = l.Get(ctx, e.ChannelID, e.Key)
	}
	return got, err
}

// Cancel marks entry CANCELLED, creating an empty one if absent.
func (l *Ledger) Cancel(ctx context.Context, channelID, key string, at time.Time) (simulator.Entry, error) {
	got, err := scanEntry(l.db.Pool.QueryRow(ctx, `
		INSERT INTO channel_ledger (channel_id, channel_key, amount, status, at)
		VALUES ($1,$2,0,$3,$4)
		ON CONFLICT (channel_id, channel_key) DO UPDATE SET status=EXCLUDED.status, at=EXCLUDED.at
		WHERE channel_ledger.status <> EXCLUDED.status
		RETURNING `+ledgerCols,
		channelID, key, string(simulator.Cancelled), at))
	if err == pgx.ErrNoRows {
		got, _, err = l.Get(ctx, channelID, key)
	}
	return got, err
}

// Entries lists channel entries in insertion order.
func (l *Ledger) Entries(ctx context.Context, channelID string) ([]simulator.Entry, error) {
	rows, err := l.db.Pool.Query(ctx,
		`SELECT `+ledgerCols+` FROM channel_ledger WHERE channel_id=$1 ORDER BY at, channel_key`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []simulator.Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

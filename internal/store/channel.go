package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// ChannelRecord is the persisted configuration of a payment channel.
// Config is kept as JSON so domain packages can decode only the fields they own.
type ChannelRecord struct {
	ID           string
	Name         string
	IsEnabled    bool
	AvgLatencyMS int
	Config       json.RawMessage
}

// ListChannels returns all configured channels in a stable order.
func (d *DB) ListChannels(ctx context.Context) ([]ChannelRecord, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT id, name, is_enabled, avg_latency_ms, config
		FROM channels
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()

	channels := make([]ChannelRecord, 0)
	for rows.Next() {
		var ch ChannelRecord
		if err := rows.Scan(&ch.ID, &ch.Name, &ch.IsEnabled, &ch.AvgLatencyMS, &ch.Config); err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		channels = append(channels, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channels: %w", err)
	}
	return channels, nil
}

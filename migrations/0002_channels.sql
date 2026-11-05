-- +goose Up

-- Таблица платёжных каналов.
CREATE TABLE channels (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    is_enabled      BOOLEAN NOT NULL DEFAULT TRUE,
    avg_latency_ms  INT NOT NULL DEFAULT 0
                    CHECK (avg_latency_ms >= 0),
    config          JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);


-- Начальные данные: четыре платёжных канала.
INSERT INTO channels (
    id,
    name,
    is_enabled,
    avg_latency_ms,
    config
)
VALUES
(
    'ch_cheap_slow',
    'Дешёвый медленный',
    TRUE,
    1200,
    '{"success_rate":0.86,"error_rate":0.06,"rps_limit":50,"schedule":"24x7","tariff":{"fixed":3,"percent":0.002}}'
),
(
    'ch_fast_expensive',
    'Быстрый дорогой',
    TRUE,
    250,
    '{"success_rate":0.97,"error_rate":0.01,"rps_limit":200,"schedule":"24x7","tariff":{"fixed":12,"percent":0.010}}'
),
(
    'ch_balanced',
    'Сбалансированный',
    TRUE,
    500,
    '{"success_rate":0.92,"error_rate":0.03,"rps_limit":100,"schedule":"24x7","tariff":{"fixed":6,"percent":0.005}}'
),
(
    'ch_night_only',
    'Ночной (нестабильный днём)',
    TRUE,
    700,
    '{"success_rate":0.78,"error_rate":0.12,"rps_limit":80,"schedule":"22:00-06:00","tariff":{"fixed":4,"percent":0.003}}'
)
ON CONFLICT (id) DO NOTHING;


-- +goose Down

DROP TABLE channels;
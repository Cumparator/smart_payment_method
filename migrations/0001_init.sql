-- +goose Up

-- Таблица платежей.
CREATE TABLE payments (
    id               UUID PRIMARY KEY,
    idempotency_key  TEXT NOT NULL UNIQUE,
    amount           BIGINT NOT NULL CHECK (amount > 0),
    currency         CHAR(3) NOT NULL,
    state            TEXT NOT NULL,
    attempts_count   INT NOT NULL DEFAULT 0,
    next_run_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX payments_queue_idx
    ON payments (next_run_at)
    WHERE state IN ('RECEIVED', 'RECONCILING');


-- Попытки проведения платежей.
CREATE TABLE attempts (
    id           BIGSERIAL PRIMARY KEY,
    payment_id   UUID NOT NULL REFERENCES payments(id),
    attempt_no   INT NOT NULL,
    channel_id   TEXT NOT NULL,
    channel_key  TEXT NOT NULL UNIQUE,
    state        TEXT NOT NULL,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ,
    UNIQUE (payment_id, attempt_no)
);

CREATE UNIQUE INDEX attempts_one_live_idx
    ON attempts (payment_id)
    WHERE state IN ('STARTED', 'UNKNOWN');


-- История изменения состояния платежей.
CREATE TABLE payment_events (
    id          BIGSERIAL PRIMARY KEY,
    payment_id  UUID NOT NULL REFERENCES payments(id),
    from_state  TEXT,
    to_state    TEXT NOT NULL,
    reason      TEXT,
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);


-- Решения маршрутизации.
CREATE TABLE routing_decisions (
    id          BIGSERIAL PRIMARY KEY,
    payment_id  UUID NOT NULL REFERENCES payments(id),
    attempt_no  INT NOT NULL,
    strategy    TEXT NOT NULL,
    decision    JSONB NOT NULL,
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);


-- События платёжных каналов.
CREATE TABLE channel_events (
    id          BIGSERIAL PRIMARY KEY,
    channel_id  TEXT NOT NULL,
    from_state  TEXT NOT NULL,
    to_state    TEXT NOT NULL,
    details     JSONB,
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);


-- Книга списаний имитатора.
CREATE TABLE channel_ledger (
    channel_id   TEXT NOT NULL,
    channel_key  TEXT NOT NULL,
    amount       BIGINT NOT NULL,
    status       TEXT NOT NULL,
    at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, channel_key)
);


-- Результаты экспериментальных запусков.
CREATE TABLE experiment_runs (
    id          BIGSERIAL PRIMARY KEY,
    scenario    TEXT NOT NULL,
    strategy    TEXT NOT NULL,
    seed        BIGINT NOT NULL,
    metrics     JSONB NOT NULL,
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);


-- +goose Down

DROP TABLE experiment_runs;
DROP TABLE channel_ledger;
DROP TABLE channel_events;
DROP TABLE routing_decisions;
DROP TABLE payment_events;
DROP TABLE attempts;
DROP TABLE payments;
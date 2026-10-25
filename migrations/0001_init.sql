-- Стартовая схема. Описание таблиц — wiki, «Схема базы данных PostgreSQL».

CREATE TABLE payments (
    id               UUID PRIMARY KEY,
    idempotency_key  TEXT NOT NULL UNIQUE,
    amount           BIGINT NOT NULL CHECK (amount > 0),     -- копейки
    currency         CHAR(3) NOT NULL,
    state            TEXT NOT NULL,
    attempts_count   INT NOT NULL DEFAULT 0,
    next_run_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX payments_queue_idx ON payments (next_run_at) WHERE state IN ('RECEIVED', 'RECONCILING');

CREATE TABLE attempts (
    id           BIGSERIAL PRIMARY KEY,
    payment_id   UUID NOT NULL REFERENCES payments(id),
    attempt_no   INT  NOT NULL,
    channel_id   TEXT NOT NULL,
    channel_key  TEXT NOT NULL UNIQUE,                       -- payment_id:attempt_no
    state        TEXT NOT NULL,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ,
    UNIQUE (payment_id, attempt_no)
);
CREATE UNIQUE INDEX attempts_one_live_idx ON attempts (payment_id) WHERE state IN ('STARTED', 'UNKNOWN');

CREATE TABLE payment_events (                                -- append-only
    id          BIGSERIAL PRIMARY KEY,
    payment_id  UUID NOT NULL REFERENCES payments(id),
    from_state  TEXT,
    to_state    TEXT NOT NULL,
    reason      TEXT,
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE routing_decisions (
    id          BIGSERIAL PRIMARY KEY,
    payment_id  UUID NOT NULL REFERENCES payments(id),
    attempt_no  INT  NOT NULL,
    strategy    TEXT NOT NULL,
    decision    JSONB NOT NULL,                              -- кандидаты, причины исключения, оценки, снимок статистики
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE channel_events (
    id          BIGSERIAL PRIMARY KEY,
    channel_id  TEXT NOT NULL,
    from_state  TEXT NOT NULL,                               -- CLOSED / OPEN / HALF_OPEN
    to_state    TEXT NOT NULL,
    details     JSONB,
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE channel_ledger (                                -- «книга» имитатора
    channel_id   TEXT NOT NULL,
    channel_key  TEXT NOT NULL,
    amount       BIGINT NOT NULL,
    status       TEXT NOT NULL,                              -- CHARGED / CANCELLED
    at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, channel_key)                    -- повтор с тем же ключом не спишет дважды
);

CREATE TABLE experiment_runs (
    id          BIGSERIAL PRIMARY KEY,
    scenario    TEXT NOT NULL,
    strategy    TEXT NOT NULL,
    seed        BIGINT NOT NULL,
    metrics     JSONB NOT NULL,
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Client refresh requests; also the work queue of the background updater.
CREATE TABLE quote_updates (
    id              UUID PRIMARY KEY,
    pair            TEXT NOT NULL CHECK (pair ~ '^[A-Z]{3}/[A-Z]{3}$'),
    status          TEXT NOT NULL CHECK (status IN ('pending', 'completed', 'failed')),
    price           NUMERIC(20, 6),
    -- Day the provider's rate applies to (ECB publishes once per business day).
    source_date     DATE,
    error           TEXT,
    -- NULL when the request had no key; NULLs never conflict.
    idempotency_key UUID UNIQUE,
    attempts        INT NOT NULL DEFAULT 0,
    locked_until    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ
);

-- Only pending rows, so the queue index stays small however long the history grows.
CREATE INDEX quote_updates_pending_idx ON quote_updates (created_at) WHERE status = 'pending';

-- Latest known price per pair.
CREATE TABLE quotes (
    pair        TEXT PRIMARY KEY CHECK (pair ~ '^[A-Z]{3}/[A-Z]{3}$'),
    price       NUMERIC(20, 6) NOT NULL,
    source_date DATE,
    updated_at  TIMESTAMPTZ NOT NULL
);

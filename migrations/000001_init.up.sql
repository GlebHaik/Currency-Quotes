-- Every client refresh request; also the work queue for the background worker.
CREATE TABLE quote_updates (
    id              UUID PRIMARY KEY,
    pair            TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('pending', 'completed', 'failed')),
    price           NUMERIC(20, 6),
    -- Date the provider's rate applies to (ECB publishes once per business day).
    source_date     DATE,
    error           TEXT,
    -- Client's Idempotency-Key; NULL when the request had none (NULLs never conflict).
    idempotency_key UUID UNIQUE,
    attempts        INT NOT NULL DEFAULT 0,
    locked_until    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ
);

CREATE INDEX quote_updates_pending_idx ON quote_updates (created_at) WHERE status = 'pending';

-- Latest known price per pair.
CREATE TABLE quotes (
    pair        TEXT PRIMARY KEY,
    price       NUMERIC(20, 6) NOT NULL,
    source_date DATE,
    updated_at  TIMESTAMPTZ NOT NULL
);

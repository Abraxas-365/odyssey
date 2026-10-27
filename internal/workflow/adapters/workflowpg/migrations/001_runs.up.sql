-- Durable workflow runs. River (schema "river") moves them: every job names
-- a run and the revision it was scheduled for, so a job that lost a race (a
-- signal, a cancel, a retry already done) finds a newer revision and stops.
CREATE TABLE workflow.runs (
    id              uuid        PRIMARY KEY,
    organization_id uuid        NOT NULL,
    workflow        text        NOT NULL,
    subject         text        NOT NULL DEFAULT '',
    status          text        NOT NULL CHECK (status IN ('running', 'waiting', 'succeeded', 'failed', 'cancelled')),
    rev             bigint      NOT NULL DEFAULT 1,        -- bumped on every transition
    step            int         NOT NULL DEFAULT 0,
    step_name       text        NOT NULL,
    attempt         int         NOT NULL DEFAULT 0,        -- failed attempts of the current step
    step_started_at timestamptz NOT NULL DEFAULT now(),
    input           jsonb       NOT NULL,
    outputs         jsonb       NOT NULL DEFAULT '{}',
    error           text        NOT NULL DEFAULT '',
    waiting_for     text        NOT NULL DEFAULT '',
    wait_until      timestamptz,
    started_by      uuid,
    correlation_id  text        NOT NULL DEFAULT '',
    idempotency_key text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz,
    CHECK ((status IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL))
);
CREATE UNIQUE INDEX runs_idempotency_uq ON workflow.runs (organization_id, workflow, idempotency_key)
    WHERE idempotency_key <> '';
CREATE INDEX runs_subject_idx ON workflow.runs (organization_id, subject, created_at DESC);
CREATE INDEX runs_active_idx ON workflow.runs (status) WHERE finished_at IS NULL;

-- Signals sent to a run; kept until the run waits for them (early signals
-- are not lost).
CREATE TABLE workflow.signals (
    id          bigserial   PRIMARY KEY,
    run_id      uuid        NOT NULL REFERENCES workflow.runs (id) ON DELETE CASCADE,
    name        text        NOT NULL,
    payload     jsonb       NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    consumed_at timestamptz
);
CREATE INDEX signals_pending_idx ON workflow.signals (run_id, name, id) WHERE consumed_at IS NULL;

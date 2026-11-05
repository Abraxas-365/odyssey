-- Approval requests: an action IT's rules hold until someone with the right
-- permission decides (docs/approvals.md). The payload is the governing
-- domain's own description of the action, as it re-runs; the requester's
-- permissions are the snapshot taken when they asked (the approved action
-- re-runs as them, with exactly that).
CREATE TABLE approval.requests (
    id                    uuid        PRIMARY KEY,
    organization_id       uuid        NOT NULL,
    action                text        NOT NULL,
    project_id            uuid        NOT NULL,
    environment_id        uuid        NOT NULL,
    summary               text        NOT NULL,
    payload               jsonb       NOT NULL DEFAULT '{}',
    requested_by          uuid,
    requested_system      text        NOT NULL DEFAULT '',
    requester_permissions text[]      NOT NULL DEFAULT '{}',
    reason                text        NOT NULL DEFAULT '',
    rules                 jsonb       NOT NULL DEFAULT '[]',
    approvers             text[]      NOT NULL,
    status                text        NOT NULL DEFAULT 'pending'
                                      CHECK (status IN ('pending', 'approved', 'rejected', 'expired', 'cancelled')),
    decided_by            uuid,
    decided_at            timestamptz,
    decision_note         text        NOT NULL DEFAULT '',
    expires_at            timestamptz NOT NULL,
    consumed_at           timestamptz,
    execution_error       text        NOT NULL DEFAULT '',
    run_id                text        NOT NULL DEFAULT '',
    run_step              text        NOT NULL DEFAULT '',
    key                   text        NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT now(),
    CHECK ((status = 'pending') = (decided_at IS NULL)),
    CHECK (consumed_at IS NULL OR status = 'approved'),
    CHECK ((run_id = '') = (run_step = ''))
);

-- One open request per held thing: a workflow step holds its request for
-- good (asking twice returns it, whatever became of it); a direct action
-- only while it is pending (the same person may ask again afterwards).
CREATE UNIQUE INDEX requests_key_uq ON approval.requests (organization_id, key)
    WHERE status = 'pending' OR run_id <> '';
CREATE INDEX requests_org_idx ON approval.requests (organization_id, created_at DESC, id);
CREATE INDEX requests_run_idx ON approval.requests (organization_id, run_id) WHERE run_id <> '';
CREATE INDEX requests_due_idx ON approval.requests (expires_at) WHERE status = 'pending';

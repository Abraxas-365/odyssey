-- Audit trail: one row per domain event, hash-chained per organization
-- (hash = sha256(prev_hash || entry)), so deleting or editing a row breaks
-- verification. The application only INSERTs; heads track each chain's tip
-- and serialize writers of one organization.
CREATE TABLE audit.entries (
    id              uuid        PRIMARY KEY,      -- the event ID: re-delivery is a no-op
    organization_id uuid        NOT NULL,
    seq             bigint      NOT NULL CHECK (seq > 0),
    type            text        NOT NULL,
    version         int         NOT NULL,
    actor_id        uuid,
    client          text        NOT NULL DEFAULT '',
    subject         text        NOT NULL,
    summary         text        NOT NULL,
    payload         jsonb       NOT NULL,
    correlation_id  text        NOT NULL,
    causation_id    uuid,
    occurred_at     timestamptz NOT NULL,
    recorded_at     timestamptz NOT NULL,
    prev_hash       text        NOT NULL,
    hash            text        NOT NULL,
    UNIQUE (organization_id, seq)
);
CREATE INDEX entries_occurred_idx ON audit.entries (organization_id, occurred_at DESC, seq DESC);
CREATE INDEX entries_subject_idx ON audit.entries (organization_id, subject, seq DESC);
CREATE INDEX entries_correlation_idx ON audit.entries (organization_id, correlation_id);

CREATE TABLE audit.heads (
    organization_id uuid   PRIMARY KEY,
    seq             bigint NOT NULL,
    hash            text   NOT NULL
);

-- The trail is append-only even for code that forgets: updates and deletes
-- fail. (A DBA can still drop the trigger; the hash chain catches that.)
CREATE FUNCTION audit.reject_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit entries are append-only';
END $$;
CREATE TRIGGER entries_append_only BEFORE UPDATE OR DELETE ON audit.entries
    FOR EACH ROW EXECUTE FUNCTION audit.reject_change();
CREATE TRIGGER entries_no_truncate BEFORE TRUNCATE ON audit.entries
    FOR EACH STATEMENT EXECUTE FUNCTION audit.reject_change();

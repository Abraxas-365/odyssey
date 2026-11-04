-- Upload tickets (phase 1d-2): a one-use, 10-minute permission to publish a
-- folder to one environment with one configuration. The token itself is
-- never stored, only its SHA-256; used_at flips once (the consuming UPDATE is
-- the single-use guarantee) and deployment_id records what the upload
-- started. Expired tickets are swept by the next ticket their organization
-- issues.
CREATE TABLE release.upload_tickets (
    id              uuid PRIMARY KEY,
    organization_id uuid        NOT NULL,
    project_id      uuid        NOT NULL,
    environment_id  uuid        NOT NULL,
    token_hash      bytea       NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    name            text        NOT NULL DEFAULT '',
    config          jsonb       NOT NULL,
    client          text        NOT NULL DEFAULT '',
    requested_by    uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    used_at         timestamptz,
    deployment_id   uuid,
    CHECK (deployment_id IS NULL OR used_at IS NOT NULL)
);
CREATE INDEX upload_tickets_org_expiry_idx ON release.upload_tickets (organization_id, expires_at);

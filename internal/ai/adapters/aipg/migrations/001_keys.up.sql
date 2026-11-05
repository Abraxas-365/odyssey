-- Keys the platform made at the AI gateway for an environment's app (2d-ai).
-- The row is the decision and the handle to revoke the key; the key itself is
-- never stored (it travels sealed to the app's cluster). A key moves
-- creating → active → revoking and its row is dropped once the gateway
-- revoked it. name is what the key is called at the gateway
-- (odicea-<environment>-<id>), so a key can be found again without an id.
CREATE TABLE ai.keys (
    id              uuid PRIMARY KEY,
    organization_id uuid        NOT NULL,
    project_id      uuid        NOT NULL,
    environment_id  uuid        NOT NULL,
    provider_key_id text        CHECK (provider_key_id IS NULL OR provider_key_id <> ''),
    name            text        NOT NULL CHECK (name <> ''),
    status          text        NOT NULL CHECK (status IN ('creating', 'active', 'revoking')),
    created_by      uuid        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);
-- One key is in force per environment; a rotation's new key is creating
-- until it takes over, and the old one is revoking.
CREATE UNIQUE INDEX keys_one_active_idx ON ai.keys (environment_id) WHERE status = 'active';
CREATE UNIQUE INDEX keys_name_idx ON ai.keys (name);
CREATE INDEX keys_environment_idx ON ai.keys (organization_id, environment_id, created_at);

-- Environment settings (phase 1e, "Quién puede abrirla"): how an environment
-- is configured, as an append-only history. A change is a new row with the
-- next revision; the latest one is in force. An environment with no row uses
-- the defaults (access team). The deploy workflow's apply step reads the
-- latest revision and records it on the deployment it applies.
CREATE TABLE release.environment_settings (
    environment_id  uuid        NOT NULL,
    organization_id uuid        NOT NULL,
    revision        bigint      NOT NULL CHECK (revision > 0),
    access_mode     text        NOT NULL CHECK (access_mode IN ('team', 'organization', 'public')),
    created_by      uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (environment_id, revision)
);
CREATE INDEX environment_settings_org_idx ON release.environment_settings (organization_id, environment_id);

-- The settings revision a deployment applied (null until its apply step ran,
-- and for deployments made before settings existed).
ALTER TABLE release.deployments ADD COLUMN settings_revision bigint;

-- A deployment of the current release that only applies new settings.
ALTER TABLE release.deployments DROP CONSTRAINT deployments_reason_check;
ALTER TABLE release.deployments ADD CONSTRAINT deployments_reason_check
    CHECK (reason IN ('deploy', 'rollback', 'auto_rollback', 'config'));

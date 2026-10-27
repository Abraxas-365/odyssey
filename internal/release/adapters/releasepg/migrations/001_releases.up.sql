-- Releases: an image pinned by digest plus the configuration it runs with.
-- Immutable: a rollback deploys an older release again.
CREATE TABLE release.releases (
    id              uuid PRIMARY KEY,
    organization_id uuid        NOT NULL,
    project_id      uuid        NOT NULL,
    image           text        NOT NULL CHECK (image ~ '@sha256:[a-f0-9]{64}$'),
    config          jsonb       NOT NULL,
    created_by      uuid,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX releases_project_idx ON release.releases (organization_id, project_id, created_at DESC);

-- What each environment serves and what is changing it. One row per
-- environment, created by its first deployment; locking it serialises the
-- environment's deploys. current_release_id changes only in the deploy
-- workflow's promote step.
CREATE TABLE release.environment_states (
    environment_id        uuid PRIMARY KEY,
    organization_id       uuid        NOT NULL,
    project_id            uuid        NOT NULL,
    app                   text        NOT NULL,
    revision              bigint      NOT NULL DEFAULT 0,
    current_release_id    uuid REFERENCES release.releases (id),
    current_deployment_id uuid,
    active_deployment_id  uuid,
    url                   text        NOT NULL DEFAULT '',
    updated_at            timestamptz NOT NULL DEFAULT now()
);

-- Deployments: one attempt each to make a release the one an environment
-- serves. A finished deployment never changes.
CREATE TABLE release.deployments (
    id                  uuid PRIMARY KEY,
    organization_id     uuid        NOT NULL,
    project_id          uuid        NOT NULL,
    environment_id      uuid        NOT NULL REFERENCES release.environment_states (environment_id),
    release_id          uuid        NOT NULL REFERENCES release.releases (id),
    image               text        NOT NULL,
    status              text        NOT NULL CHECK (status IN ('pending', 'applying', 'verifying', 'ready', 'failed', 'cancelled')),
    reason              text        NOT NULL CHECK (reason IN ('deploy', 'rollback', 'auto_rollback')),
    step                text        NOT NULL,
    revision            bigint      NOT NULL,
    previous_release_id uuid REFERENCES release.releases (id),
    rollback_of         uuid REFERENCES release.deployments (id),
    error_code          text,
    error_message       text,
    run_id              uuid        NOT NULL,
    requested_by        uuid,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    finished_at         timestamptz,
    UNIQUE (environment_id, revision),
    CHECK ((status IN ('ready', 'failed', 'cancelled')) = (finished_at IS NOT NULL)),
    CHECK ((status = 'failed') = (error_code IS NOT NULL))
);
CREATE INDEX deployments_environment_idx ON release.deployments (organization_id, environment_id, created_at DESC, id DESC);

-- At most one unfinished deployment per environment.
CREATE UNIQUE INDEX deployments_one_active_uq ON release.deployments (environment_id)
    WHERE status IN ('pending', 'applying', 'verifying');

ALTER TABLE release.environment_states
    ADD FOREIGN KEY (current_deployment_id) REFERENCES release.deployments (id) DEFERRABLE INITIALLY DEFERRED,
    ADD FOREIGN KEY (active_deployment_id) REFERENCES release.deployments (id) DEFERRABLE INITIALLY DEFERRED;

-- Clusters of an organization. A cluster is created pending with a one-time
-- enrolment token (only its hash is stored); its agent trades the token for
-- a client certificate and from then on it is active, until revoked.
CREATE TABLE fleet.clusters (
    id                    uuid PRIMARY KEY,
    organization_id       uuid        NOT NULL,
    name                  text        NOT NULL,
    status                text        NOT NULL CHECK (status IN ('pending', 'active', 'revoked')),
    token_hash            bytea,
    token_expires_at      timestamptz,
    -- certificate_serial is the certificate the agent holds now;
    -- previous_serial the one it renewed from (accepted until it expires,
    -- an agent mid-renewal may still present it). Re-enrolling clears it.
    certificate_serial    text        NOT NULL DEFAULT '',
    certificate_expires_at timestamptz,
    previous_serial       text        NOT NULL DEFAULT '',
    previous_expires_at   timestamptz,
    -- The tunnel: session_id is the latest tunnel opened (a newer one
    -- supersedes it), last_seen_at its heartbeat.
    session_id            uuid,
    agent                 jsonb       NOT NULL DEFAULT '{}',
    last_seen_at          timestamptz,
    created_by            uuid        NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT now(),
    enrolled_at           timestamptz,
    revoked_at            timestamptz,
    revoked_by            uuid,
    CHECK ((token_hash IS NULL) = (token_expires_at IS NULL))
);
CREATE UNIQUE INDEX clusters_name_idx ON fleet.clusters (organization_id, lower(name)) WHERE status <> 'revoked';
CREATE INDEX clusters_org_idx ON fleet.clusters (organization_id, created_at);

-- Which cluster each environment runs on, fixed by its first app.
CREATE TABLE fleet.placements (
    environment_id  uuid PRIMARY KEY,
    organization_id uuid        NOT NULL,
    project_id      uuid        NOT NULL,
    cluster_id      uuid        NOT NULL REFERENCES fleet.clusters (id),
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX placements_cluster_idx ON fleet.placements (organization_id, cluster_id);

-- The desired state of every app, by environment and name. Its revision
-- only grows (an older deployment never wins late).
CREATE TABLE fleet.desired_apps (
    environment_id  uuid        NOT NULL REFERENCES fleet.placements (environment_id),
    name            text        NOT NULL,
    organization_id uuid        NOT NULL,
    project_id      uuid        NOT NULL,
    cluster_id      uuid        NOT NULL REFERENCES fleet.clusters (id),
    revision        bigint      NOT NULL CHECK (revision > 0),
    spec            jsonb       NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (environment_id, name)
);
CREATE INDEX desired_apps_cluster_idx ON fleet.desired_apps (organization_id, cluster_id);

-- What the agents last reported of each app (observations, not decisions).
CREATE TABLE fleet.app_status (
    environment_id    uuid        NOT NULL,
    name              text        NOT NULL,
    organization_id   uuid        NOT NULL,
    cluster_id        uuid        NOT NULL REFERENCES fleet.clusters (id),
    observed_revision bigint      NOT NULL,
    phase             text        NOT NULL,
    reason            text        NOT NULL DEFAULT '',
    message           text        NOT NULL DEFAULT '',
    ready_replicas    integer     NOT NULL DEFAULT 0,
    url               text        NOT NULL DEFAULT '',
    observed_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (environment_id, name),
    FOREIGN KEY (environment_id, name) REFERENCES fleet.desired_apps (environment_id, name)
);

-- Builds sent to clusters (phase 1b-2). The build domain decides them; the
-- fleet places them on the environment's cluster, carries them through the
-- tunnel and keeps what the agent reports. Tenant keys as in 002.

CREATE TABLE fleet.desired_builds (
    id               uuid        PRIMARY KEY,
    organization_id  uuid        NOT NULL,
    project_id       uuid        NOT NULL,
    environment_id   uuid        NOT NULL,
    cluster_id       uuid        NOT NULL,
    source_key       text        NOT NULL,
    source_sha256    text        NOT NULL CHECK (source_sha256 ~ '^[a-f0-9]{64}$'),
    source_size      bigint      NOT NULL CHECK (source_size > 0),
    image_repository text        NOT NULL,
    cache_ref        text        NOT NULL,
    timeout_seconds  integer     NOT NULL CHECK (timeout_seconds > 0),
    -- cancelled: no longer wanted (the cluster is told to drop it)
    cancelled        boolean     NOT NULL DEFAULT false,
    -- final: the agent reported it succeeded or failed (never sent again)
    final            boolean     NOT NULL DEFAULT false,
    -- the stored log: its size, whether lines were dropped beyond the cap,
    -- and whether it was archived elsewhere (later lines are not stored)
    log_bytes        bigint      NOT NULL DEFAULT 0,
    log_truncated    boolean     NOT NULL DEFAULT false,
    log_forgotten    boolean     NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, cluster_id) REFERENCES fleet.clusters (organization_id, id),
    UNIQUE (organization_id, id)
);
CREATE INDEX desired_builds_cluster_idx ON fleet.desired_builds (organization_id, cluster_id, updated_at);

-- What the agents last reported of each build (observations).
CREATE TABLE fleet.build_status (
    build_id        uuid        PRIMARY KEY,
    organization_id uuid        NOT NULL,
    phase           text        NOT NULL CHECK (phase IN ('pending', 'running', 'succeeded', 'failed')),
    image_digest    text        NOT NULL DEFAULT '',
    reason          text        NOT NULL DEFAULT '',
    message         text        NOT NULL DEFAULT '',
    observed_at     timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, build_id) REFERENCES fleet.desired_builds (organization_id, id) ON DELETE CASCADE
);

-- Live build logs, until the build domain archives them. seq comes from the
-- agent and is stable across resends: a line sent twice is stored once.
CREATE TABLE fleet.build_logs (
    build_id        uuid   NOT NULL,
    seq             bigint NOT NULL CHECK (seq > 0),
    organization_id uuid   NOT NULL,
    stream          text   NOT NULL,
    text            text   NOT NULL,
    PRIMARY KEY (build_id, seq),
    FOREIGN KEY (organization_id, build_id) REFERENCES fleet.desired_builds (organization_id, id) ON DELETE CASCADE
);

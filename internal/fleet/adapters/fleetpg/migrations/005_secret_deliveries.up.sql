-- Managed secret values on their way to a cluster (2a). The value was sealed
-- in a person's browser to the cluster's public key (pkg/secretbox): the
-- control plane cannot open it. A row lives until the agent acknowledges it
-- (stored or not) or SecretDeliveryTTL passes; only the newest version of a
-- secret waits. Tenant keys as in 002.
CREATE TABLE fleet.secret_deliveries (
    id              uuid        PRIMARY KEY,
    organization_id uuid        NOT NULL,
    project_id      uuid        NOT NULL,
    environment_id  uuid        NOT NULL,
    cluster_id      uuid        NOT NULL,
    app             text        NOT NULL,
    name            text        NOT NULL CHECK (name ~ '^[A-Za-z_][A-Za-z0-9_]{0,127}$'),
    version         bigint      NOT NULL CHECK (version > 0),
    key_id          text        NOT NULL CHECK (key_id <> ''),
    sealed          bytea       NOT NULL CHECK (octet_length(sealed) BETWEEN 48 AND 65584),
    created_by      uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, cluster_id) REFERENCES fleet.clusters (organization_id, id),
    UNIQUE (organization_id, id),
    UNIQUE (environment_id, app, name)
);
CREATE INDEX secret_deliveries_cluster_idx ON fleet.secret_deliveries (organization_id, cluster_id, created_at);
CREATE INDEX secret_deliveries_created_idx ON fleet.secret_deliveries (created_at);

-- The last delivery of a secret that did not arrive (the agent could not
-- store it, or nobody took it within the TTL): the console shows the secret
-- as failed until a newer value is on its way or one is stored.
CREATE TABLE fleet.secret_failures (
    organization_id uuid        NOT NULL,
    environment_id  uuid        NOT NULL,
    cluster_id      uuid        NOT NULL,
    app             text        NOT NULL,
    name            text        NOT NULL,
    version         bigint      NOT NULL CHECK (version > 0),
    error           text        NOT NULL CHECK (char_length(error) BETWEEN 1 AND 500),
    failed_at       timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, cluster_id) REFERENCES fleet.clusters (organization_id, id),
    PRIMARY KEY (environment_id, app, name)
);
CREATE INDEX secret_failures_org_idx ON fleet.secret_failures (organization_id, environment_id);

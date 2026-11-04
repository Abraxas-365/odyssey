-- Resources (2b): the desired state of every database, cache and files volume
-- of an environment, and what the agents last reported of them. An environment
-- is pinned to a cluster by its placement, exactly like its apps.

-- The generation only grows (an older change never wins late). deleted: the
-- resource and its data are to be removed; the row stays until the agent
-- reports it gone.
CREATE TABLE fleet.desired_resources (
    environment_id  uuid        NOT NULL REFERENCES fleet.placements (environment_id),
    name            text        NOT NULL,
    organization_id uuid        NOT NULL,
    project_id      uuid        NOT NULL,
    cluster_id      uuid        NOT NULL REFERENCES fleet.clusters (id),
    kind            text        NOT NULL CHECK (kind IN ('postgres', 'redis', 'files')),
    generation      bigint      NOT NULL CHECK (generation > 0),
    storage_gi      integer     NOT NULL CHECK (storage_gi BETWEEN 1 AND 100),
    deleted         boolean     NOT NULL DEFAULT false,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (environment_id, name)
);
CREATE INDEX desired_resources_cluster_idx ON fleet.desired_resources (organization_id, cluster_id);

-- Observations, not decisions. No foreign key: the row of a resource that
-- was reported gone is deleted together with its desired state.
CREATE TABLE fleet.resource_status (
    environment_id      uuid        NOT NULL,
    name                text        NOT NULL,
    organization_id     uuid        NOT NULL,
    cluster_id          uuid        NOT NULL REFERENCES fleet.clusters (id),
    observed_generation bigint      NOT NULL,
    phase               text        NOT NULL,
    message             text        NOT NULL DEFAULT '',
    observed_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (environment_id, name)
);

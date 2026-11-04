-- Resources of an environment (2b): databases, caches and file volumes. The
-- row is the decision (what exists, how big, whether it is being deleted);
-- what the cluster observes of it is read from the fleet, never stored here.
-- A deleted resource keeps its row, as 'deleting', until the cluster confirms
-- its data is erased; then the row is dropped (the name is free again).
CREATE TABLE resource.resources (
    id              uuid PRIMARY KEY,
    organization_id uuid        NOT NULL,
    project_id      uuid        NOT NULL,
    environment_id  uuid        NOT NULL,
    name            text        NOT NULL CHECK (name ~ '^[a-z]([a-z0-9-]*[a-z0-9])?$' AND length(name) <= 30),
    kind            text        NOT NULL CHECK (kind IN ('postgres', 'redis', 'files')),
    storage_gi      integer     NOT NULL CHECK (storage_gi BETWEEN 1 AND 100),
    generation      bigint      NOT NULL CHECK (generation > 0),
    status          text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'deleting')),
    created_by      uuid        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
-- Names are unique per environment; rows of a deleted resource are dropped,
-- so the name is free as soon as the cluster confirmed.
CREATE UNIQUE INDEX resources_name_idx ON resource.resources (organization_id, environment_id, name);
CREATE INDEX resources_environment_idx ON resource.resources (organization_id, environment_id, created_at);

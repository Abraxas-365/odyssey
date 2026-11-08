-- Catalog stage 2 (docs/catalog.md): a resource the company already has
-- has no size (storage_gi 0, only with an existing spec) and object storage
-- is a kind.
ALTER TABLE fleet.desired_resources
    DROP CONSTRAINT IF EXISTS desired_resources_kind_check,
    DROP CONSTRAINT IF EXISTS desired_resources_storage_gi_check;

ALTER TABLE fleet.desired_resources
    ADD CONSTRAINT desired_resources_kind_check CHECK (kind IN ('postgres', 'redis', 'files', 'object-storage')),
    ADD CONSTRAINT desired_resources_storage_gi_check CHECK (
        (storage_gi BETWEEN 1 AND 100 AND NOT spec ? 'existing')
        OR (storage_gi = 0 AND spec ? 'existing')
        OR (deleted AND storage_gi BETWEEN 0 AND 100)),
    -- a volume of the cluster is never one the company already has
    ADD CONSTRAINT desired_resources_existing_kind_check CHECK (NOT (spec ? 'existing' AND kind = 'files'));

-- Catalog stage 2 (docs/catalog.md): a resource the company already has
-- (a recipe on the existing engine) has no size. storage_gi stays NOT NULL:
-- 0 is "no size", and then params has no storage_gi; a sized resource keeps
-- both, equal. Object storage is a kind (only ever one the company has).
ALTER TABLE resource.resources
    DROP CONSTRAINT IF EXISTS resources_kind_check,
    DROP CONSTRAINT IF EXISTS resources_storage_gi_check,
    DROP CONSTRAINT IF EXISTS resources_params_check;

ALTER TABLE resource.resources
    ADD CONSTRAINT resources_kind_check CHECK (kind IN ('postgres', 'redis', 'files', 'object-storage')),
    ADD CONSTRAINT resources_storage_gi_check CHECK (storage_gi = 0 OR storage_gi BETWEEN 1 AND 100),
    ADD CONSTRAINT resources_params_check CHECK (
        jsonb_typeof(params) = 'object'
        AND (params ? 'storage_gi') = (storage_gi > 0)
        AND (NOT params ? 'storage_gi' OR (params ->> 'storage_gi')::int = storage_gi));

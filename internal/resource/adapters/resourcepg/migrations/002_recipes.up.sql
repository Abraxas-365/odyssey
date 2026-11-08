-- Resources are pinned to a catalog recipe (docs/catalog.md): recipe and
-- recipe_version name the immutable version that prepares it, params its
-- normalized parameters (the source of truth). Every resource from before
-- the catalog is what the basic recipe of its kind, version 1, prepares:
-- the backfill pins it there with its size. storage_gi stays, in step with
-- params.storage_gi (the CHECK), while consumers move to params.
ALTER TABLE resource.resources
    ADD COLUMN IF NOT EXISTS recipe         text,
    ADD COLUMN IF NOT EXISTS recipe_version integer,
    ADD COLUMN IF NOT EXISTS params         jsonb;

UPDATE resource.resources
   SET recipe = kind || '-basico', recipe_version = 1, params = jsonb_build_object('storage_gi', storage_gi)
 WHERE recipe IS NULL;

ALTER TABLE resource.resources
    ALTER COLUMN recipe SET NOT NULL,
    ALTER COLUMN recipe_version SET NOT NULL,
    ALTER COLUMN params SET NOT NULL,
    ADD CONSTRAINT resources_recipe_check CHECK (recipe ~ '^[a-z][a-z0-9-]{0,62}$' AND recipe_version > 0),
    ADD CONSTRAINT resources_params_check CHECK (jsonb_typeof(params) = 'object' AND (params ->> 'storage_gi')::int = storage_gi);

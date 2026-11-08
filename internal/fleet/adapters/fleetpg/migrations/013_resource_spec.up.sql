-- Catalog stage 1a, cut 4: a desired resource carries what resolved it
-- (recipe, "postgres-produccion@1"; empty before the catalog) and how the
-- kind's engine prepares it (spec, resourcespec.Spec as JSON; {} is the
-- basic resource every agent prepares). Existing rows are basic resources.
ALTER TABLE fleet.desired_resources
    ADD COLUMN IF NOT EXISTS recipe text  NOT NULL DEFAULT '' CHECK (length(recipe) <= 80),
    ADD COLUMN IF NOT EXISTS spec   jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(spec) = 'object');

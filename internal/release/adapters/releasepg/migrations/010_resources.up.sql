-- Resources an environment's app uses (2b): databases, caches and volumes
-- connected to it, as references only (name, kind, variable prefix). Each
-- settings revision is the whole configuration, so a connection is part of the
-- revision a deployment applies; the resources themselves belong to the
-- resource domain.
ALTER TABLE release.environment_settings
    ADD COLUMN resources jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(resources) = 'array');

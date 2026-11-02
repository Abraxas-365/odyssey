-- The commit a deployment's source was imported from (a build.GitRef); NULL
-- for an uploaded folder, and only a deployment from a source can have one.
ALTER TABLE release.deployments
    ADD COLUMN source_git jsonb,
    ADD CONSTRAINT deployments_source_git_object_check
        CHECK (source_git IS NULL OR jsonb_typeof(source_git) = 'object'),
    ADD CONSTRAINT deployments_source_git_source_check
        CHECK (source_git IS NULL OR source_id IS NOT NULL);

-- The recipe versions an organization publishes itself (docs/catalog.md):
-- today a resource the company already has (engine 'existing'). Like the
-- factory's versions, which are code, a published version never changes and
-- is never removed: resources and approvals are pinned to it. The trigger
-- holds that even for code that forgets; a new connection is a new version.
-- Ids start with 'org-', which no factory recipe does, so a later release of
-- Odicea never takes one. The spec is the resolved, typed engine spec
-- (pkg/resourcespec): references to the company's secret stores, never a
-- credential.
CREATE TABLE catalog.recipe_versions (
    organization_id uuid        NOT NULL,
    recipe          text        NOT NULL CHECK (recipe ~ '^org-[a-z0-9]+(-[a-z0-9]+)*$' AND length(recipe) <= 48),
    version         int         NOT NULL CHECK (version > 0),
    kind            text        NOT NULL,
    title           text        NOT NULL CHECK (length(title) BETWEEN 1 AND 80),
    description     text        NOT NULL DEFAULT '' CHECK (length(description) <= 500),
    engine          text        NOT NULL CHECK (engine = 'existing'),
    spec            jsonb       NOT NULL CHECK (jsonb_typeof(spec) = 'object'),
    created_by      uuid        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, recipe, version)
);

CREATE FUNCTION catalog.reject_version_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'a published recipe version never changes: publish a new version';
END $$;
CREATE TRIGGER recipe_versions_immutable BEFORE UPDATE OR DELETE ON catalog.recipe_versions
    FOR EACH ROW EXECUTE FUNCTION catalog.reject_version_change();
CREATE TRIGGER recipe_versions_no_truncate BEFORE TRUNCATE ON catalog.recipe_versions
    FOR EACH STATEMENT EXECUTE FUNCTION catalog.reject_version_change();

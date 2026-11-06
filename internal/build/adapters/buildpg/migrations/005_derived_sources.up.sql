-- Derived sources (multi-service apps): a service whose code lives in a
-- folder builds only that folder, cut from the uploaded source (parent_id)
-- and re-rooted. Made once per parent and folder while it lasts; a derived
-- source has its own bytes and its own life (its builds keep it). For the
-- sweep and the quota a parent is built (and needed) when it or one of the
-- sources cut from it is.
ALTER TABLE build.sources
    ADD COLUMN parent_id uuid REFERENCES build.sources (id),
    ADD COLUMN root      text NOT NULL DEFAULT '' CHECK (char_length(root) <= 200),
    ADD CHECK ((parent_id IS NULL) = (root = ''));

CREATE INDEX sources_parent_idx ON build.sources (parent_id) WHERE parent_id IS NOT NULL;
CREATE UNIQUE INDEX sources_derived_idx ON build.sources (parent_id, root) WHERE parent_id IS NOT NULL AND expired_at IS NULL;

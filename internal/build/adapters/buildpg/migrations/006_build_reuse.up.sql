-- Build reuse (MS-3b). A source's content_digest identifies its files
-- ("c1:<sha256 hex>", pkg/srcarchive): the same folder packed again has the
-- same one ('' for sources uploaded before: never reused). A build copies it
-- from its source, so the earlier builds of the same code are found by an
-- index. builder_identity is the identity of the builder that made the image
-- (reported when it succeeded; '' when the builder announced none). A build
-- of the same code with the same builder may reuse an earlier build's image
-- (reused_from) instead of building; rebuild asks it not to.
ALTER TABLE build.sources
    ADD COLUMN content_digest text NOT NULL DEFAULT '' CHECK (content_digest = '' OR content_digest ~ '^c[0-9]+:[a-f0-9]{64}$');

ALTER TABLE build.builds
    ADD COLUMN content_digest   text    NOT NULL DEFAULT '' CHECK (content_digest = '' OR content_digest ~ '^c[0-9]+:[a-f0-9]{64}$'),
    ADD COLUMN rebuild          boolean NOT NULL DEFAULT false,
    ADD COLUMN builder_identity text    NOT NULL DEFAULT '' CHECK (builder_identity = '' OR builder_identity ~ '^b[0-9]+:[a-f0-9]{64}$'),
    ADD COLUMN reused_from      uuid    REFERENCES build.builds (id),
    ADD CHECK (reused_from IS NULL OR status = 'succeeded');

CREATE INDEX builds_reuse_idx ON build.builds (organization_id, project_id, content_digest, builder_identity, finished_at DESC)
    WHERE status = 'succeeded' AND content_digest <> '' AND builder_identity <> '';

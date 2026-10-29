-- The quota counts an organization's unexpired sources.
CREATE INDEX sources_organization_idx ON build.sources (organization_id, created_at) WHERE expired_at IS NULL;

-- Where to start reading an archived log for a seq: [{seq, offset}, ...]
-- every build.LogIndexEvery lines ('[]' reads from the top).
ALTER TABLE build.builds ADD COLUMN logs_index jsonb NOT NULL DEFAULT '[]';

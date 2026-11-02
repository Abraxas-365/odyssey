-- The commit a source was imported from (a build.GitRef); NULL for an
-- uploaded folder.
ALTER TABLE build.sources
	ADD COLUMN git jsonb CHECK (git IS NULL OR jsonb_typeof(git) = 'object');

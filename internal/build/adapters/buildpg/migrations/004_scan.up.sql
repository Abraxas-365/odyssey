-- The vulnerability scan of a build's image (3c): recorded once, after the
-- build succeeded, by the build.scan workflow. NULL until then (and for a
-- build that did not succeed). The counts are those the agent's scanner
-- reported; error is set when the image could not be scanned.

ALTER TABLE build.builds
	ADD COLUMN scan jsonb CHECK (scan IS NULL OR jsonb_typeof(scan) = 'object');

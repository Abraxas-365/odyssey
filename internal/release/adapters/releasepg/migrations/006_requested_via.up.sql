-- Where a deployment was requested from: 'console', 'assistant:<preset>',
-- 'github' or 'platform' (the server decides it, never the caller). '' for
-- deployments made before this was recorded.
ALTER TABLE release.deployments ADD COLUMN requested_via text NOT NULL DEFAULT '';

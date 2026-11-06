-- Who asked for a deployment, by name, as the directory knew them right
-- after it started (a snapshot: a later rename does not rewrite history).
-- '' until the name is resolved, for deployments made before it was
-- recorded, and for the platform's own.
ALTER TABLE release.deployments ADD COLUMN requested_by_name text NOT NULL DEFAULT '';

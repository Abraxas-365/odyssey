-- Settings of some services only (MS-4). A service's own plain variables
-- live next to the whole app's (env); a secret's, a resource's and the AI's
-- services travel inside their JSON (secrets, resources, ai), so only the
-- new layer needs a column. A secret request keeps who will read the value:
-- loading it inherits that.
ALTER TABLE release.environment_settings ADD COLUMN service_env jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE release.secret_requests ADD COLUMN services text[] NOT NULL DEFAULT '{}';

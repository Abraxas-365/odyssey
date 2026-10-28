-- The cluster's own words about a failure, kept apart from the plain-Spanish
-- error_message (the console shows them only with technical details on).
ALTER TABLE release.deployments ADD COLUMN error_detail text NOT NULL DEFAULT '';

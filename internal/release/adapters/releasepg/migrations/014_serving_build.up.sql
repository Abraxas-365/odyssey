-- The inventory (cut 3c) asks, for each environment, which build made the
-- release it serves (to show that image's vulnerability scan): the oldest
-- deployment of a release that has a build. A rollback deploys the same
-- release without a build of its own, so the lookup goes through release_id.
CREATE INDEX deployments_release_build_idx ON release.deployments (release_id, created_at) WHERE build_id IS NOT NULL;

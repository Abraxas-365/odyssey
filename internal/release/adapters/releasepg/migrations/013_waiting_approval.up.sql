-- Deployments wait for IT (cut 3b): the authorize step may hold a deployment
-- until someone approves it. A waiting deployment is still the environment's
-- active one (a newer deployment replaces it and withdraws its request), and
-- one from source has no release yet while it waits at its build step.
ALTER TABLE release.deployments DROP CONSTRAINT deployments_status_check;
ALTER TABLE release.deployments ADD CONSTRAINT deployments_status_check
    CHECK (status IN ('building', 'waiting_approval', 'pending', 'applying', 'verifying', 'ready', 'failed', 'cancelled'));

ALTER TABLE release.deployments DROP CONSTRAINT deployments_release_check;
ALTER TABLE release.deployments ADD CONSTRAINT deployments_release_check
    CHECK (release_id IS NOT NULL OR (build_id IS NOT NULL AND step = 'build' AND status IN ('building', 'waiting_approval', 'failed', 'cancelled')));

DROP INDEX release.deployments_one_active_uq;
CREATE UNIQUE INDEX deployments_one_active_uq ON release.deployments (environment_id)
    WHERE status IN ('building', 'waiting_approval', 'pending', 'applying', 'verifying');

-- Deployments from source (phase 1b): a deployment may start by building an
-- uploaded folder. Until its build succeeds it has no release (nor image);
-- the build, the source it builds and the configuration the release will
-- get are recorded on the deployment itself.
ALTER TABLE release.deployments
    ALTER COLUMN release_id DROP NOT NULL,
    ADD COLUMN build_id    uuid,
    ADD COLUMN source_id   uuid,
    ADD COLUMN source_name text   NOT NULL DEFAULT '',
    ADD COLUMN source_size bigint NOT NULL DEFAULT 0,
    ADD COLUMN config      jsonb;

ALTER TABLE release.deployments DROP CONSTRAINT deployments_status_check;
ALTER TABLE release.deployments ADD CONSTRAINT deployments_status_check
    CHECK (status IN ('building', 'pending', 'applying', 'verifying', 'ready', 'failed', 'cancelled'));

-- A deployment builds from a source, with a configuration, or deploys a
-- release; only one that ended building (failed, cancelled) never got one.
ALTER TABLE release.deployments ADD CONSTRAINT deployments_source_check
    CHECK ((build_id IS NULL) = (source_id IS NULL) AND (build_id IS NULL) = (config IS NULL));
ALTER TABLE release.deployments ADD CONSTRAINT deployments_release_check
    CHECK (release_id IS NOT NULL OR (build_id IS NOT NULL AND step = 'build' AND status IN ('building', 'failed', 'cancelled')));
ALTER TABLE release.deployments ADD CONSTRAINT deployments_image_check
    CHECK ((release_id IS NULL) = (image = ''));

CREATE UNIQUE INDEX deployments_build_uq ON release.deployments (build_id) WHERE build_id IS NOT NULL;

DROP INDEX release.deployments_one_active_uq;
CREATE UNIQUE INDEX deployments_one_active_uq ON release.deployments (environment_id)
    WHERE status IN ('building', 'pending', 'applying', 'verifying');

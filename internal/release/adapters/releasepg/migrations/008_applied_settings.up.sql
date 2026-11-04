-- Who can open an app, honestly (phase 1e): the settings revision the cluster
-- was last told lives on the environment state, set by the apply step when the
-- cluster takes the app. A config deployment that fails afterwards leaves it
-- there: the app is not reopened by itself (fail-closed beats convenience).
ALTER TABLE release.environment_states ADD COLUMN applied_settings_revision bigint;

-- What the deployments that already went live handed their cluster.
UPDATE release.environment_states s SET applied_settings_revision = d.settings_revision
  FROM release.deployments d
 WHERE d.id = s.current_deployment_id AND d.settings_revision IS NOT NULL;

-- Apps published before settings existed are served open: their agents had no
-- access gate. Record it as it is ("public", by nobody) instead of letting the
-- default (team) claim a protection they never had, and without changing them
-- silently. New environments still default to team.
WITH open_apps AS (
    SELECT s.environment_id, s.organization_id
      FROM release.environment_states s
      LEFT JOIN release.deployments d ON d.id = s.current_deployment_id
     WHERE s.current_release_id IS NOT NULL
       AND d.settings_revision IS NULL          -- served by a deployment that predates settings
       AND NOT EXISTS (SELECT 1 FROM release.environment_settings e WHERE e.environment_id = s.environment_id)
), seeded AS (
    INSERT INTO release.environment_settings (environment_id, organization_id, revision, access_mode, created_by)
    SELECT environment_id, organization_id, 1, 'public', NULL FROM open_apps
    RETURNING environment_id
)
UPDATE release.environment_states s SET applied_settings_revision = 1
  FROM seeded WHERE s.environment_id = seeded.environment_id;

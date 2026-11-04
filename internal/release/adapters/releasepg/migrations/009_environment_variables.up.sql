-- Variables and secrets of an environment (2a). Each settings revision is the
-- whole configuration: env holds its plain variables ({"NAME": "value"}),
-- secrets its secret variables as references only (never a value: a managed
-- value travels sealed to the cluster through fleet.secret_deliveries; an
-- external one is read in the cluster from the company's vault).
ALTER TABLE release.environment_settings
    ADD COLUMN env     jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(env) = 'object'),
    ADD COLUMN secrets jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(secrets) = 'array');

-- A managed secret someone asked for (an assistant's request_secret) and no
-- one loaded yet. Not settings: until a value exists nothing changes in the
-- cluster. Loading it (or removing it) ends the request.
CREATE TABLE release.secret_requests (
    environment_id  uuid        NOT NULL,
    organization_id uuid        NOT NULL,
    name            text        NOT NULL CHECK (name ~ '^[A-Za-z_][A-Za-z0-9_]{0,127}$'),
    note            text        NOT NULL DEFAULT '',
    requested_by    uuid,
    requested_via   text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (environment_id, name)
);
CREATE INDEX secret_requests_org_idx ON release.secret_requests (organization_id, environment_id);

-- The high-water mark of each managed secret's version in an environment: the
-- highest version any settings revision ever carried (Repository.AppendSettings
-- raises it in the same transaction as the revision). It must NEVER go down,
-- not even if the settings history is purged or compacted one day: the
-- cluster may still hold that value and takes only newer versions, so a name
-- removed and loaded again must not reuse a number. That is why it is a table
-- of its own and not a scan of the history.
CREATE TABLE release.secret_versions (
    environment_id  uuid   NOT NULL,
    organization_id uuid   NOT NULL,
    name            text   NOT NULL,
    version         bigint NOT NULL CHECK (version > 0),
    PRIMARY KEY (environment_id, name)
);
CREATE INDEX secret_versions_org_idx ON release.secret_versions (organization_id, environment_id);

-- What the history already holds.
INSERT INTO release.secret_versions (environment_id, organization_id, name, version)
SELECT e.environment_id, e.organization_id, s->>'name', max((s->>'version')::bigint)
FROM release.environment_settings e, jsonb_array_elements(e.secrets) AS s
WHERE s->>'source' = 'managed' AND (s->>'version')::bigint > 0
GROUP BY e.environment_id, e.organization_id, s->>'name'
ON CONFLICT (environment_id, name) DO NOTHING;

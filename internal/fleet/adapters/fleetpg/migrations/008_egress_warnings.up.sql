-- What TI should review although the agent applied the policy (2d): an allow
-- entry that includes a node of the cluster. The agent reports it with its
-- acknowledgement (a JSON array of sentences, at most ten).
ALTER TABLE fleet.clusters
    ADD COLUMN policy_warnings jsonb NOT NULL DEFAULT '[]'
        CONSTRAINT clusters_policy_warnings_array CHECK (jsonb_typeof(policy_warnings) = 'array');

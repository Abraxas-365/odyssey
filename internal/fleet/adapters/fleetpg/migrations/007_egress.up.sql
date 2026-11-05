-- Egress policy (2d): what the apps of a cluster may reach. It lives on the
-- cluster (one policy per cluster, chosen by TI) next to the generation the
-- agent acknowledges: policy_generation grows with every change, and the
-- agent answers with the generation it applied (policy_acked_generation) and
-- the error, if it could not (policy_error).
ALTER TABLE fleet.clusters
    ADD COLUMN egress                  jsonb       NOT NULL DEFAULT '{"mode": "internet", "allow": []}',
    ADD COLUMN policy_generation       bigint      NOT NULL DEFAULT 0 CHECK (policy_generation >= 0),
    ADD COLUMN policy_acked_generation bigint,
    ADD COLUMN policy_error            text        NOT NULL DEFAULT '',
    ADD COLUMN policy_acked_at         timestamptz,
    ADD CONSTRAINT clusters_egress_mode CHECK (egress->>'mode' IN ('internet', 'restricted'));

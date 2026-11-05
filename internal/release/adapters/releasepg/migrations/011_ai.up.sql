-- The app's use of the organization's AI gateway (2d-ai), part of each
-- settings revision like the access mode: whether it is on, the version of
-- the managed key it waits for, the cluster key that was sealed to and the
-- gateway address. Never the key itself: that travels sealed to the cluster
-- (fleet.secret_deliveries) and the ai domain owns its lifecycle.
ALTER TABLE release.environment_settings
    ADD COLUMN ai jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(ai) = 'object');

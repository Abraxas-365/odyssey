-- Multi-service apps (MS-2): each service's part of an app's rollout, as
-- the agent reported it ([] from an agent that runs one workload per app).
ALTER TABLE fleet.app_status ADD COLUMN services jsonb NOT NULL DEFAULT '[]'::jsonb;

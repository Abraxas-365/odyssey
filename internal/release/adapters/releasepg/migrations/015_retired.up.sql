-- Archiving a project retires its app from the cluster: the environment
-- records when (and its revision moves above every deployment). A later
-- deployment that reaches the cluster clears it.
ALTER TABLE release.environment_states ADD COLUMN retired_at timestamptz;

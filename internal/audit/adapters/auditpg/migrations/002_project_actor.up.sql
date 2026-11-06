-- Everything about one app (the console's per-app filter): the entries whose
-- payload names the project, whatever their subject. And the people filter
-- and the weekly summary read by actor and by time.
CREATE INDEX entries_project_idx ON audit.entries (organization_id, (payload->>'project_id'), seq DESC);
CREATE INDEX entries_actor_idx ON audit.entries (organization_id, actor_id, occurred_at DESC)
    WHERE actor_id IS NOT NULL;

-- MS-3c: a change of an app's services (set_services) is about the whole
-- app, not one environment: its request has no environment.
ALTER TABLE approval.requests ALTER COLUMN environment_id DROP NOT NULL;

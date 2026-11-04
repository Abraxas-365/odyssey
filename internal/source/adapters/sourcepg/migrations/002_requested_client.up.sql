-- The client of the request that queued a push ("mcp:<id>" for an assistant,
-- the console's id, '' for a webhook or the poll): a deployment's
-- requested_via is derived from it when the push is deployed.
ALTER TABLE source.pushes ADD COLUMN requested_client text NOT NULL DEFAULT '';

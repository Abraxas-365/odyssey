-- The OAuth client (event.Meta.Client) of the request that started the run:
-- the events its steps publish name the same channel (console, an
-- assistant, GitHub) as the request did. Empty for runs started before.
ALTER TABLE workflow.runs ADD COLUMN client text NOT NULL DEFAULT '';

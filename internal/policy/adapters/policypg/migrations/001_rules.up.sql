-- IT's rules: CEL expressions that allow, block or hold an action for
-- approval (docs/policy.md). Every organization also has the built-in rules
-- (builtin = their stable key), inserted lazily on the first read; a
-- built-in can be disabled, never deleted. version moves up with each edit:
-- the CEL compile cache is keyed by (id, version).
CREATE TABLE policy.rules (
    id              uuid        PRIMARY KEY,
    organization_id uuid        NOT NULL,
    name            text        NOT NULL,
    description     text        NOT NULL DEFAULT '',
    expression      text        NOT NULL,
    effect          text        NOT NULL CHECK (effect IN ('allow', 'deny', 'approval')),
    approvers       text        NOT NULL DEFAULT 'odyssey:approvals:decide',
    enabled         boolean     NOT NULL DEFAULT true,
    builtin         text,
    version         int         NOT NULL DEFAULT 1 CHECK (version > 0),
    created_by      uuid,
    updated_by      uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
-- one built-in of each key per organization: the seeding is ON CONFLICT DO NOTHING
CREATE UNIQUE INDEX rules_builtin_idx ON policy.rules (organization_id, builtin) WHERE builtin IS NOT NULL;
CREATE INDEX rules_org_idx ON policy.rules (organization_id, created_at, id);

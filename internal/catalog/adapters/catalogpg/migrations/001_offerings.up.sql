-- What each organization offers of the recipe versions the binary ships
-- (docs/catalog.md). Recipe versions themselves are code (immutable); a row
-- here is the organization's mutable offering of one recipe: the version
-- offered, the tiers, the hard limits, the pre-approved range and the tiers
-- where it is its kind's default. Rows are seeded lazily on the first read
-- (ON CONFLICT DO NOTHING); revision moves up with each edit.
CREATE TABLE catalog.offerings (
    organization_id uuid        NOT NULL,
    recipe          text        NOT NULL CHECK (recipe ~ '^[a-z][a-z0-9-]{0,62}$'),
    version         int         NOT NULL CHECK (version > 0),
    active          boolean     NOT NULL,
    tiers           text[]      NOT NULL DEFAULT '{}',
    defaults        text[]      NOT NULL DEFAULT '{}' CHECK (defaults <@ tiers),
    limits          jsonb       NOT NULL DEFAULT '{}',
    preapproved     jsonb       NOT NULL DEFAULT '{}',
    revision        int         NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_by      uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, recipe)
);

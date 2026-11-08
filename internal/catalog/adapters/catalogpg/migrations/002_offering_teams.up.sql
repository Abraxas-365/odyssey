-- An offering may be limited to teams: IAMKit group ids of the organization
-- (docs/catalog.md). Empty is everyone, as before. They narrow who creates
-- the recipe, never what already exists.
ALTER TABLE catalog.offerings
    ADD COLUMN teams text[] NOT NULL DEFAULT '{}' CHECK (cardinality(teams) <= 50);

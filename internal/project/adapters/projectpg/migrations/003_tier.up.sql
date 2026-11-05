-- Tier: how much the app matters to the company. IT's rules (the policy
-- schema) read it; only the projects:tier permission changes it. Existing
-- apps become "internal", the middle level.
ALTER TABLE project.projects
    ADD COLUMN tier text NOT NULL DEFAULT 'internal' CHECK (tier IN ('sandbox', 'internal', 'production'));

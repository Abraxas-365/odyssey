-- Environments: where a project runs. Every project is born with
-- "production"; staging and PR previews come later. What runs in an
-- environment (its current release) lives in the release schema.
CREATE TABLE project.environments (
    id              uuid PRIMARY KEY,
    organization_id uuid        NOT NULL,
    project_id      uuid        NOT NULL REFERENCES project.projects (id),
    slug            text        NOT NULL CHECK (slug ~ '^[a-z][a-z0-9-]{1,38}[a-z0-9]$' AND slug NOT LIKE '%--%'),
    kind            text        NOT NULL CHECK (kind IN ('production', 'staging', 'preview')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, slug)
);
CREATE INDEX environments_project_idx ON project.environments (organization_id, project_id);

-- A project has at most one production environment.
CREATE UNIQUE INDEX environments_one_production_uq ON project.environments (project_id) WHERE kind = 'production';

-- Projects created before environments existed get their production one.
INSERT INTO project.environments (id, organization_id, project_id, slug, kind, created_at)
SELECT gen_random_uuid(), p.organization_id, p.id, 'production', 'production', p.created_at
  FROM project.projects p;

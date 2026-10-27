-- Projects: the root of everything a team ships. Rows are never deleted:
-- archiving keeps builds, releases and audit history attached.
CREATE TABLE project.projects (
    id              uuid PRIMARY KEY,
    organization_id uuid        NOT NULL,
    name            text        NOT NULL CHECK (name <> ''),
    slug            text        NOT NULL CHECK (slug ~ '^[a-z][a-z0-9-]{1,38}[a-z0-9]$' AND slug NOT LIKE '%--%'),
    description     text        NOT NULL DEFAULT '',
    status          text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_by      uuid        NOT NULL,
    updated_by      uuid        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    archived_at     timestamptz,
    archived_by     uuid,
    CHECK ((status = 'archived') = (archived_at IS NOT NULL)),
    CHECK ((archived_at IS NULL) = (archived_by IS NULL))
);

-- The slug is part of hostnames: unique per organization forever (an
-- archived project keeps its slug so old links never point at another app).
CREATE UNIQUE INDEX projects_slug_uq ON project.projects (organization_id, slug);
CREATE INDEX projects_list_idx ON project.projects (organization_id, status, lower(name), id);

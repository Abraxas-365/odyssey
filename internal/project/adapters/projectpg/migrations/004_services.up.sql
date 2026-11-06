-- Services: the workloads of an app (its web, its API, a queue worker). An
-- app has at least one, in the order people listed them. The project owns
-- this topology; how each service runs belongs to each version (release).
CREATE TABLE project.services (
    organization_id uuid        NOT NULL,
    project_id      uuid        NOT NULL REFERENCES project.projects (id),
    name            text        NOT NULL CHECK (name ~ '^[a-z][a-z0-9]{0,19}$'),
    kind            text        NOT NULL CHECK (kind IN ('web', 'worker')),
    root            text        NOT NULL DEFAULT '' CHECK (char_length(root) <= 200),
    route           text        NOT NULL DEFAULT '' CHECK (char_length(route) <= 100),
    strip_prefix    boolean     NOT NULL DEFAULT false,
    position        int         NOT NULL CHECK (position BETWEEN 0 AND 7),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, name),
    CHECK (kind = 'web' OR (route = '' AND NOT strip_prefix))
);
CREATE INDEX services_project_idx ON project.services (organization_id, project_id, position);
-- Two services never answer the same path of an app.
CREATE UNIQUE INDEX services_route_uq ON project.services (project_id, route) WHERE route <> '';

-- Every app made before services existed is one web service with all of its
-- code at the whole address.
INSERT INTO project.services (organization_id, project_id, name, kind, root, route, position, updated_at)
SELECT p.organization_id, p.id, 'web', 'web', '', '/', 0, p.created_at
  FROM project.projects p;

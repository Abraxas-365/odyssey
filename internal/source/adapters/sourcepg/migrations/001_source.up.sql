-- Installations of Odyssey's GitHub App linked to an organization. GitHub's
-- installation id is the identity: one installation, one organization.
CREATE TABLE source.installations (
    id               uuid PRIMARY KEY,
    organization_id  uuid        NOT NULL,
    external_id      bigint      NOT NULL UNIQUE CHECK (external_id > 0),
    account          text        NOT NULL,
    account_type     text        NOT NULL,
    all_repositories boolean     NOT NULL DEFAULT false,
    suspended        boolean     NOT NULL DEFAULT false,
    linked_by        uuid,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX installations_org_idx ON source.installations (organization_id, account);

-- Connections: a project's repository branch (one per project). The
-- repository is GitHub's numeric id (it survives renames); repository is its
-- name as last seen. last_commit is the newest commit queued; etag and
-- checked_at are the poll's.
CREATE TABLE source.connections (
    id              uuid PRIMARY KEY,
    organization_id uuid        NOT NULL,
    project_id      uuid        NOT NULL,
    installation_id uuid        NOT NULL REFERENCES source.installations (id),
    repository_id   bigint      NOT NULL CHECK (repository_id > 0),
    repository      text        NOT NULL,
    branch          text        NOT NULL CHECK (branch <> ''),
    root            text        NOT NULL DEFAULT '',
    config          jsonb       NOT NULL CHECK (jsonb_typeof(config) = 'object'),
    last_commit     text        NOT NULL DEFAULT '',
    etag            text        NOT NULL DEFAULT '',
    checked_at      timestamptz,
    connected_by    uuid        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, project_id)
);
CREATE INDEX connections_repository_idx ON source.connections (installation_id, repository_id);
CREATE INDEX connections_reconcile_idx ON source.connections (checked_at NULLS FIRST, id);

-- Pushes: commits to publish, in the order queued (seq). Deduplication
-- happens under the connection's lock (a webhook or the poll queue a commit
-- other than last_commit; "Publicar ahora" always queues), so a branch
-- force-pushed back to an earlier commit publishes it again. The history
-- outlives the connection (no foreign key).
CREATE TABLE source.pushes (
    id              uuid PRIMARY KEY,
    seq             bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    organization_id uuid        NOT NULL,
    project_id      uuid        NOT NULL,
    connection_id   uuid        NOT NULL,
    git             jsonb       NOT NULL CHECK (jsonb_typeof(git) = 'object'),
    trigger         text        NOT NULL CHECK (trigger IN ('webhook', 'reconcile', 'manual')),
    status          text        NOT NULL CHECK (status IN ('queued', 'importing', 'deployed', 'failed', 'skipped')),
    deployment_id   uuid,
    error           jsonb CHECK (error IS NULL OR jsonb_typeof(error) = 'object'),
    requested_by    uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CHECK ((status = 'deployed') = (deployment_id IS NOT NULL)),
    CHECK (status <> 'failed' OR error IS NOT NULL)
);
CREATE UNIQUE INDEX pushes_deployment_idx ON source.pushes (organization_id, deployment_id) WHERE deployment_id IS NOT NULL;
CREATE INDEX pushes_project_idx ON source.pushes (organization_id, project_id, seq DESC);
CREATE INDEX pushes_unfinished_idx ON source.pushes (connection_id, seq) WHERE status IN ('queued', 'importing');

-- Webhook deliveries seen (GitHub redelivers; each is handled once).
CREATE TABLE source.deliveries (
    id      text PRIMARY KEY CHECK (id <> ''),
    seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX deliveries_seen_idx ON source.deliveries (seen_at);

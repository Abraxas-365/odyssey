-- Sources: uploaded archives of an app's code. The bytes live in blob
-- storage under blob_key; the row is the index. A sweep expires old sources
-- (expired_at) and then deletes their bytes (bytes_deleted_at); the images
-- built from them stay.
CREATE TABLE build.sources (
    id               uuid PRIMARY KEY,
    organization_id  uuid        NOT NULL,
    project_id       uuid        NOT NULL,
    name             text        NOT NULL,
    size             bigint      NOT NULL CHECK (size > 0),
    sha256           text        NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    files            integer     NOT NULL CHECK (files >= 0),
    unpacked_size    bigint      NOT NULL CHECK (unpacked_size >= 0),
    blob_key         text        NOT NULL UNIQUE,
    uploaded_by      uuid,
    created_at       timestamptz NOT NULL DEFAULT now(),
    expired_at       timestamptz,
    bytes_deleted_at timestamptz,
    CHECK (bytes_deleted_at IS NULL OR expired_at IS NOT NULL)
);
CREATE INDEX sources_sweep_idx ON build.sources (created_at) WHERE expired_at IS NULL;
CREATE INDEX sources_undeleted_idx ON build.sources (expired_at) WHERE expired_at IS NOT NULL AND bytes_deleted_at IS NULL;

-- Builds: one attempt each to make an image of a source, in the cluster of
-- the environment it is for. A finished build never changes (but for where
-- its log is archived).
CREATE TABLE build.builds (
    id              uuid PRIMARY KEY,
    organization_id uuid        NOT NULL,
    project_id      uuid        NOT NULL,
    environment_id  uuid        NOT NULL,
    source_id       uuid        NOT NULL REFERENCES build.sources (id),
    status          text        NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    image           text        NOT NULL DEFAULT '',
    error_code      text,
    error_message   text,
    error_detail    text        NOT NULL DEFAULT '',
    run_id          uuid        NOT NULL,
    requested_by    uuid,
    logs_key        text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    finished_at     timestamptz,
    CHECK ((status IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL)),
    CHECK ((status = 'failed') = (error_code IS NOT NULL)),
    CHECK ((status = 'succeeded') = (image ~ '@sha256:[a-f0-9]{64}$'))
);
CREATE INDEX builds_source_idx ON build.builds (source_id) WHERE finished_at IS NULL;
CREATE INDEX builds_environment_idx ON build.builds (organization_id, environment_id, created_at DESC);

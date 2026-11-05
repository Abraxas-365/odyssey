-- Image scans (3c): what the agent reported of the vulnerability scan of a
-- build's image (observations, like build_status). A scan is stored only for
-- the digest the build's succeeded status reported; it goes away with the
-- desired build.

CREATE TABLE fleet.build_scans (
    build_id        uuid        PRIMARY KEY,
    organization_id uuid        NOT NULL,
    image_digest    text        NOT NULL CHECK (image_digest ~ '^sha256:[a-f0-9]{64}$'),
    critical        integer     NOT NULL DEFAULT 0 CHECK (critical >= 0),
    high            integer     NOT NULL DEFAULT 0 CHECK (high >= 0),
    medium          integer     NOT NULL DEFAULT 0 CHECK (medium >= 0),
    low             integer     NOT NULL DEFAULT 0 CHECK (low >= 0),
    tool            text        NOT NULL DEFAULT '',
    db_updated_at   text        NOT NULL DEFAULT '',
    -- not empty: the scan could not be done and the counts mean nothing
    error           text        NOT NULL DEFAULT '',
    observed_at     timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, build_id) REFERENCES fleet.desired_builds (organization_id, id) ON DELETE CASCADE
);

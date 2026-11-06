-- The services of a deployment from source and the build of each: the
-- app's services as they were when the deployment was asked for (its
-- version is made of exactly these, whatever changes meanwhile). The first
-- one's build is also deployments.build_id. A deployment made before apps
-- had services has none here: its build_id is its only build.
CREATE TABLE release.deployment_builds (
    deployment_id uuid    NOT NULL REFERENCES release.deployments (id),
    position      int     NOT NULL CHECK (position BETWEEN 0 AND 7),
    name          text    NOT NULL CHECK (name ~ '^[a-z][a-z0-9]{0,19}$'),
    kind          text    NOT NULL CHECK (kind IN ('web', 'worker')),
    root          text    NOT NULL DEFAULT '' CHECK (char_length(root) <= 200),
    route         text    NOT NULL DEFAULT '' CHECK (char_length(route) <= 100),
    strip_prefix  boolean NOT NULL DEFAULT false,
    build_id      uuid    NOT NULL UNIQUE,
    PRIMARY KEY (deployment_id, name),
    UNIQUE (deployment_id, position)
);

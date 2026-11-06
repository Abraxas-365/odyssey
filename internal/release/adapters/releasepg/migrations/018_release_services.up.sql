-- The services of a version: the app's topology when the version was made
-- (which services, which path of the address each answers, where its code
-- was) and the image and run settings of each. A version never changes, so
-- deploying it again (rollback, auto-restore) brings back exactly this.
CREATE TABLE release.release_services (
    release_id   uuid    NOT NULL REFERENCES release.releases (id),
    position     int     NOT NULL CHECK (position BETWEEN 0 AND 7),
    name         text    NOT NULL CHECK (name ~ '^[a-z][a-z0-9]{0,19}$'),
    kind         text    NOT NULL CHECK (kind IN ('web', 'worker')),
    root         text    NOT NULL DEFAULT '' CHECK (char_length(root) <= 200),
    route        text    NOT NULL DEFAULT '' CHECK (char_length(route) <= 100),
    strip_prefix boolean NOT NULL DEFAULT false,
    image        text    NOT NULL CHECK (image ~ '@sha256:[a-f0-9]{64}$'),
    port         int     NOT NULL DEFAULT 0 CHECK (port BETWEEN 0 AND 65535),
    health_path  text    NOT NULL DEFAULT '',
    replicas     int     NOT NULL CHECK (replicas BETWEEN 1 AND 10),
    PRIMARY KEY (release_id, name),
    UNIQUE (release_id, position)
);

-- Every version made before services existed ran one web service with all
-- of the code at the whole address.
INSERT INTO release.release_services (release_id, position, name, kind, root, route, image, port, health_path, replicas)
SELECT r.id, 0, 'web', 'web', '', '/', r.image,
       COALESCE((r.config ->> 'port')::int, 8080),
       COALESCE(NULLIF(r.config ->> 'health_path', ''), '/'),
       GREATEST(COALESCE((r.config ->> 'replicas')::int, 1), 1)
  FROM release.releases r;

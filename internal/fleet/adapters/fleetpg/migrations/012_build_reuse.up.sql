-- Build reuse (MS-3b): a build of code an earlier build already made, with
-- the same builder, may reuse that image. builder_identity is the identity
-- the cluster's builder announced when the build was sent (what made the
-- image; '' when it announced none). reuse_image is what the cluster is told
-- to reuse ('' builds); skip_scan, that the build domain copies the earlier
-- scan. reused is what the agent reported: it reused the image instead of
-- building (stored only when the digest is reuse_image's).
ALTER TABLE fleet.desired_builds
    ADD COLUMN builder_identity text    NOT NULL DEFAULT '' CHECK (builder_identity = '' OR builder_identity ~ '^b[0-9]+:[a-f0-9]{64}$'),
    ADD COLUMN reuse_image      text    NOT NULL DEFAULT '' CHECK (reuse_image = '' OR reuse_image ~ '^[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}$'),
    ADD COLUMN skip_scan        boolean NOT NULL DEFAULT false CHECK (NOT skip_scan OR reuse_image <> '');

ALTER TABLE fleet.build_status ADD COLUMN reused boolean NOT NULL DEFAULT false;

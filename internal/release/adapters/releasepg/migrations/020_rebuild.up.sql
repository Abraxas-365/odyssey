-- A deployment from source may ask to build every service again even when
-- an earlier build of the same code could be reused (MS-3b, "Reconstruir
-- todo"): a newer base image, a fix outside the code. Whether a build did
-- reuse is the build's own (build.builds.reused_from).
ALTER TABLE release.deployments ADD COLUMN rebuild boolean NOT NULL DEFAULT false;

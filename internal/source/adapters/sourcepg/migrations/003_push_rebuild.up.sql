-- A push someone published with "Reconstruir todo": its deployment builds
-- every service again instead of reusing images of the same code (MS-3b).
ALTER TABLE source.pushes ADD COLUMN rebuild boolean NOT NULL DEFAULT false;

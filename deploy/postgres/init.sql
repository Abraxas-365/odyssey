-- One Postgres server, one database per service. IAMKit refuses to share
-- a schema with another application, so each gets its own database.
CREATE DATABASE odyssey;
CREATE DATABASE iamkit;

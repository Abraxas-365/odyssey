-- What changed in a version, in the words of whoever asked for it (UX v4
-- §5.3): an assistant says what it did, a person may write it. One line, at
-- most 200 characters; '' when nobody said (and for versions made before it
-- was recorded). An upload ticket keeps it until the upload starts the
-- deployment.
ALTER TABLE release.deployments ADD COLUMN message text NOT NULL DEFAULT ''
    CHECK (char_length(message) <= 200);
ALTER TABLE release.upload_tickets ADD COLUMN message text NOT NULL DEFAULT ''
    CHECK (char_length(message) <= 200);

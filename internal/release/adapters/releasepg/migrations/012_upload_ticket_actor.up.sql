-- The permissions of whoever asked for an upload ticket (policy 3a): the
-- upload is redeemed later, from a request with no token (the ticket is the
-- credential), and IT's rules judge the deployment as the person who asked,
-- with the permissions their token held then. A snapshot, not a grant: it is
-- only read to evaluate rules. Tickets issued before this column hold none.
ALTER TABLE release.upload_tickets
    ADD COLUMN permissions text[] NOT NULL DEFAULT '{}';

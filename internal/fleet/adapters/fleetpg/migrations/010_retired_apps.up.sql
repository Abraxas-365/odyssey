-- Retired apps (their project archived): the desired app stays, marked
-- deleted at a revision above every deployment, until its cluster reports it
-- gone; then it and its status are forgotten. A later deployment of the same
-- name revives it.
ALTER TABLE fleet.desired_apps ADD COLUMN deleted boolean NOT NULL DEFAULT false;

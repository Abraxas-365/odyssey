-- An assistant may ask, with deploy_folder's rebuild, that the deployment the
-- upload starts build every service again (MS-7; MS-3b's "Reconstruir todo").
-- The upload is redeemed later, so the ticket keeps the wish.
ALTER TABLE release.upload_tickets ADD COLUMN rebuild boolean NOT NULL DEFAULT false;

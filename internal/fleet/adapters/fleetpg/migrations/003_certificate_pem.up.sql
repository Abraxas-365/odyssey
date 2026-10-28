-- The certificate last issued to each cluster (public data): an agent whose
-- Enroll or Renew answer was lost gets it back with Recover by proving it
-- holds the key it names.
ALTER TABLE fleet.clusters ADD COLUMN certificate_pem bytea;

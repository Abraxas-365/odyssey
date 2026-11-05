-- A revoke_failed key is retried by the sweep with a growing wait instead of
-- every tick forever: revoke_attempts counts the revocations that failed since
-- a person last asked (turning the AI off again starts over) and next_retry_at
-- is when the sweep may try again (NULL: now). The wait doubles from 5 minutes
-- up to 6 hours (aipg.FailRevocation). The sweep takes the due keys, those
-- that never waited first, so a gateway outage that keeps one environment
-- failing cannot starve the others.
ALTER TABLE ai.keys ADD COLUMN revoke_attempts integer NOT NULL DEFAULT 0 CHECK (revoke_attempts >= 0);
ALTER TABLE ai.keys ADD COLUMN next_retry_at timestamptz;
-- Keys already failed were reported once: they count that attempt and are due.
UPDATE ai.keys SET revoke_attempts = 1 WHERE status = 'revoke_failed';
DROP INDEX ai.keys_revoke_failed_idx;
CREATE INDEX keys_revoke_failed_idx ON ai.keys (next_retry_at NULLS FIRST, created_at) WHERE status = 'revoke_failed';

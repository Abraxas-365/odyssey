-- A key whose revocation could not be confirmed at the gateway (the gateway
-- unreachable or not connected, retries exhausted) is revoke_failed instead of
-- revoking forever: the console shows it, a person can retry (turning the AI
-- off again) and a sweep retries it on its own. revoke_error is why, for IT
-- (never a key).
ALTER TABLE ai.keys DROP CONSTRAINT keys_status_check;
ALTER TABLE ai.keys ADD CONSTRAINT keys_status_check
    CHECK (status IN ('creating', 'active', 'revoking', 'revoke_failed'));
ALTER TABLE ai.keys ADD COLUMN revoke_error text NOT NULL DEFAULT '';
CREATE INDEX keys_revoke_failed_idx ON ai.keys (created_at) WHERE status = 'revoke_failed';

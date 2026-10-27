-- Transactional outbox: an event is inserted in the same transaction as the
-- write it describes, with one delivery row per subscriber. The dispatcher
-- runs each delivery in its own transaction (handler effects + done mark).
CREATE TABLE event.outbox (
    seq             bigserial PRIMARY KEY,           -- insertion order
    id              uuid        NOT NULL UNIQUE,
    organization_id uuid        NOT NULL,
    type            text        NOT NULL,
    version         int         NOT NULL CHECK (version > 0),
    actor_id        uuid,                            -- NULL: system action
    subject         text        NOT NULL,
    summary         text        NOT NULL,
    payload         jsonb       NOT NULL,
    occurred_at     timestamptz NOT NULL,
    correlation_id  text        NOT NULL,
    causation_id    uuid,
    client          text        NOT NULL DEFAULT ''
);
CREATE INDEX outbox_correlation_idx ON event.outbox (correlation_id);
CREATE INDEX outbox_occurred_idx ON event.outbox (occurred_at);

CREATE TABLE event.deliveries (
    event_id        uuid        NOT NULL REFERENCES event.outbox (id) ON DELETE CASCADE,
    subscriber      text        NOT NULL,
    seq             bigint      NOT NULL,             -- copy of outbox.seq: delivery order
    organization_id uuid        NOT NULL,
    status          text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'dead')),
    attempts        int         NOT NULL DEFAULT 0,
    next_at         timestamptz NOT NULL DEFAULT now(),
    last_error      text,
    done_at         timestamptz,
    PRIMARY KEY (event_id, subscriber)
);
-- unordered subscribers: the oldest due delivery
CREATE INDEX deliveries_due_idx ON event.deliveries (subscriber, seq) WHERE status = 'pending';
-- ordered subscribers: the oldest undelivered event of one organization
CREATE INDEX deliveries_org_idx ON event.deliveries (subscriber, organization_id, seq) WHERE status <> 'done';

ALTER TABLE notifications
    ADD COLUMN attempts         INT NOT NULL DEFAULT 0,
    ADD COLUMN max_attempts     INT NOT NULL DEFAULT 5,
    ADD COLUMN next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN locked_until     TIMESTAMPTZ,
    ADD COLUMN provider_message_id  TEXT,
    ADD COLUMN last_error       TEXT,
    ADD COLUMN sent_at          TIMESTAMPTZ;

ALTER TABLE notifications
    ADD CONSTRAINT notifications_status_check
    CHECK (status IN('pending', 'processing', 'sent', 'failed', 'cancelled'));

CREATE INDEX idx_notifications_claim
    ON notifications (priority,next_attempt_at)
    WHERE status IN ('pending', 'processing');
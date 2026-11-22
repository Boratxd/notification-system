ALTER TABLE notifications
    ADD COLUMN idempotency_key TEXT,
    ADD COLUMN batch_id UUID;

CREATE UNIQUE INDEX uniq_notifications_idempotency_key
    ON notifications (idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE INDEX idx_notifications_batch_id
    ON notifications (batch_id) WHERE batch_id IS NOT NULL;
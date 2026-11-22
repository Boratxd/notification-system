DROP INDEX idx_notifications_batch_id;
DROP INDEX uniq_notifications_idempotency_key;
ALTER TABLE notifications DROP COLUMN idempotency_key, DROP COLUMN batch_id;
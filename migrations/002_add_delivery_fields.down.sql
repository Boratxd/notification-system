DROP INDEX idx_notifications_claim;
ALTER TABLE notifications DROP CONSTRAINT notifications_status_check;
ALTER TABLE notifications
    DROP COLUMN attempts, DROP COLUMN max_attempts, DROP COLUMN next_attempt_at,
    DROP COLUMN locked_until, DROP COLUMN provider_message_id,
    DROP COLUMN last_error, DROP COLUMN sent_at;
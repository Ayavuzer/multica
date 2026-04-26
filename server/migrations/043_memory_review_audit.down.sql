DROP TABLE IF EXISTS memory_review_audit;

UPDATE memory_entry
SET status = 'rejected',
    approved_at = NULL,
    rejected_at = COALESCE(rejected_at, now()),
    updated_at = now()
WHERE status = 'revoked';

ALTER TABLE memory_entry DROP CONSTRAINT IF EXISTS memory_entry_status_check;
ALTER TABLE memory_entry
    ADD CONSTRAINT memory_entry_status_check
    CHECK (status IN ('pending', 'approved', 'rejected'));

ALTER TABLE memory_entry DROP CONSTRAINT IF EXISTS memory_entry_status_timestamp_check;
ALTER TABLE memory_entry
    ADD CONSTRAINT memory_entry_check1
    CHECK (
        (status = 'pending' AND approved_at IS NULL AND rejected_at IS NULL)
        OR (status = 'approved' AND approved_at IS NOT NULL AND rejected_at IS NULL)
        OR (status = 'rejected' AND approved_at IS NULL AND rejected_at IS NOT NULL)
    );

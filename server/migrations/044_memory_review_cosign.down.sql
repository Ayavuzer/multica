DROP INDEX IF EXISTS idx_memory_review_audit_cosign;
DROP INDEX IF EXISTS idx_memory_review_audit_source_comment;

ALTER TABLE memory_review_audit
    DROP COLUMN IF EXISTS co_sign_actor_role,
    DROP COLUMN IF EXISTS co_sign_actor_id,
    DROP COLUMN IF EXISTS co_sign_actor_type,
    DROP COLUMN IF EXISTS co_sign_comment_id,
    DROP COLUMN IF EXISTS source_comment_id;

ALTER TABLE memory_review_audit
    ADD COLUMN source_comment_id UUID REFERENCES comment(id) ON DELETE SET NULL,
    ADD COLUMN co_sign_comment_id UUID REFERENCES comment(id) ON DELETE SET NULL,
    ADD COLUMN co_sign_actor_type TEXT CHECK (co_sign_actor_type IN ('member')),
    ADD COLUMN co_sign_actor_id UUID,
    ADD COLUMN co_sign_actor_role TEXT CHECK (co_sign_actor_role IN ('owner', 'admin'));

CREATE INDEX idx_memory_review_audit_source_comment
    ON memory_review_audit(source_comment_id)
    WHERE source_comment_id IS NOT NULL;

CREATE INDEX idx_memory_review_audit_cosign
    ON memory_review_audit(co_sign_comment_id)
    WHERE co_sign_comment_id IS NOT NULL;

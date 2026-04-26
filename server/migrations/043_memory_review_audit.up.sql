ALTER TABLE memory_entry DROP CONSTRAINT IF EXISTS memory_entry_status_check;
ALTER TABLE memory_entry
    ADD CONSTRAINT memory_entry_status_check
    CHECK (status IN ('pending', 'approved', 'rejected', 'revoked'));

ALTER TABLE memory_entry DROP CONSTRAINT IF EXISTS memory_entry_check1;
ALTER TABLE memory_entry
    ADD CONSTRAINT memory_entry_status_timestamp_check
    CHECK (
        (status = 'pending' AND approved_at IS NULL AND rejected_at IS NULL)
        OR (status = 'approved' AND approved_at IS NOT NULL AND rejected_at IS NULL)
        OR (status = 'rejected' AND approved_at IS NULL AND rejected_at IS NOT NULL)
        OR (status = 'revoked' AND approved_at IS NULL AND rejected_at IS NULL)
    );

CREATE TABLE memory_review_audit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    memory_id UUID NOT NULL REFERENCES memory_entry(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    action TEXT NOT NULL CHECK (action IN ('approve', 'reject', 'revoke')),
    previous_status TEXT NOT NULL,
    new_status TEXT NOT NULL,
    actor_type TEXT NOT NULL CHECK (actor_type IN ('member', 'agent')),
    actor_id UUID NOT NULL,
    source_issue_id UUID REFERENCES issue(id) ON DELETE SET NULL,
    guardrail JSONB NOT NULL DEFAULT '{}',
    review_note TEXT NOT NULL,
    request_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_memory_review_audit_memory ON memory_review_audit(memory_id, created_at DESC);
CREATE INDEX idx_memory_review_audit_workspace ON memory_review_audit(workspace_id, created_at DESC);
CREATE INDEX idx_memory_review_audit_actor ON memory_review_audit(workspace_id, actor_type, actor_id, created_at DESC);

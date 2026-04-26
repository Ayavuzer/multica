package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mustDecodeJSON(t *testing.T, w *httptest.ResponseRecorder, dest any) {
	t.Helper()
	if err := json.NewDecoder(w.Body).Decode(dest); err != nil {
		t.Fatalf("decode response JSON: %v; body=%s", err, w.Body.String())
	}
}

func handlerTestAgentID(t *testing.T) string {
	t.Helper()
	var agentID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT id
		FROM agent
		WHERE workspace_id = $1
		ORDER BY created_at ASC
		LIMIT 1
	`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load handler test agent: %v", err)
	}
	return agentID
}

func TestAgentApproveWorkspaceMemoryWritesAudit(t *testing.T) {
	ctx := context.Background()
	sourceIssueID := createMemorySourceIssue(t, "agent approve workspace source")
	ariaID := createMemoryReviewAgent(t)
	memoryID := createPendingMemory(t, "workspace", nil, sourceIssueID, "member", testUserID, "Workspace operating policy", "Use the workspace handoff policy for agent routing.")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memories/"+memoryID+"/approve", map[string]any{
		"review_note":  "security-kvkk-guard clean; source issue reviewed; workspace policy approved",
		"source_issue": sourceIssueID,
	})
	req.Header.Set("X-Agent-ID", ariaID)
	req.Header.Set("X-Request-ID", "memory-review-test")
	req = withURLParam(req, "id", memoryID)

	testHandler.ApproveMemory(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ApproveMemory: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp MemoryResponse
	mustDecodeJSON(t, w, &resp)
	if resp.Status != "approved" || resp.ReviewedByType == nil || *resp.ReviewedByType != "agent" {
		t.Fatalf("unexpected review response: %+v", resp)
	}
	if resp.AuditID == nil || *resp.AuditID == "" {
		t.Fatalf("expected audit_id in response: %+v", resp)
	}

	var auditCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM memory_review_audit
		WHERE memory_id = $1 AND action = 'approve' AND actor_type = 'agent'
		  AND actor_id = $2 AND source_issue_id = $3 AND request_id = 'memory-review-test'
	`, memoryID, ariaID, sourceIssueID).Scan(&auditCount); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one audit row, got %d", auditCount)
	}

	var commentCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM comment
		WHERE issue_id = $1 AND workspace_id = $2 AND author_type = 'agent' AND author_id = $3
		  AND content LIKE '%' || $4 || '%'
	`, sourceIssueID, testWorkspaceID, ariaID, memoryID).Scan(&commentCount); err != nil {
		t.Fatalf("count review comments: %v", err)
	}
	if commentCount != 1 {
		t.Fatalf("expected one source issue comment, got %d", commentCount)
	}
}

func TestAgentApproveAgentRolePolicyMemory(t *testing.T) {
	sourceIssueID := createMemorySourceIssue(t, "agent scope role boundary source")
	ariaID := createMemoryReviewAgent(t)
	targetAgentID := handlerTestAgentID(t)
	memoryID := createPendingMemory(t, "agent", &targetAgentID, sourceIssueID, "member", testUserID, "Agent role boundary policy", "Agent role boundary: BMad Dev must follow the implementation workflow.")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memories/"+memoryID+"/approve", map[string]any{
		"review_note":  "security-kvkk-guard clean; source issue reviewed; role boundary policy",
		"source_issue": sourceIssueID,
	})
	req.Header.Set("X-Agent-ID", ariaID)
	req = withURLParam(req, "id", memoryID)

	testHandler.ApproveMemory(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ApproveMemory agent-scope role policy: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGeminiApproveARIAProposedWorkspaceMemory(t *testing.T) {
	ctx := context.Background()
	sourceIssueID := createMemorySourceIssue(t, "gemini approve aria workspace source")
	ariaID := createMemoryReviewAgentWithID(t, memoryReviewARIAAgentID, "ARIA Orchestrator")
	geminiID := createMemoryReviewAgentWithID(t, memoryReviewGeminiAgentID, "Gemini")
	memoryID := createPendingMemory(t, "workspace", nil, sourceIssueID, "agent", ariaID, "Multica workspace operating policy", "Agents must use structured Multica CLI reads with --output json.")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memories/"+memoryID+"/approve", map[string]any{
		"review_note":  "security-kvkk-guard clean; Gemini independent review; workspace policy approved",
		"source_issue": sourceIssueID,
	})
	req.Header.Set("X-Agent-ID", geminiID)
	req.Header.Set("X-Request-ID", "gemini-workspace-review-test")
	req = withURLParam(req, "id", memoryID)

	testHandler.ApproveMemory(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Gemini ApproveMemory workspace: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp MemoryResponse
	mustDecodeJSON(t, w, &resp)
	if resp.Status != "approved" || resp.ReviewedByID == nil || *resp.ReviewedByID != geminiID {
		t.Fatalf("unexpected Gemini review response: %+v", resp)
	}

	var auditCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM memory_review_audit
		WHERE memory_id = $1 AND action = 'approve' AND actor_type = 'agent'
		  AND actor_id = $2 AND source_issue_id = $3 AND request_id = 'gemini-workspace-review-test'
	`, memoryID, geminiID, sourceIssueID).Scan(&auditCount); err != nil {
		t.Fatalf("count Gemini audit rows: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one Gemini audit row, got %d", auditCount)
	}
}

func TestGeminiApproveARIAProposedAgentRolePolicyMemory(t *testing.T) {
	sourceIssueID := createMemorySourceIssue(t, "gemini approve aria agent policy source")
	ariaID := createMemoryReviewAgentWithID(t, memoryReviewARIAAgentID, "ARIA Orchestrator")
	geminiID := createMemoryReviewAgentWithID(t, memoryReviewGeminiAgentID, "Gemini")
	targetAgentID := geminiID
	memoryID := createPendingMemory(t, "agent", &targetAgentID, sourceIssueID, "agent", ariaID, "Gemini analysis agent role", "Agent role boundary: Gemini performs deep research, reports with primary sources, and does not edit repository files directly.")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memories/"+memoryID+"/approve", map[string]any{
		"review_note":  "security-kvkk-guard clean; Gemini independent review; Gemini role boundary approved",
		"source_issue": sourceIssueID,
	})
	req.Header.Set("X-Agent-ID", geminiID)
	req = withURLParam(req, "id", memoryID)

	testHandler.ApproveMemory(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Gemini ApproveMemory agent role policy: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAgentApproveOwnWorkspaceMemoryDeniedWithOwnerCosign(t *testing.T) {
	sourceIssueID := createMemorySourceIssue(t, "owner cosign workspace source")
	ariaID := createMemoryReviewAgentWithID(t, memoryReviewARIAAgentID, "ARIA Orchestrator")
	sourceCommentID := createMemoryReviewComment(t, sourceIssueID, "member", testUserID, "Owner co-sign: approve this clean workspace memory.")
	memoryID := createPendingMemory(t, "workspace", nil, sourceIssueID, "agent", ariaID, "Workspace operating policy", "Use explicit source issues for memory review decisions.")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memories/"+memoryID+"/approve", map[string]any{
		"review_note":    "security-kvkk-guard clean; owner co-sign verified",
		"source_issue":   sourceIssueID,
		"source_comment": sourceCommentID,
	})
	req.Header.Set("X-Agent-ID", ariaID)
	req = withURLParam(req, "id", memoryID)

	testHandler.ApproveMemory(w, req)
	assertJSONError(t, w, http.StatusForbidden, "self_approval_denied")
}

func TestAgentApproveOwnAgentPolicyMemoryDeniedWithOwnerCosign(t *testing.T) {
	sourceIssueID := createMemorySourceIssue(t, "owner cosign agent policy source")
	ariaID := createMemoryReviewAgentWithID(t, memoryReviewARIAAgentID, "ARIA Orchestrator")
	targetAgentID := ariaID
	sourceCommentID := createMemoryReviewComment(t, sourceIssueID, "member", testUserID, "Owner co-sign: approve this agent role boundary memory.")
	memoryID := createPendingMemory(t, "agent", &targetAgentID, sourceIssueID, "agent", ariaID, "Agent role boundary policy", "Agent role boundary: BMad Dev must follow assigned issue workflow.")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memories/"+memoryID+"/approve", map[string]any{
		"review_note":    "security-kvkk-guard clean; owner co-sign verified; role boundary policy",
		"source_issue":   sourceIssueID,
		"source_comment": sourceCommentID,
	})
	req.Header.Set("X-Agent-ID", ariaID)
	req = withURLParam(req, "id", memoryID)

	testHandler.ApproveMemory(w, req)
	assertJSONError(t, w, http.StatusForbidden, "self_approval_denied")
}

func TestAgentApproveHumanOnlyScopeDenied(t *testing.T) {
	sourceIssueID := createMemorySourceIssue(t, "human only scope source")
	ariaID := createMemoryReviewAgent(t)
	scopeID := sourceIssueID
	memoryID := createPendingMemory(t, "issue", &scopeID, sourceIssueID, "member", testUserID, "Issue-specific memory", "This issue-specific memory requires a human reviewer.")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memories/"+memoryID+"/approve", map[string]any{
		"review_note":  "security-kvkk-guard clean; source issue reviewed; attempting issue scope",
		"source_issue": sourceIssueID,
	})
	req.Header.Set("X-Agent-ID", ariaID)
	req = withURLParam(req, "id", memoryID)

	testHandler.ApproveMemory(w, req)
	assertJSONError(t, w, http.StatusForbidden, "human_only_scope")
}

func TestAgentApproveSelfApprovalDenied(t *testing.T) {
	sourceIssueID := createMemorySourceIssue(t, "self approval source")
	ariaID := createMemoryReviewAgentWithID(t, memoryReviewARIAAgentID, "ARIA Orchestrator")
	memoryID := createPendingMemory(t, "workspace", nil, sourceIssueID, "agent", ariaID, "Workspace operating policy", "Use source issues for every memory review.")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memories/"+memoryID+"/approve", map[string]any{
		"review_note":  "security-kvkk-guard clean; source issue reviewed; self approval check",
		"source_issue": sourceIssueID,
	})
	req.Header.Set("X-Agent-ID", ariaID)
	req = withURLParam(req, "id", memoryID)

	testHandler.ApproveMemory(w, req)
	assertJSONError(t, w, http.StatusForbidden, "self_approval_denied")
}

func TestAgentApproveOwnMemoryWithNonOwnerCosignDenied(t *testing.T) {
	sourceIssueID := createMemorySourceIssue(t, "non owner cosign source")
	ariaID := createMemoryReviewAgentWithID(t, memoryReviewARIAAgentID, "ARIA Orchestrator")
	memberID := createWorkspaceMember(t, "member")
	sourceCommentID := createMemoryReviewComment(t, sourceIssueID, "member", memberID, "Looks fine to me.")
	memoryID := createPendingMemory(t, "workspace", nil, sourceIssueID, "agent", ariaID, "Workspace operating policy", "Use clean source issue policy for memory review.")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memories/"+memoryID+"/approve", map[string]any{
		"review_note":    "security-kvkk-guard clean; non-owner co-sign should fail",
		"source_issue":   sourceIssueID,
		"source_comment": sourceCommentID,
	})
	req.Header.Set("X-Agent-ID", ariaID)
	req = withURLParam(req, "id", memoryID)

	testHandler.ApproveMemory(w, req)
	assertJSONError(t, w, http.StatusForbidden, "self_approval_denied")
}

func TestMemoryReviewRequiresSourceIssue(t *testing.T) {
	sourceIssueID := createMemorySourceIssue(t, "missing review source issue")
	ariaID := createMemoryReviewAgent(t)
	memoryID := createPendingMemory(t, "workspace", nil, sourceIssueID, "member", testUserID, "Workspace operating policy", "Use source issues for every memory review.")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memories/"+memoryID+"/approve", map[string]any{
		"review_note": "security-kvkk-guard clean; missing source issue",
	})
	req.Header.Set("X-Agent-ID", ariaID)
	req = withURLParam(req, "id", memoryID)

	testHandler.ApproveMemory(w, req)
	assertJSONError(t, w, http.StatusBadRequest, "source_issue_required")
}

func TestAgentReviewRejectsInvalidTaskContext(t *testing.T) {
	sourceIssueID := createMemorySourceIssue(t, "invalid task context source")
	ariaID := createMemoryReviewAgent(t)
	memoryID := createPendingMemory(t, "workspace", nil, sourceIssueID, "member", testUserID, "Workspace operating policy", "Use source issues for every memory review.")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memories/"+memoryID+"/approve", map[string]any{
		"review_note":  "security-kvkk-guard clean; source issue reviewed; invalid task context",
		"source_issue": sourceIssueID,
	})
	req.Header.Set("X-Agent-ID", ariaID)
	req.Header.Set("X-Task-ID", sourceIssueID)
	req = withURLParam(req, "id", memoryID)

	testHandler.ApproveMemory(w, req)
	assertJSONError(t, w, http.StatusForbidden, "insufficient_permissions")
}

func TestAgentApproveGuardrailFailed(t *testing.T) {
	sourceIssueID := createMemorySourceIssue(t, "guardrail failed source")
	ariaID := createMemoryReviewAgent(t)
	memoryID := createPendingMemory(t, "workspace", nil, sourceIssueID, "member", testUserID, "Workspace contact", "Contact user@example.com for credentials.")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memories/"+memoryID+"/approve", map[string]any{
		"review_note":  "security-kvkk-guard clean; source issue reviewed; should fail guardrail",
		"source_issue": sourceIssueID,
	})
	req.Header.Set("X-Agent-ID", ariaID)
	req = withURLParam(req, "id", memoryID)

	testHandler.ApproveMemory(w, req)
	assertJSONError(t, w, http.StatusBadRequest, "guardrail_failed")
}

func TestAgentReviewRateLimit(t *testing.T) {
	sourceIssueID := createMemorySourceIssue(t, "rate limit source")
	ariaID := createMemoryReviewAgent(t)

	for i := 0; i < memoryReviewBurstLimit; i++ {
		memoryID := createPendingMemory(t, "workspace", nil, sourceIssueID, "member", testUserID, "Workspace operating policy", "Use clean source issue policy for memory review.")
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/memories/"+memoryID+"/approve", map[string]any{
			"review_note":  "security-kvkk-guard clean; source issue reviewed; rate limit setup",
			"source_issue": sourceIssueID,
		})
		req.Header.Set("X-Agent-ID", ariaID)
		req = withURLParam(req, "id", memoryID)
		testHandler.ApproveMemory(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("ApproveMemory setup %d: expected 200, got %d: %s", i, w.Code, w.Body.String())
		}
	}

	memoryID := createPendingMemory(t, "workspace", nil, sourceIssueID, "member", testUserID, "Workspace operating policy", "Use clean source issue policy for memory review.")
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memories/"+memoryID+"/approve", map[string]any{
		"review_note":  "security-kvkk-guard clean; source issue reviewed; should hit rate limit",
		"source_issue": sourceIssueID,
	})
	req.Header.Set("X-Agent-ID", ariaID)
	req = withURLParam(req, "id", memoryID)

	testHandler.ApproveMemory(w, req)
	assertJSONError(t, w, http.StatusTooManyRequests, "rate_limited")
}

func TestOwnerRevokeApprovedMemory(t *testing.T) {
	sourceIssueID := createMemorySourceIssue(t, "owner revoke source")
	memoryID := createPendingMemory(t, "workspace", nil, sourceIssueID, "member", testUserID, "Workspace operating policy", "Use explicit revoke when approved memory is superseded.")
	if _, err := testPool.Exec(context.Background(), `
		UPDATE memory_entry
		SET status = 'approved', reviewed_by_type = 'member', reviewed_by_id = $2,
			review_note = 'initial approval', approved_at = now(), updated_at = now()
		WHERE id = $1
	`, memoryID, testUserID); err != nil {
		t.Fatalf("mark memory approved: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memories/"+memoryID+"/revoke", map[string]any{
		"review_note":  "owner/admin rollback after policy changed",
		"source_issue": sourceIssueID,
	})
	req = withURLParam(req, "id", memoryID)

	testHandler.RevokeMemory(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("RevokeMemory: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp MemoryResponse
	mustDecodeJSON(t, w, &resp)
	if resp.Status != "revoked" {
		t.Fatalf("expected revoked response, got %+v", resp)
	}
}

func createMemorySourceIssue(t *testing.T, title string) string {
	t.Helper()
	ctx := context.Background()
	var number int
	if err := testPool.QueryRow(ctx, `SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1`, testWorkspaceID).Scan(&number); err != nil {
		t.Fatalf("next issue number: %v", err)
	}
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number, position)
		VALUES ($1, $2, 'todo', 'none', 'member', $3, $4, 0)
		RETURNING id
	`, testWorkspaceID, title, testUserID, number).Scan(&issueID); err != nil {
		t.Fatalf("create source issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
	})
	return issueID
}

func createMemoryReviewAgent(t *testing.T) string {
	return createMemoryReviewAgentWithID(t, "", "ARIA Orchestrator")
}

func createMemoryReviewAgentWithID(t *testing.T, agentID, name string) string {
	t.Helper()
	ctx := context.Background()
	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, handlerTestAgentID(t)).Scan(&runtimeID); err != nil {
		t.Fatalf("load runtime: %v", err)
	}
	if strings.TrimSpace(name) == "" {
		name = "Memory Review Agent"
	}
	var createdID string
	created := true
	if strings.TrimSpace(agentID) == "" {
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent (
				workspace_id, name, description, runtime_mode, runtime_config,
				runtime_id, visibility, max_concurrent_tasks, owner_id
			)
			VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 1, $4)
			RETURNING id
		`, testWorkspaceID, name, runtimeID, testUserID).Scan(&createdID); err != nil {
			t.Fatalf("create memory review agent: %v", err)
		}
		t.Setenv(memoryReviewAllowlistEnvKey, createdID)
	} else {
		if err := testPool.QueryRow(ctx, `
			WITH inserted AS (
				INSERT INTO agent (
					id, workspace_id, name, description, runtime_mode, runtime_config,
					runtime_id, visibility, max_concurrent_tasks, owner_id
				)
				VALUES ($1, $2, $3, '', 'cloud', '{}'::jsonb, $4, 'workspace', 1, $5)
				ON CONFLICT (id) DO NOTHING
				RETURNING id, true AS created
			)
			SELECT id, created FROM inserted
			UNION ALL
			SELECT id, false AS created FROM agent WHERE id = $1
			LIMIT 1
		`, agentID, testWorkspaceID, name, runtimeID, testUserID).Scan(&createdID, &created); err != nil {
			t.Fatalf("create memory review agent %s: %v", agentID, err)
		}
	}
	t.Cleanup(func() {
		if created {
			testPool.Exec(ctx, `DELETE FROM memory_review_audit WHERE actor_id = $1`, createdID)
			testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, createdID)
		}
	})
	return createdID
}

func createWorkspaceMember(t *testing.T, role string) string {
	t.Helper()
	ctx := context.Background()
	var userID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ($1, gen_random_uuid()::text || '@handler-test.multica.ai')
		RETURNING id
	`, "Memory Review Member").Scan(&userID); err != nil {
		t.Fatalf("create workspace user: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, $3)
	`, testWorkspaceID, userID, role); err != nil {
		t.Fatalf("create workspace member: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return userID
}

func createMemoryReviewComment(t *testing.T, issueID, authorType, authorID, content string) string {
	t.Helper()
	ctx := context.Background()
	var commentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type)
		VALUES ($1, $2, $3, $4, $5, 'comment')
		RETURNING id
	`, issueID, testWorkspaceID, authorType, authorID, content).Scan(&commentID); err != nil {
		t.Fatalf("create memory review comment: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM comment WHERE id = $1`, commentID)
	})
	return commentID
}

func createPendingMemory(t *testing.T, scopeType string, scopeID *string, sourceIssueID, proposedByType, proposedByID, title, content string) string {
	t.Helper()
	ctx := context.Background()
	var scopeArg any
	if scopeID != nil {
		scopeArg = *scopeID
	}
	var memoryID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO memory_entry (
			workspace_id, scope_type, scope_id, title, content,
			source_issue_id, proposed_by_type, proposed_by_id, guardrail
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, '{"allowed":true,"version":"test"}'::jsonb)
		RETURNING id
	`, testWorkspaceID, scopeType, scopeArg, title, content, sourceIssueID, proposedByType, proposedByID).Scan(&memoryID); err != nil {
		t.Fatalf("create pending memory: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM memory_entry WHERE id = $1`, memoryID)
	})
	return memoryID
}

func assertJSONError(t *testing.T, w *httptest.ResponseRecorder, status int, errText string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("expected status %d, got %d: %s", status, w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if got := strings.TrimSpace(body["error"].(string)); got != errText {
		t.Fatalf("expected error %q, got %q", errText, got)
	}
}

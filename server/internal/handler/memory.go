package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/memoryguard"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	memoryMaxTitleRunes   = 160
	memoryMaxContentRunes = 4000
	memoryDefaultLimit    = 50
	memoryMaxLimit        = 200

	memoryReviewARIAAgentID     = "06df6f38-8533-4428-a71a-373d905c8eba"
	memoryReviewGeminiAgentID   = "4092769e-9f15-4a15-810e-6e1f67528e76"
	memoryReviewAllowlistEnvKey = "MULTICA_MEMORY_REVIEW_AGENT_ALLOWLIST"
	memoryReviewHourlyLimit     = 20
	memoryReviewBurstLimit      = 5
)

var memoryReviewDefaultAgentAllowlist = map[string]struct{}{
	memoryReviewARIAAgentID:   {},
	memoryReviewGeminiAgentID: {},
}

const memoryColumns = `
	id, workspace_id, scope_type, scope_id, title, content, status,
	source_issue_id, source_comment_id,
	proposed_by_type, proposed_by_id,
	reviewed_by_type, reviewed_by_id, review_note, guardrail,
	approved_at, rejected_at, created_at, updated_at
`

type memoryEntry struct {
	ID              pgtype.UUID
	WorkspaceID     pgtype.UUID
	ScopeType       string
	ScopeID         pgtype.UUID
	Title           string
	Content         string
	Status          string
	SourceIssueID   pgtype.UUID
	SourceCommentID pgtype.UUID
	ProposedByType  string
	ProposedByID    pgtype.UUID
	ReviewedByType  pgtype.Text
	ReviewedByID    pgtype.UUID
	ReviewNote      pgtype.Text
	Guardrail       []byte
	ApprovedAt      pgtype.Timestamptz
	RejectedAt      pgtype.Timestamptz
	CreatedAt       pgtype.Timestamptz
	UpdatedAt       pgtype.Timestamptz
}

type MemoryResponse struct {
	ID              string  `json:"id"`
	WorkspaceID     string  `json:"workspace_id"`
	ScopeType       string  `json:"scope_type"`
	ScopeID         *string `json:"scope_id"`
	Title           string  `json:"title"`
	Content         string  `json:"content"`
	Status          string  `json:"status"`
	SourceIssueID   *string `json:"source_issue_id"`
	SourceCommentID *string `json:"source_comment_id"`
	ProposedByType  string  `json:"proposed_by_type"`
	ProposedByID    string  `json:"proposed_by_id"`
	ReviewedByType  *string `json:"reviewed_by_type"`
	ReviewedByID    *string `json:"reviewed_by_id"`
	ReviewNote      *string `json:"review_note"`
	Guardrail       any     `json:"guardrail"`
	ApprovedAt      *string `json:"approved_at"`
	RejectedAt      *string `json:"rejected_at"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	AuditID         *string `json:"audit_id,omitempty"`
}

// MemoryContextData is the approved memory subset sent to daemon runtimes.
type MemoryContextData struct {
	ID              string  `json:"id"`
	ScopeType       string  `json:"scope_type"`
	ScopeID         *string `json:"scope_id,omitempty"`
	Title           string  `json:"title"`
	Content         string  `json:"content"`
	SourceIssueID   *string `json:"source_issue_id,omitempty"`
	SourceCommentID *string `json:"source_comment_id,omitempty"`
}

type ProposeMemoryRequest struct {
	ScopeType       string  `json:"scope_type"`
	ScopeID         *string `json:"scope_id"`
	Title           string  `json:"title"`
	Content         string  `json:"content"`
	SourceIssueID   *string `json:"source_issue_id"`
	SourceCommentID *string `json:"source_comment_id"`
}

type reviewMemoryRequest struct {
	ReviewNote      string `json:"review_note"`
	Note            string `json:"note"`
	SourceIssue     string `json:"source_issue"`
	SourceIssueID   string `json:"source_issue_id"`
	SourceComment   string `json:"source_comment"`
	SourceCommentID string `json:"source_comment_id"`
}

type memoryReviewActor struct {
	Type string
	ID   string
	UUID pgtype.UUID
}

type memoryReviewSource struct {
	IssueID   pgtype.UUID
	CommentID pgtype.UUID
}

type memoryOwnerCoSign struct {
	CommentID pgtype.UUID
	ActorID   pgtype.UUID
	Role      string
}

type memoryScanner interface {
	Scan(dest ...any) error
}

func scanMemoryEntry(s memoryScanner) (memoryEntry, error) {
	var entry memoryEntry
	err := s.Scan(
		&entry.ID,
		&entry.WorkspaceID,
		&entry.ScopeType,
		&entry.ScopeID,
		&entry.Title,
		&entry.Content,
		&entry.Status,
		&entry.SourceIssueID,
		&entry.SourceCommentID,
		&entry.ProposedByType,
		&entry.ProposedByID,
		&entry.ReviewedByType,
		&entry.ReviewedByID,
		&entry.ReviewNote,
		&entry.Guardrail,
		&entry.ApprovedAt,
		&entry.RejectedAt,
		&entry.CreatedAt,
		&entry.UpdatedAt,
	)
	return entry, err
}

func memoryToResponse(entry memoryEntry) MemoryResponse {
	guardrail := any(map[string]any{})
	if len(entry.Guardrail) > 0 {
		_ = json.Unmarshal(entry.Guardrail, &guardrail)
	}
	return MemoryResponse{
		ID:              uuidToString(entry.ID),
		WorkspaceID:     uuidToString(entry.WorkspaceID),
		ScopeType:       entry.ScopeType,
		ScopeID:         uuidToPtr(entry.ScopeID),
		Title:           entry.Title,
		Content:         entry.Content,
		Status:          entry.Status,
		SourceIssueID:   uuidToPtr(entry.SourceIssueID),
		SourceCommentID: uuidToPtr(entry.SourceCommentID),
		ProposedByType:  entry.ProposedByType,
		ProposedByID:    uuidToString(entry.ProposedByID),
		ReviewedByType:  textToPtr(entry.ReviewedByType),
		ReviewedByID:    uuidToPtr(entry.ReviewedByID),
		ReviewNote:      textToPtr(entry.ReviewNote),
		Guardrail:       guardrail,
		ApprovedAt:      timestampToPtr(entry.ApprovedAt),
		RejectedAt:      timestampToPtr(entry.RejectedAt),
		CreatedAt:       timestampToString(entry.CreatedAt),
		UpdatedAt:       timestampToString(entry.UpdatedAt),
	}
}

func memoryToContext(entry memoryEntry) MemoryContextData {
	return MemoryContextData{
		ID:              uuidToString(entry.ID),
		ScopeType:       entry.ScopeType,
		ScopeID:         uuidToPtr(entry.ScopeID),
		Title:           entry.Title,
		Content:         entry.Content,
		SourceIssueID:   uuidToPtr(entry.SourceIssueID),
		SourceCommentID: uuidToPtr(entry.SourceCommentID),
	}
}

func (h *Handler) ListMemories(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	query, args, err := buildMemoryListQuery(workspaceID, r, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := h.queryMemories(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list memories")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"memories": memoryEntriesToResponses(entries),
		"total":    len(entries),
	})
}

func (h *Handler) SearchMemories(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	query, args, err := buildMemoryListQuery(workspaceID, r, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := h.queryMemories(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to search memories")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"memories": memoryEntriesToResponses(entries),
		"total":    len(entries),
	})
}

func (h *Handler) GetMemory(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	entry, err := h.getMemoryInWorkspace(r.Context(), chi.URLParam(r, "id"), workspaceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	}
	writeJSON(w, http.StatusOK, memoryToResponse(entry))
}

func (h *Handler) ProposeMemory(w http.ResponseWriter, r *http.Request) {
	var req ProposeMemoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.ScopeType = strings.TrimSpace(req.ScopeType)
	if req.ScopeType == "" {
		req.ScopeType = "workspace"
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Content = strings.TrimSpace(req.Content)
	if err := validateMemoryText(req.Title, req.Content); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	report := memoryguard.Inspect(req.Title, req.Content)
	if !report.Allowed {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":    "memory content failed guardrails",
			"findings": memoryguard.FindingTypes(report.Findings),
		})
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	scopeID, ok := h.validateMemoryScope(w, r, workspaceID, req.ScopeType, req.ScopeID)
	if !ok {
		return
	}
	sourceIssueID, sourceCommentID, ok := h.validateMemorySourceRefs(w, r, workspaceID, req.SourceIssueID, req.SourceCommentID)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	guardrail, _ := json.Marshal(report)

	entry, err := scanMemoryEntry(h.DB.QueryRow(r.Context(), `
		INSERT INTO memory_entry (
			workspace_id, scope_type, scope_id, title, content,
			source_issue_id, source_comment_id,
			proposed_by_type, proposed_by_id, guardrail
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+memoryColumns,
		parseUUID(workspaceID),
		req.ScopeType,
		scopeID,
		req.Title,
		req.Content,
		sourceIssueID,
		sourceCommentID,
		actorType,
		parseUUID(actorID),
		guardrail,
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to propose memory")
		return
	}
	writeJSON(w, http.StatusCreated, memoryToResponse(entry))
}

func (h *Handler) ApproveMemory(w http.ResponseWriter, r *http.Request) {
	h.reviewMemory(w, r, "approved")
}

func (h *Handler) RejectMemory(w http.ResponseWriter, r *http.Request) {
	h.reviewMemory(w, r, "rejected")
}

func (h *Handler) RevokeMemory(w http.ResponseWriter, r *http.Request) {
	h.revokeMemory(w, r)
}

func (h *Handler) reviewMemory(w http.ResponseWriter, r *http.Request, status string) {
	workspaceID := h.resolveWorkspaceID(r)
	actor, ok := h.resolveMemoryReviewActor(w, r, workspaceID)
	if !ok {
		return
	}
	req, ok := decodeReviewMemoryRequest(w, r)
	if !ok {
		return
	}
	current, err := h.getMemoryInWorkspace(r.Context(), chi.URLParam(r, "id"), workspaceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	}
	if current.Status != "pending" {
		writeError(w, http.StatusConflict, "memory_not_pending")
		return
	}
	reviewNote := req.canonicalReviewNote()
	if reviewNote == "" {
		writeError(w, http.StatusBadRequest, "review_note_required")
		return
	}
	source, ok := h.resolveReviewSource(w, r, workspaceID, req)
	if !ok {
		return
	}
	report := memoryguard.Inspect(current.Title, current.Content)
	if status == "approved" && !report.Allowed {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":    "guardrail_failed",
			"findings": memoryguard.FindingTypes(report.Findings),
		})
		return
	}
	var coSign *memoryOwnerCoSign
	if actor.Type == "agent" {
		var errMsg string
		coSign, errMsg = h.validateAgentMemoryReview(r, workspaceID, actor, current, status, source)
		if errMsg != "" {
			statusCode := http.StatusForbidden
			if errMsg == "rate_limited" {
				statusCode = http.StatusTooManyRequests
			}
			writeError(w, statusCode, errMsg)
			return
		}
	}
	guardrail, _ := json.Marshal(report)

	entry, auditID, err := h.persistMemoryReview(r.Context(), current, workspaceID, actor, status, source, coSign, reviewNote, guardrail, r.Header.Get("X-Request-ID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to review memory")
		return
	}
	resp := memoryToResponse(entry)
	resp.AuditID = &auditID
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) revokeMemory(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	if !ok {
		return
	}
	req, ok := decodeReviewMemoryRequest(w, r)
	if !ok {
		return
	}
	current, err := h.getMemoryInWorkspace(r.Context(), chi.URLParam(r, "id"), workspaceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	}
	if current.Status != "approved" {
		writeError(w, http.StatusConflict, "memory_not_approved")
		return
	}
	reviewNote := req.canonicalReviewNote()
	if reviewNote == "" {
		writeError(w, http.StatusBadRequest, "review_note_required")
		return
	}
	source, ok := h.resolveReviewSource(w, r, workspaceID, req)
	if !ok {
		return
	}
	report := memoryguard.Inspect(current.Title, current.Content)
	guardrail, _ := json.Marshal(report)
	actor := memoryReviewActor{
		Type: "member",
		ID:   uuidToString(member.UserID),
		UUID: member.UserID,
	}
	entry, auditID, err := h.persistMemoryRevoke(r.Context(), current, workspaceID, actor, source, reviewNote, guardrail, r.Header.Get("X-Request-ID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke memory")
		return
	}
	resp := memoryToResponse(entry)
	resp.AuditID = &auditID
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) loadApprovedMemoryContext(ctx context.Context, workspaceID string, agentID pgtype.UUID, issue *db.Issue) []MemoryContextData {
	if workspaceID == "" || h.DB == nil {
		return nil
	}

	args := []any{parseUUID(workspaceID), agentID}
	conditions := []string{
		"(scope_type = 'workspace' AND scope_id IS NULL)",
		"(scope_type = 'agent' AND scope_id = $2)",
	}
	if issue != nil {
		args = append(args, issue.ID)
		conditions = append(conditions, fmt.Sprintf("(scope_type = 'issue' AND scope_id = $%d)", len(args)))
		if issue.ProjectID.Valid {
			args = append(args, issue.ProjectID)
			conditions = append(conditions, fmt.Sprintf("(scope_type = 'project' AND scope_id = $%d)", len(args)))
		}
	}

	query := `
		SELECT ` + memoryColumns + `
		FROM memory_entry
		WHERE workspace_id = $1
		  AND status = 'approved'
		  AND (` + strings.Join(conditions, " OR ") + `)
		ORDER BY
			CASE scope_type
				WHEN 'issue' THEN 1
				WHEN 'project' THEN 2
				WHEN 'agent' THEN 3
				ELSE 4
			END,
			updated_at DESC
		LIMIT 20`

	entries, err := h.queryMemories(ctx, query, args...)
	if err != nil {
		return nil
	}
	memories := make([]MemoryContextData, 0, len(entries))
	for _, entry := range entries {
		if !memoryguard.Inspect(entry.Title, entry.Content).Allowed {
			continue
		}
		memories = append(memories, memoryToContext(entry))
	}
	return memories
}

func (h *Handler) queryMemories(ctx context.Context, query string, args ...any) ([]memoryEntry, error) {
	rows, err := h.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []memoryEntry
	for rows.Next() {
		entry, err := scanMemoryEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (h *Handler) getMemoryInWorkspace(ctx context.Context, memoryID, workspaceID string) (memoryEntry, error) {
	entry, err := scanMemoryEntry(h.DB.QueryRow(ctx, `
		SELECT `+memoryColumns+`
		FROM memory_entry
		WHERE id = $1 AND workspace_id = $2`,
		parseUUID(memoryID),
		parseUUID(workspaceID),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return memoryEntry{}, err
	}
	return entry, err
}

func (h *Handler) resolveMemoryReviewActor(w http.ResponseWriter, r *http.Request, workspaceID string) (memoryReviewActor, bool) {
	if _, ok := requireUserID(w, r); !ok {
		return memoryReviewActor{}, false
	}
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return memoryReviewActor{}, false
	}

	agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	if agentID == "" {
		member, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
		if !ok {
			return memoryReviewActor{}, false
		}
		return memoryReviewActor{Type: "member", ID: uuidToString(member.UserID), UUID: member.UserID}, true
	}

	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          parseUUID(agentID),
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		writeError(w, http.StatusForbidden, "insufficient_permissions")
		return memoryReviewActor{}, false
	}
	if !memoryReviewAgentAllowed(agent) {
		writeError(w, http.StatusForbidden, "insufficient_permissions")
		return memoryReviewActor{}, false
	}
	if taskID := strings.TrimSpace(r.Header.Get("X-Task-ID")); taskID != "" {
		task, err := h.Queries.GetAgentTask(r.Context(), parseUUID(taskID))
		if err != nil || uuidToString(task.AgentID) != agentID {
			writeError(w, http.StatusForbidden, "insufficient_permissions")
			return memoryReviewActor{}, false
		}
	}

	return memoryReviewActor{Type: "agent", ID: agentID, UUID: agent.ID}, true
}

func memoryReviewAgentAllowed(agent db.Agent) bool {
	agentID := uuidToString(agent.ID)
	if _, ok := memoryReviewDefaultAgentAllowlist[agentID]; ok {
		return true
	}
	for _, allowedID := range strings.Split(os.Getenv(memoryReviewAllowlistEnvKey), ",") {
		if strings.TrimSpace(allowedID) == agentID {
			return true
		}
	}
	return false
}

func (h *Handler) resolveReviewSource(w http.ResponseWriter, r *http.Request, workspaceID string, req reviewMemoryRequest) (memoryReviewSource, bool) {
	ref := req.canonicalSourceIssue()
	if ref == "" {
		writeError(w, http.StatusBadRequest, "source_issue_required")
		return memoryReviewSource{}, false
	}
	issue, ok := h.findIssueRefInWorkspace(r.Context(), ref, workspaceID)
	if !ok {
		writeError(w, http.StatusNotFound, "source_issue_not_found")
		return memoryReviewSource{}, false
	}

	source := memoryReviewSource{IssueID: issue.ID}
	if commentRef := req.canonicalSourceComment(); commentRef != "" {
		comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
			ID:          parseUUID(commentRef),
			WorkspaceID: parseUUID(workspaceID),
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "source_comment_not_found")
			return memoryReviewSource{}, false
		}
		if uuidToString(comment.IssueID) != uuidToString(issue.ID) {
			writeError(w, http.StatusBadRequest, "source_comment_id does not belong to source_issue_id")
			return memoryReviewSource{}, false
		}
		source.CommentID = comment.ID
	}

	return source, true
}

func (h *Handler) validateAgentMemoryReview(r *http.Request, workspaceID string, actor memoryReviewActor, current memoryEntry, status string, source memoryReviewSource) (*memoryOwnerCoSign, string) {
	if h.agentMemoryReviewRateLimited(r.Context(), workspaceID, actor.UUID) {
		return nil, "rate_limited"
	}
	if current.ProposedByType == actor.Type && uuidToString(current.ProposedByID) == actor.ID {
		return nil, "self_approval_denied"
	}
	if status != "approved" {
		return nil, ""
	}

	switch current.ScopeType {
	case "workspace":
	case "agent":
		if !isAgentRolePolicyMemory(current) {
			return nil, "human_only_scope"
		}
	default:
		return nil, "human_only_scope"
	}

	return nil, ""
}

func (h *Handler) validateOwnerCoSign(ctx context.Context, workspaceID string, source memoryReviewSource) (*memoryOwnerCoSign, string) {
	if !source.CommentID.Valid {
		return nil, "self_approval_denied"
	}
	comment, err := h.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
		ID:          source.CommentID,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil || uuidToString(comment.IssueID) != uuidToString(source.IssueID) {
		return nil, "owner_cosign_required"
	}
	if comment.AuthorType != "member" {
		return nil, "owner_cosign_required"
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      comment.AuthorID,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil || !roleAllowed(member.Role, "owner", "admin") {
		return nil, "owner_cosign_required"
	}
	return &memoryOwnerCoSign{
		CommentID: comment.ID,
		ActorID:   comment.AuthorID,
		Role:      member.Role,
	}, ""
}

func isAgentRolePolicyMemory(entry memoryEntry) bool {
	text := strings.ToLower(entry.Title + "\n" + entry.Content)
	markers := []string{
		"agent role",
		"role boundary",
		"role policy",
		"operating policy",
		"operational policy",
	}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (h *Handler) agentMemoryReviewRateLimited(ctx context.Context, workspaceID string, agentID pgtype.UUID) bool {
	var hourly, burst int
	err := h.DB.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE created_at >= now() - interval '1 hour')::int,
			count(*) FILTER (WHERE created_at >= now() - interval '1 minute')::int
		FROM memory_review_audit
		WHERE workspace_id = $1
		  AND actor_type = 'agent'
		  AND actor_id = $2
		  AND action IN ('approve', 'reject')
	`, parseUUID(workspaceID), agentID).Scan(&hourly, &burst)
	if err != nil {
		return true
	}
	return hourly >= memoryReviewHourlyLimit || burst >= memoryReviewBurstLimit
}

func (h *Handler) persistMemoryReview(ctx context.Context, current memoryEntry, workspaceID string, actor memoryReviewActor, status string, source memoryReviewSource, coSign *memoryOwnerCoSign, reviewNote string, guardrail []byte, requestID string) (memoryEntry, string, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return memoryEntry{}, "", err
	}
	defer tx.Rollback(ctx)

	query := `
		UPDATE memory_entry
		SET status = $3,
			reviewed_by_type = $4,
			reviewed_by_id = $5,
			review_note = $6,
			guardrail = $7,
			approved_at = CASE WHEN $3 = 'approved' THEN now() ELSE NULL END,
			rejected_at = CASE WHEN $3 = 'rejected' THEN now() ELSE NULL END,
			updated_at = now()
		WHERE id = $1 AND workspace_id = $2 AND status = 'pending'
		RETURNING ` + memoryColumns
	entry, err := scanMemoryEntry(tx.QueryRow(
		ctx,
		query,
		current.ID,
		parseUUID(workspaceID),
		status,
		actor.Type,
		actor.UUID,
		strToText(reviewNote),
		guardrail,
	))
	if err != nil {
		return memoryEntry{}, "", err
	}
	action := "reject"
	if status == "approved" {
		action = "approve"
	}
	auditID, err := insertMemoryReviewAudit(ctx, tx, current, action, status, actor, source, coSign, reviewNote, guardrail, requestID)
	if err != nil {
		return memoryEntry{}, "", err
	}
	if err := insertMemoryReviewComment(ctx, tx, current, workspaceID, action, actor, source.IssueID, reviewNote); err != nil {
		return memoryEntry{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return memoryEntry{}, "", err
	}
	return entry, auditID, nil
}

func (h *Handler) persistMemoryRevoke(ctx context.Context, current memoryEntry, workspaceID string, actor memoryReviewActor, source memoryReviewSource, reviewNote string, guardrail []byte, requestID string) (memoryEntry, string, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return memoryEntry{}, "", err
	}
	defer tx.Rollback(ctx)

	entry, err := scanMemoryEntry(tx.QueryRow(
		ctx,
		`
			UPDATE memory_entry
			SET status = 'revoked',
				reviewed_by_type = $3,
				reviewed_by_id = $4,
				review_note = $5,
				guardrail = $6,
				approved_at = NULL,
				rejected_at = NULL,
				updated_at = now()
			WHERE id = $1 AND workspace_id = $2 AND status = 'approved'
			RETURNING `+memoryColumns,
		current.ID,
		parseUUID(workspaceID),
		actor.Type,
		actor.UUID,
		strToText(reviewNote),
		guardrail,
	))
	if err != nil {
		return memoryEntry{}, "", err
	}
	auditID, err := insertMemoryReviewAudit(ctx, tx, current, "revoke", "revoked", actor, source, nil, reviewNote, guardrail, requestID)
	if err != nil {
		return memoryEntry{}, "", err
	}
	if err := insertMemoryReviewComment(ctx, tx, current, workspaceID, "revoke", actor, source.IssueID, reviewNote); err != nil {
		return memoryEntry{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return memoryEntry{}, "", err
	}
	return entry, auditID, nil
}

func insertMemoryReviewAudit(ctx context.Context, exec dbExecutor, current memoryEntry, action, newStatus string, actor memoryReviewActor, source memoryReviewSource, coSign *memoryOwnerCoSign, reviewNote string, guardrail []byte, requestID string) (string, error) {
	var coSignCommentID pgtype.UUID
	var coSignActorType pgtype.Text
	var coSignActorID pgtype.UUID
	var coSignActorRole pgtype.Text
	if coSign != nil {
		coSignCommentID = coSign.CommentID
		coSignActorType = strToText("member")
		coSignActorID = coSign.ActorID
		coSignActorRole = strToText(coSign.Role)
	}

	var auditID string
	err := exec.QueryRow(ctx, `
		INSERT INTO memory_review_audit (
			memory_id, workspace_id, action, previous_status, new_status,
			actor_type, actor_id, source_issue_id, source_comment_id,
			guardrail, review_note, request_id,
			co_sign_comment_id, co_sign_actor_type, co_sign_actor_id, co_sign_actor_role
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING id
	`,
		current.ID,
		current.WorkspaceID,
		action,
		current.Status,
		newStatus,
		actor.Type,
		actor.UUID,
		source.IssueID,
		source.CommentID,
		guardrail,
		reviewNote,
		strToText(strings.TrimSpace(requestID)),
		coSignCommentID,
		coSignActorType,
		coSignActorID,
		coSignActorRole,
	).Scan(&auditID)
	return auditID, err
}

func insertMemoryReviewComment(ctx context.Context, exec dbExecutor, current memoryEntry, workspaceID, action string, actor memoryReviewActor, sourceIssueID pgtype.UUID, reviewNote string) error {
	content := fmt.Sprintf("Memory %s %s. Review note: %s", uuidToString(current.ID), action, reviewNote)
	_, err := exec.Exec(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type)
		VALUES ($1, $2, $3, $4, $5, 'system')
	`, sourceIssueID, parseUUID(workspaceID), actor.Type, actor.UUID, content)
	return err
}

func memoryEntriesToResponses(entries []memoryEntry) []MemoryResponse {
	resp := make([]MemoryResponse, len(entries))
	for i, entry := range entries {
		resp[i] = memoryToResponse(entry)
	}
	return resp
}

func buildMemoryListQuery(workspaceID string, r *http.Request, includeSearch bool) (string, []any, error) {
	args := []any{parseUUID(workspaceID)}
	clauses := []string{"workspace_id = $1"}

	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		if !validMemoryStatus(status) {
			return "", nil, fmt.Errorf("invalid status %q", status)
		}
		args = append(args, status)
		clauses = append(clauses, fmt.Sprintf("status = $%d", len(args)))
	}
	if scopeType := strings.TrimSpace(r.URL.Query().Get("scope_type")); scopeType != "" {
		if !validMemoryScopeType(scopeType) {
			return "", nil, fmt.Errorf("invalid scope_type %q", scopeType)
		}
		args = append(args, scopeType)
		clauses = append(clauses, fmt.Sprintf("scope_type = $%d", len(args)))
	}
	if scopeID := strings.TrimSpace(r.URL.Query().Get("scope_id")); scopeID != "" {
		args = append(args, parseUUID(scopeID))
		clauses = append(clauses, fmt.Sprintf("scope_id = $%d", len(args)))
	}
	if includeSearch {
		pattern := "%" + escapeLike(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))) + "%"
		args = append(args, pattern)
		clauses = append(clauses, fmt.Sprintf("(LOWER(title) LIKE $%d ESCAPE '\\' OR LOWER(content) LIKE $%d ESCAPE '\\')", len(args), len(args)))
	}

	limit, err := intQueryParam(r, "limit", memoryDefaultLimit, memoryMaxLimit)
	if err != nil {
		return "", nil, err
	}
	offset, err := intQueryParam(r, "offset", 0, 100000)
	if err != nil {
		return "", nil, err
	}
	args = append(args, limit)
	limitRef := fmt.Sprintf("$%d", len(args))
	args = append(args, offset)
	offsetRef := fmt.Sprintf("$%d", len(args))

	query := `
		SELECT ` + memoryColumns + `
		FROM memory_entry
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY updated_at DESC
		LIMIT ` + limitRef + ` OFFSET ` + offsetRef
	return query, args, nil
}

func intQueryParam(r *http.Request, name string, defaultValue, maxValue int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	if value > maxValue {
		return maxValue, nil
	}
	return value, nil
}

func validateMemoryText(title, content string) error {
	if title == "" {
		return fmt.Errorf("title is required")
	}
	if content == "" {
		return fmt.Errorf("content is required")
	}
	if utf8.RuneCountInString(title) > memoryMaxTitleRunes {
		return fmt.Errorf("title must be at most %d characters", memoryMaxTitleRunes)
	}
	if utf8.RuneCountInString(content) > memoryMaxContentRunes {
		return fmt.Errorf("content must be at most %d characters", memoryMaxContentRunes)
	}
	return nil
}

func (h *Handler) validateMemoryScope(w http.ResponseWriter, r *http.Request, workspaceID, scopeType string, scopeID *string) (pgtype.UUID, bool) {
	if !validMemoryScopeType(scopeType) {
		writeError(w, http.StatusBadRequest, "invalid scope_type")
		return pgtype.UUID{}, false
	}
	if scopeType == "workspace" {
		return pgtype.UUID{}, true
	}
	if scopeID == nil || strings.TrimSpace(*scopeID) == "" {
		writeError(w, http.StatusBadRequest, "scope_id is required for "+scopeType+" memory")
		return pgtype.UUID{}, false
	}

	id := strings.TrimSpace(*scopeID)
	switch scopeType {
	case "project":
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{ID: parseUUID(id), WorkspaceID: parseUUID(workspaceID)}); err != nil {
			writeError(w, http.StatusNotFound, "project not found")
			return pgtype.UUID{}, false
		}
	case "agent":
		if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: parseUUID(id), WorkspaceID: parseUUID(workspaceID)}); err != nil {
			writeError(w, http.StatusNotFound, "agent not found")
			return pgtype.UUID{}, false
		}
	case "issue":
		issue, ok := h.findIssueRefInWorkspace(r.Context(), id, workspaceID)
		if !ok {
			writeError(w, http.StatusNotFound, "issue not found")
			return pgtype.UUID{}, false
		}
		return issue.ID, true
	}
	return parseUUID(id), true
}

func (h *Handler) validateMemorySourceRefs(w http.ResponseWriter, r *http.Request, workspaceID string, sourceIssueRef, sourceCommentRef *string) (pgtype.UUID, pgtype.UUID, bool) {
	var sourceIssueID pgtype.UUID
	var sourceCommentID pgtype.UUID

	if sourceIssueRef != nil && strings.TrimSpace(*sourceIssueRef) != "" {
		issue, ok := h.findIssueRefInWorkspace(r.Context(), strings.TrimSpace(*sourceIssueRef), workspaceID)
		if !ok {
			writeError(w, http.StatusNotFound, "source issue not found")
			return pgtype.UUID{}, pgtype.UUID{}, false
		}
		sourceIssueID = issue.ID
	}

	if sourceCommentRef != nil && strings.TrimSpace(*sourceCommentRef) != "" {
		comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
			ID:          parseUUID(strings.TrimSpace(*sourceCommentRef)),
			WorkspaceID: parseUUID(workspaceID),
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "source comment not found")
			return pgtype.UUID{}, pgtype.UUID{}, false
		}
		if sourceIssueID.Valid && uuidToString(sourceIssueID) != uuidToString(comment.IssueID) {
			writeError(w, http.StatusBadRequest, "source_comment_id does not belong to source_issue_id")
			return pgtype.UUID{}, pgtype.UUID{}, false
		}
		sourceIssueID = comment.IssueID
		sourceCommentID = comment.ID
	}

	return sourceIssueID, sourceCommentID, true
}

func (h *Handler) findIssueRefInWorkspace(ctx context.Context, ref, workspaceID string) (db.Issue, bool) {
	if issue, ok := h.resolveIssueByIdentifier(ctx, ref, workspaceID); ok {
		return issue, true
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          parseUUID(ref),
		WorkspaceID: parseUUID(workspaceID),
	})
	return issue, err == nil
}

func decodeReviewMemoryRequest(w http.ResponseWriter, r *http.Request) (reviewMemoryRequest, bool) {
	var req reviewMemoryRequest
	if r.Body == nil {
		return req, true
	}
	err := json.NewDecoder(r.Body).Decode(&req)
	if err == nil || errors.Is(err, io.EOF) {
		return req, true
	}
	writeError(w, http.StatusBadRequest, "invalid request body")
	return reviewMemoryRequest{}, false
}

func (req reviewMemoryRequest) canonicalReviewNote() string {
	if note := strings.TrimSpace(req.ReviewNote); note != "" {
		return note
	}
	return strings.TrimSpace(req.Note)
}

func (req reviewMemoryRequest) canonicalSourceIssue() string {
	if sourceIssue := strings.TrimSpace(req.SourceIssue); sourceIssue != "" {
		return sourceIssue
	}
	return strings.TrimSpace(req.SourceIssueID)
}

func (req reviewMemoryRequest) canonicalSourceComment() string {
	if sourceComment := strings.TrimSpace(req.SourceComment); sourceComment != "" {
		return sourceComment
	}
	return strings.TrimSpace(req.SourceCommentID)
}

func validMemoryScopeType(scopeType string) bool {
	switch scopeType {
	case "workspace", "project", "agent", "issue":
		return true
	default:
		return false
	}
}

func validMemoryStatus(status string) bool {
	switch status {
	case "pending", "approved", "rejected", "revoked":
		return true
	default:
		return false
	}
}

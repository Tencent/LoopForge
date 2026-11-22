package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"agentflow/internal/auth"
	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

const (
	runCleanupDefaultRetentionDays = 30
	runCleanupMaxBatch             = 100
	runCleanupDefaultLimit         = 200
	runCleanupMaxRetentionDays     = 3650
)

var runCleanupStatuses = map[string]bool{"succeeded": true, "failed": true, "cancelled": true}

type runCleanupCandidate struct {
	ID             string    `json:"id"`
	Status         string    `json:"status"`
	WorkflowName   string    `json:"workflowName,omitempty"`
	ProjectID      string    `json:"projectId,omitempty"`
	Input          string    `json:"input,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	FinishedAt     time.Time `json:"finishedAt,omitzero"`
	WorkspaceBytes int64     `json:"workspaceBytes"`
}

type runCleanupTotals struct {
	CandidateCount int   `json:"candidateCount"`
	ListedCount    int   `json:"listedCount"`
	WorkspaceBytes int64 `json:"workspaceBytes"`
	DiskFreeBytes  int64 `json:"diskFreeBytes"`
	DiskTotalBytes int64 `json:"diskTotalBytes"`
	RetentionDays  int   `json:"retentionDays"`
}

type runCleanupView struct {
	Candidates []runCleanupCandidate `json:"candidates"`
	Totals     runCleanupTotals      `json:"totals"`
}

type runCleanupFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

type runCleanupResult struct {
	Deleted    []string            `json:"deleted"`
	Failed     []runCleanupFailure `json:"failed,omitempty"`
	FreedBytes int64               `json:"freedBytes"`
}

type runCleanupRequest struct {
	IDs           []string `json:"ids"`
	ProjectID     string   `json:"projectId"`
	OlderThanDays int      `json:"olderThanDays"`
	Statuses      []string `json:"statuses"`
}

// runCleanupCandidates lists terminal runs that a retention policy would
// remove, together with the workspace bytes they occupy. It is read-only: the
// operator confirms the selection in the UI and posts it to runCleanupDelete.
func (h *runsHandlers) runCleanupCandidates(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	retentionDays := parseCleanupDays(c.Query("olderThanDays"))
	statuses := cleanupStatusFilter(c.Query("statuses"))
	limit := parseCleanupLimit(c.Query("limit"))

	runs, err := h.repo.RunSummariesFor(domain.AnalyticsFilter{ProjectID: projectID}, "")
	if err != nil {
		respond(c, err)
		return
	}
	user, authenticated := h.requestUser(c.Request, true)
	workspaceRoot := h.cleanupWorkspaceRoot()
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	candidates := make([]runCleanupCandidate, 0, len(runs))
	var totalBytes int64
	for _, run := range runs {
		if !h.cleanupRunVisible(run, user, authenticated) {
			continue
		}
		if !cleanupCandidateRun(run, statuses, cutoff) {
			continue
		}
		bytes := cleanupWorkspaceBytes(run.WorkspaceRoot, workspaceRoot)
		totalBytes += bytes
		candidates = append(candidates, runCleanupCandidate{
			ID:             run.ID,
			Status:         run.Status,
			WorkflowName:   run.WorkflowSnapshot.Name,
			ProjectID:      run.ExecutionContext.ProjectID,
			Input:          cleanupClipText(run.Input, 160),
			CreatedAt:      run.CreatedAt,
			FinishedAt:     run.UpdatedAt,
			WorkspaceBytes: bytes,
		})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].CreatedAt.Before(candidates[j].CreatedAt) })
	total := len(candidates)
	listed := candidates
	if limit > 0 && len(listed) > limit {
		listed = listed[:limit]
	}
	freeBytes, diskBytes := diskSpace(cleanupDiskRoot(workspaceRoot))
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, runCleanupView{
		Candidates: listed,
		Totals: runCleanupTotals{
			CandidateCount: total,
			ListedCount:    len(listed),
			WorkspaceBytes: totalBytes,
			DiskFreeBytes:  freeBytes,
			DiskTotalBytes: diskBytes,
			RetentionDays:  retentionDays,
		},
	})
}

// runCleanupDelete removes the confirmed runs. Each deletion goes through
// Engine.DeleteRun so the sandbox, preview, runner workspace and artifacts are
// reclaimed instead of only hiding the row.
func (h *runsHandlers) runCleanupDelete(c *gin.Context) {
	var req runCleanupRequest
	if !decode(c, &req) {
		return
	}
	user, authenticated := h.requestUser(c.Request, true)
	workspaceRoot := h.cleanupWorkspaceRoot()

	ids := cleanupUniqueIDs(req.IDs)
	if len(ids) == 0 {
		retentionDays := req.OlderThanDays
		if retentionDays <= 0 {
			retentionDays = runCleanupDefaultRetentionDays
		}
		if retentionDays > runCleanupMaxRetentionDays {
			c.JSON(http.StatusBadRequest, gin.H{"error": "olderThanDays is out of range"})
			return
		}
		runs, err := h.repo.RunSummariesFor(domain.AnalyticsFilter{ProjectID: strings.TrimSpace(req.ProjectID)}, "")
		if err != nil {
			respond(c, err)
			return
		}
		statuses := cleanupStatusSet(req.Statuses)
		cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
		for _, run := range runs {
			if len(ids) >= runCleanupMaxBatch {
				break
			}
			if !h.cleanupRunVisible(run, user, authenticated) || !cleanupCandidateRun(run, statuses, cutoff) {
				continue
			}
			ids = append(ids, run.ID)
		}
	}
	if len(ids) == 0 {
		c.JSON(http.StatusOK, runCleanupResult{Deleted: []string{}})
		return
	}
	if len(ids) > runCleanupMaxBatch {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("一次最多删除 %d 条运行", runCleanupMaxBatch)})
		return
	}

	result := runCleanupResult{Deleted: make([]string, 0, len(ids))}
	for _, id := range ids {
		run, ok := h.repo.RunState(id)
		if !ok {
			result.Failed = append(result.Failed, runCleanupFailure{ID: id, Error: "run not found"})
			continue
		}
		if !h.cleanupRunVisible(run, user, authenticated) {
			result.Failed = append(result.Failed, runCleanupFailure{ID: id, Error: "无权删除该运行"})
			continue
		}
		if !cleanupTerminalStatus(run.Status) {
			result.Failed = append(result.Failed, runCleanupFailure{ID: id, Error: "运行尚未结束，请先停止"})
			continue
		}
		bytes := cleanupWorkspaceBytes(run.WorkspaceRoot, workspaceRoot)
		if err := h.engine.DeleteRun(id); err != nil {
			result.Failed = append(result.Failed, runCleanupFailure{ID: id, Error: err.Error()})
			continue
		}
		result.Deleted = append(result.Deleted, id)
		result.FreedBytes += bytes
	}
	c.JSON(http.StatusOK, result)
}

func (h *runsHandlers) cleanupWorkspaceRoot() string {
	if h.engine == nil {
		return ""
	}
	return h.engine.WorkspaceRoot()
}

// cleanupRunVisible mirrors the run list visibility rule: another user's
// personal conversation workspace is not listed and must not be deleted.
func (h *runsHandlers) cleanupRunVisible(run domain.Run, user *auth.User, authenticated bool) bool {
	if !run.IsPersonalWorkspace() {
		return true
	}
	return authenticated && user != nil && user.ID > 0 && user.ID == run.ExecutionContext.ResponsibleUserID
}

func cleanupCandidateRun(run domain.Run, statuses map[string]bool, cutoff time.Time) bool {
	if !cleanupTerminalStatus(run.Status) {
		return false
	}
	if statuses != nil && !statuses[run.Status] {
		return false
	}
	if !run.CreatedAt.IsZero() && run.CreatedAt.After(cutoff) {
		return false
	}
	return true
}

func cleanupTerminalStatus(status string) bool {
	return runCleanupStatuses[status]
}

func cleanupStatusSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if runCleanupStatuses[value] {
			set[value] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

func cleanupStatusFilter(raw string) map[string]bool {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return cleanupStatusSet(strings.Split(raw, ","))
}

func cleanupWorkspaceBytes(root, workspaceRoot string) int64 {
	root = strings.TrimSpace(root)
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if root == "" || workspaceRoot == "" {
		return 0
	}
	cleanRoot := filepath.Clean(root)
	cleanBase := filepath.Clean(workspaceRoot)
	if cleanRoot == cleanBase || !strings.HasPrefix(cleanRoot, cleanBase+string(os.PathSeparator)) {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(cleanRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, statErr := entry.Info(); statErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func cleanupDiskRoot(workspaceRoot string) string {
	if strings.TrimSpace(workspaceRoot) != "" {
		return workspaceRoot
	}
	return "."
}

func cleanupClipText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "…"
}

func cleanupUniqueIDs(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func parseCleanupDays(raw string) int {
	days, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || days <= 0 {
		return runCleanupDefaultRetentionDays
	}
	if days > runCleanupMaxRetentionDays {
		return runCleanupMaxRetentionDays
	}
	return days
}

func parseCleanupLimit(raw string) int {
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit <= 0 {
		return runCleanupDefaultLimit
	}
	if limit > runCleanupDefaultLimit {
		return runCleanupDefaultLimit
	}
	return limit
}

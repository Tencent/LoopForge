package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
)

// CLI Runner connection store (cli-connect.md). Task state is always
// durable: a runner disconnect only affects delivery, never the task itself.

var (
	ErrRunnerTaskNotClaimable = errors.New("runner task is not claimable")
	ErrRunnerTaskLeaseLost    = errors.New("runner task lease ownership changed")
	ErrRunnerNotFound         = errors.New("runner not found")
)

type runnerTaskPayload struct {
	Prompt            string                           `json:"prompt"`
	Workspace         string                           `json:"workspace,omitempty"`
	Args              []string                         `json:"args,omitempty"`
	ExitCode          *int                             `json:"exitCode,omitempty"`
	Result            string                           `json:"result,omitempty"`
	Error             string                           `json:"error,omitempty"`
	StartedAt         *runnerTaskTime                  `json:"startedAt,omitempty"`
	CompletedAt       *runnerTaskTime                  `json:"completedAt,omitempty"`
	RunID             string                           `json:"runId,omitempty"`
	NodeID            string                           `json:"nodeId,omitempty"`
	Invocation        *domain.RunnerInvocation         `json:"invocation,omitempty"`
	Sandbox           *domain.RunnerSandboxSpec        `json:"sandbox,omitempty"`
	Command           string                           `json:"command,omitempty"`
	Action            string                           `json:"action,omitempty"`
	PreviewArchive    *domain.RunnerPreviewArchiveSpec `json:"previewArchive,omitempty"`
	WorkBranchMessage string                           `json:"workBranchMessage,omitempty"`
	IntegrationLabel  string                           `json:"integrationLabel,omitempty"`
	// Artifacts holds artifact content uploaded by the runner at completion
	// (name → file content) so the engine can write them into the run
	// workspace and reuse the existing verification/handoff pipeline.
	Artifacts map[string]string         `json:"artifacts,omitempty"`
	Outputs   *domain.RunnerTaskOutputs `json:"outputs,omitempty"`
}

// runnerTaskTime decodes timestamps written by both write paths: Go struct
// marshaling (RFC3339) and the legacy MySQL JSON_SET DATETIME format
// ("2006-01-02 15:04:05.999999"). An unparseable value must never discard
// the whole payload, so parse failures resolve to the zero time.
type runnerTaskTime struct{ time.Time }

func (t *runnerTaskTime) UnmarshalJSON(raw []byte) error {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999", "2006-01-02T15:04:05.999999"} {
		if parsed, err := time.Parse(layout, s); err == nil {
			t.Time = parsed.UTC()
			return nil
		}
	}
	return nil
}

func (t runnerTaskTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.Time.UTC().Format(time.RFC3339Nano))
}

func newRunnerTaskTime(v *time.Time) *runnerTaskTime {
	if v == nil {
		return nil
	}
	return &runnerTaskTime{Time: *v}
}

// toTime collapses a payload timestamp back into a plain pointer, returning
// nil for zero times so JSON output omits them.
func (t *runnerTaskTime) toTime() *time.Time {
	if t == nil || t.Time.IsZero() {
		return nil
	}
	value := t.Time
	return &value
}

func runnerTaskFromRow(row runnerTaskRow) domain.RunnerTask {
	t := domain.RunnerTask{
		ID: row.ID, RunnerID: row.RunnerID, Provider: row.Provider, Status: row.Status,
		LeaseOwner: row.LeaseOwner, DesiredState: row.DesiredState,
		Attempt: row.Attempt, MaxAttempts: row.MaxAttempts,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{},
	}
	if row.LeaseUntil != nil {
		until := *row.LeaseUntil
		t.LeaseUntil = &until
	}
	if row.Created != nil {
		t.CreatedAt = *row.Created
	}
	if row.Updated != nil {
		t.UpdatedAt = *row.Updated
	}
	var p runnerTaskPayload
	if json.Unmarshal(row.Payload, &p) == nil {
		t.Prompt = p.Prompt
		t.Workspace = p.Workspace
		t.Args = p.Args
		t.ExitCode = p.ExitCode
		t.Result = p.Result
		t.Error = p.Error
		t.StartedAt = p.StartedAt.toTime()
		t.CompletedAt = p.CompletedAt.toTime()
		t.RunID = p.RunID
		t.NodeID = p.NodeID
		t.Invocation = p.Invocation
		t.Sandbox = p.Sandbox
		t.Command = p.Command
		t.Action = p.Action
		t.PreviewArchive = p.PreviewArchive
		t.WorkBranchMessage = p.WorkBranchMessage
		t.IntegrationLabel = p.IntegrationLabel
		t.Artifacts = p.Artifacts
		t.Outputs = p.Outputs
	}
	return t
}

func runnerTaskPayloadBytes(v domain.RunnerTask) []byte {
	return mustJSON(runnerTaskPayload{
		Prompt: v.Prompt, Workspace: v.Workspace, Args: v.Args, ExitCode: v.ExitCode,
		Result: v.Result, Error: v.Error, StartedAt: newRunnerTaskTime(v.StartedAt),
		CompletedAt: newRunnerTaskTime(v.CompletedAt), RunID: v.RunID, NodeID: v.NodeID,
		Invocation: v.Invocation, Sandbox: v.Sandbox, Command: v.Command, Action: v.Action,
		PreviewArchive: v.PreviewArchive, WorkBranchMessage: v.WorkBranchMessage, Artifacts: v.Artifacts,
		Outputs: v.Outputs, IntegrationLabel: v.IntegrationLabel,
	})
}

func runnerTaskRowValues(v domain.RunnerTask) map[string]any {
	return map[string]any{
		"id": v.ID, "runner_id": v.RunnerID, "provider": v.Provider, "status": v.Status,
		"lease_owner": v.LeaseOwner, "lease_until": v.LeaseUntil, "desired_state": v.DesiredState,
		"attempt": v.Attempt, "max_attempts": v.MaxAttempts, "payload": runnerTaskPayloadBytes(v),
		"created_at": timePtr(v.CreatedAt), "updated_at": timePtr(v.UpdatedAt),
	}
}

func takeRunnerTask(db *gorm.DB, id string) (runnerTaskRow, bool) {
	var row runnerTaskRow
	if err := db.Where("id = ?", id).Take(&row).Error; err != nil {
		return runnerTaskRow{}, false
	}
	return row, true
}

// ---------- Runner registry ----------

func (r *RunnersRepo) PutRunner(v domain.Runner) error {
	if strings.TrimSpace(v.ID) == "" {
		return fmt.Errorf("runner id is required")
	}
	now := time.Now().UTC()
	if v.CreatedAt.IsZero() {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	if v.Status == "" {
		v.Status = domain.RunnerStatusOffline
	}
	if v.MaxConcurrency < 1 {
		v.MaxConcurrency = 1
	}
	return upsert(r.DB, "runners", map[string]any{
		"id": v.ID, "name": v.Name, "status": v.Status, "os": v.OS, "arch": v.Arch,
		"runner_version": v.RunnerVersion, "labels": jsonBytes(v.Labels), "max_concurrency": v.MaxConcurrency,
		"owner_user_id": nullableInt64(v.OwnerUserID), "session_token_hash": v.SessionTokenHash,
		"session_expires_at": v.SessionExpiresAt, "runtimes": jsonBytes(v.Runtimes),
		"last_seen_at": v.LastSeenAt, "created_at": timePtr(v.CreatedAt), "updated_at": timePtr(v.UpdatedAt),
	})
}

// MaxRunnerRunLimit bounds the user-configurable per-machine run limit.
const MaxRunnerRunLimit = 100

// SetRunnerRunLimit is the only writer of run_limit; PutRunner (register and
// heartbeat) deliberately omits the column so a reconnect never resets it.
// It takes only this machine's admission bucket so a limit change cannot
// interleave with an admission decision on the same machine.
func (r *RunnersRepo) SetRunnerRunLimit(id string, limit int) error {
	if limit < 0 || limit > MaxRunnerRunLimit {
		return fmt.Errorf("最多同时运行条数需在 0-%d 之间（0 表示不限制）", MaxRunnerRunLimit)
	}
	var value any
	if limit > 0 {
		value = limit
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockAdmissionGate(r.deps, tx, admissionBucketKey("", id, true)); err != nil {
			return err
		}
		res := tx.Model(&runnerRow{}).Where("id = ?", id).Updates(map[string]any{"run_limit": value, "updated_at": time.Now().UTC()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrRunnerNotFound
		}
		return nil
	})
}

func (r *RunnersRepo) Runner(id string) (domain.Runner, bool) {
	return r.runnerRow(id)
}

func (r *RunnersRepo) runnerRow(id string) (domain.Runner, bool) {
	var row runnerRow
	if err := r.DB.Where("id = ?", id).Take(&row).Error; err != nil {
		return domain.Runner{}, false
	}
	return runnerFromRow(row), true
}

func runnerFromRow(row runnerRow) domain.Runner {
	v := domain.Runner{
		ID: row.ID, Name: row.Name, Status: row.Status, OS: row.OS, Arch: row.Arch,
		RunnerVersion: row.RunnerVersion, MaxConcurrency: row.MaxConcurrency,
		SessionTokenHash: row.SessionTokenHash,
	}
	if row.OwnerUserID != nil {
		v.OwnerUserID = *row.OwnerUserID
	}
	if row.RunLimit != nil && *row.RunLimit > 0 {
		v.RunLimit = *row.RunLimit
	}
	if len(row.Labels) > 0 {
		_ = json.Unmarshal(row.Labels, &v.Labels)
	}
	if len(row.Runtimes) > 0 {
		_ = json.Unmarshal(row.Runtimes, &v.Runtimes)
	}
	if row.SessionExpiresAt != nil {
		t := *row.SessionExpiresAt
		v.SessionExpiresAt = &t
	}
	if row.LastSeenAt != nil {
		t := *row.LastSeenAt
		v.LastSeenAt = &t
	}
	if row.Created != nil {
		v.CreatedAt = *row.Created
	}
	if row.Updated != nil {
		v.UpdatedAt = *row.Updated
	}
	return v
}

func (r *RunnersRepo) Runners() []domain.Runner {
	var rows []runnerRow
	if err := r.DB.Order("created_at").Find(&rows).Error; err != nil {
		return []domain.Runner{}
	}
	out := make([]domain.Runner, 0, len(rows))
	for _, row := range rows {
		out = append(out, runnerFromRow(row))
	}
	return out
}

// RunnerSessionByHash resolves a session token hash to its runner. Expired
// sessions are rejected so a leaked old session token stops working.
func (r *RunnersRepo) RunnerSessionByHash(hash string) (domain.Runner, bool) {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return domain.Runner{}, false
	}
	var row runnerRow
	if err := r.DB.Where("session_token_hash = ?", hash).Take(&row).Error; err != nil {
		return domain.Runner{}, false
	}
	if row.SessionExpiresAt != nil && !row.SessionExpiresAt.After(time.Now().UTC()) {
		return domain.Runner{}, false
	}
	return runnerFromRow(row), true
}

// RunnerHeartbeat records liveness and the latest runtime inventory. The
// server-side disabled flag survives: a CLI disabled from the 执行器 page
// stays disabled even though the daemon keeps reporting it online.
func (r *RunnersRepo) RunnerHeartbeat(id string, runtimes []domain.RunnerRuntime, version string, now, sessionExpiresAt time.Time) error {
	current, ok := r.runnerRow(id)
	disabled := map[string]bool{}
	authModes := map[string]string{}
	if ok {
		for _, rt := range current.Runtimes {
			if rt.Status == domain.RuntimeStatusDisabled {
				disabled[rt.Provider] = true
			}
			if rt.AuthMode == domain.RuntimeAuthLocal || rt.AuthMode == domain.RuntimeAuthPersonal {
				authModes[rt.Provider] = rt.AuthMode
			}
		}
	}
	for i := range runtimes {
		if disabled[runtimes[i].Provider] {
			runtimes[i].Status = domain.RuntimeStatusDisabled
		}
		if mode := authModes[runtimes[i].Provider]; mode != "" {
			runtimes[i].AuthMode = mode
		} else if strings.EqualFold(runtimes[i].Provider, "codebuddy") {
			runtimes[i].AuthMode = domain.RuntimeAuthPersonal
		} else {
			runtimes[i].AuthMode = domain.RuntimeAuthLocal
		}
	}
	raw, err := json.Marshal(runtimes)
	if err != nil {
		return err
	}
	updates := map[string]any{
		"status": domain.RunnerStatusOnline, "last_seen_at": now, "runtimes": string(raw),
		"session_expires_at": sessionExpiresAt, "updated_at": now,
	}
	if version = strings.TrimSpace(version); version != "" {
		updates["runner_version"] = version
	}
	res := r.DB.Model(&runnerRow{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrRunnerNotFound
	}
	return nil
}

// SetRunnerRuntimeAuthMode selects local CLI login or the responsible user's
// saved provider credential for one runtime. The daemon inventory heartbeat
// cannot overwrite this server-managed preference.
func (r *RunnersRepo) SetRunnerRuntimeAuthMode(runnerID, provider, authMode string, now time.Time) error {
	if authMode != domain.RuntimeAuthLocal && authMode != domain.RuntimeAuthPersonal {
		return fmt.Errorf("invalid runtime auth mode %q", authMode)
	}
	if strings.EqualFold(provider, "codebuddy") && authMode != domain.RuntimeAuthPersonal {
		return fmt.Errorf("CodeBuddy 仅支持个人凭证")
	}
	return r.updateRunnerRuntimes(runnerID, func(runtimes []domain.RunnerRuntime) (bool, error) {
		found := false
		for i := range runtimes {
			if strings.EqualFold(runtimes[i].Provider, provider) {
				runtimes[i].AuthMode = authMode
				found = true
			}
		}
		if !found {
			return false, fmt.Errorf("runtime %q not found on runner %s", provider, runnerID)
		}
		return true, nil
	}, now)
}

// SetRunnerRuntimeStatus enables or disables one discovered CLI on a runner.
// Disabled CLIs stop receiving tasks of that provider.
func (r *RunnersRepo) SetRunnerRuntimeStatus(runnerID, provider, status string, now time.Time) error {
	if status != domain.RuntimeStatusOnline && status != domain.RuntimeStatusDisabled {
		return fmt.Errorf("invalid runtime status %q", status)
	}
	return r.updateRunnerRuntimes(runnerID, func(runtimes []domain.RunnerRuntime) (bool, error) {
		found := false
		for i := range runtimes {
			if strings.EqualFold(runtimes[i].Provider, provider) {
				runtimes[i].Status = status
				found = true
			}
		}
		if !found {
			return false, fmt.Errorf("runtime %q not found on runner %s", provider, runnerID)
		}
		return true, nil
	}, now)
}

func (r *RunnersRepo) updateRunnerRuntimes(runnerID string, mutate func([]domain.RunnerRuntime) (bool, error), now time.Time) error {
	current, ok := r.runnerRow(runnerID)
	if !ok {
		return ErrRunnerNotFound
	}
	changed, err := mutate(current.Runtimes)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	raw, err := json.Marshal(current.Runtimes)
	if err != nil {
		return err
	}
	return r.DB.Model(&runnerRow{}).Where("id = ?", runnerID).Updates(map[string]any{
		"runtimes": string(raw), "updated_at": now,
	}).Error
}

// MarkOfflineRunners flags runners whose heartbeat is older than threshold.
// Callers decide their own thresholds; the store only persists the state.
func (r *RunnersRepo) MarkOfflineRunners(threshold time.Duration, now time.Time) {
	if r.DB == nil {
		return
	}
	cutoff := now.Add(-threshold)
	_ = r.DB.Model(&runnerRow{}).
		Where("status <> ? AND last_seen_at IS NOT NULL AND last_seen_at < ?", domain.RunnerStatusOffline, cutoff).
		Updates(map[string]any{"status": domain.RunnerStatusOffline, "updated_at": now})
}

// MarkRunnerOffline records an intentional daemon disconnect while retaining
// the runner identity, session, and user preference for the next restart.
func (r *RunnersRepo) MarkRunnerOffline(id string, now time.Time) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrRunnerNotFound
	}
	res := r.DB.Model(&runnerRow{}).Where("id = ?", id).Updates(map[string]any{
		"status": domain.RunnerStatusOffline, "updated_at": now,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrRunnerNotFound
	}
	return nil
}

// ---------- Runner tasks ----------

func (r *RunnersRepo) PutRunnerTask(v domain.RunnerTask) error {
	if strings.TrimSpace(v.ID) == "" {
		return fmt.Errorf("runner task id is required")
	}
	if strings.TrimSpace(v.Provider) == "" {
		return fmt.Errorf("runner task provider is required")
	}
	now := time.Now().UTC()
	if v.CreatedAt.IsZero() {
		v.CreatedAt = now
	}
	if v.Status == "" {
		v.Status = domain.RunnerTaskQueued
	}
	if v.MaxAttempts < 1 {
		v.MaxAttempts = 1
	}
	v.UpdatedAt = now
	return upsert(r.DB, "runner_tasks", runnerTaskRowValues(v))
}

func (r *RunnersRepo) RunnerTask(id string) (domain.RunnerTask, bool) {
	row, ok := takeRunnerTask(r.DB, id)
	if !ok {
		return domain.RunnerTask{}, false
	}
	return runnerTaskFromRow(row), true
}

// RunnerTaskControl reads only the lease and state polled by active runners.
// Prompt, invocation, results and artifact bodies are not needed by this API.
func (r *RunnersRepo) RunnerTaskControl(id string) (domain.RunnerTask, bool) {
	var row runnerTaskRow
	if err := r.DB.Select("id", "desired_state", "status", "lease_owner").Where("id = ?", id).Take(&row).Error; err != nil {
		return domain.RunnerTask{}, false
	}
	return domain.RunnerTask{ID: row.ID, DesiredState: row.DesiredState, Status: row.Status, LeaseOwner: row.LeaseOwner}, true
}

func (r *RunnersRepo) RunnerTasks() []domain.RunnerTask {
	var rows []runnerTaskRow
	if err := r.DB.Order("created_at DESC").Limit(200).Find(&rows).Error; err != nil {
		return []domain.RunnerTask{}
	}
	out := make([]domain.RunnerTask, 0, len(rows))
	for _, row := range rows {
		out = append(out, runnerTaskFromRow(row))
	}
	return out
}

// RunnerTaskAvailable checks for eligible queued work without taking a lease.
func (r *RunnersRepo) RunnerTaskAvailable(runnerID string, providers []string) (bool, error) {
	if strings.TrimSpace(runnerID) == "" || len(providers) == 0 {
		return false, nil
	}
	allowed := make([]string, 0, len(providers))
	for _, provider := range providers {
		if provider = strings.TrimSpace(provider); provider != "" {
			allowed = append(allowed, provider)
		}
	}
	if len(allowed) == 0 {
		return false, nil
	}
	var rows []runnerTaskRow
	err := r.DB.Select("id").Where("status = ? AND (runner_id = '' OR runner_id = ?) AND provider IN ?", domain.RunnerTaskQueued, runnerID, allowed).Limit(1).Find(&rows).Error
	return len(rows) > 0, err
}

// ClaimRunnerTask atomically leases the oldest queued task whose provider is
// in the runner's available list and whose runner targeting (if any) matches.
// The transactional claim guarantees exactly one winner even when several
// runners wake up at the same time.
func (r *RunnersRepo) ClaimRunnerTask(runnerID string, providers []string, now time.Time, leaseTTL time.Duration) (domain.RunnerTask, bool, error) {
	if strings.TrimSpace(runnerID) == "" {
		return domain.RunnerTask{}, false, fmt.Errorf("runner id is required")
	}
	if len(providers) == 0 {
		return domain.RunnerTask{}, false, nil
	}
	allowed := make([]string, 0, len(providers))
	for _, p := range providers {
		if p = strings.TrimSpace(p); p != "" {
			allowed = append(allowed, p)
		}
	}
	if len(allowed) == 0 {
		return domain.RunnerTask{}, false, nil
	}
	var claimed domain.RunnerTask
	var found bool
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var rows []runnerTaskRow
		if err := r.locking(tx).
			Where("status = ? AND (runner_id = '' OR runner_id = ?) AND provider IN ?", domain.RunnerTaskQueued, runnerID, allowed).
			Order("created_at").Limit(1).
			Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		taskID := rows[0].ID
		res := tx.Model(&runnerTaskRow{}).
			Where("id = ? AND status = ?", taskID, domain.RunnerTaskQueued).
			Updates(map[string]any{
				"status": domain.RunnerTaskLeased, "lease_owner": runnerID,
				"lease_until": now.Add(leaseTTL), "attempt": rows[0].Attempt + 1,
				"desired_state": "", "updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		row, ok := takeRunnerTask(tx, taskID)
		if !ok {
			return fmt.Errorf("claimed task %s disappeared", taskID)
		}
		claimed = runnerTaskFromRow(row)
		found = true
		return nil
	})
	if err != nil {
		return domain.RunnerTask{}, false, err
	}
	return claimed, found, nil
}

// StartRunnerTask moves a leased task into running.
func (r *RunnersRepo) StartRunnerTask(id, runnerID string, now time.Time) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row, ok := r.takeRunnerTaskForLease(tx, id, runnerID)
		if !ok {
			return ErrRunnerTaskLeaseLost
		}
		task := runnerTaskFromRow(row)
		task.Status = domain.RunnerTaskRunning
		if task.StartedAt == nil {
			started := now
			task.StartedAt = &started
		}
		task.UpdatedAt = now
		return tx.Model(&runnerTaskRow{}).Where("id = ?", id).Updates(map[string]any{
			"status": task.Status, "payload": runnerTaskPayloadBytes(task), "updated_at": now,
		}).Error
	})
}

// takeRunnerTaskForLease loads a task that must currently be leased by runnerID.
func (r *RunnersRepo) takeRunnerTaskForLease(tx *gorm.DB, id, runnerID string) (runnerTaskRow, bool) {
	var row runnerTaskRow
	if err := r.locking(tx).
		Where("id = ? AND lease_owner = ? AND status IN ?", id, runnerID, []string{domain.RunnerTaskLeased, domain.RunnerTaskRunning}).
		Take(&row).Error; err != nil {
		return runnerTaskRow{}, false
	}
	return row, true
}

// RenewRunnerTaskLease extends the lease and returns the desired state so a
// cancel can travel on the same response without extra polling.
func (r *RunnersRepo) RenewRunnerTaskLease(id, runnerID string, ttl time.Duration, now time.Time) (string, error) {
	var desired string
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		row, ok := r.takeRunnerTaskForLease(tx, id, runnerID)
		if !ok {
			return ErrRunnerTaskLeaseLost
		}
		if err := tx.Model(&runnerTaskRow{}).Where("id = ?", id).Updates(map[string]any{
			"lease_until": now.Add(ttl), "updated_at": now,
		}).Error; err != nil {
			return err
		}
		desired = row.DesiredState
		return nil
	})
	return desired, err
}

// CompleteRunnerTask finalizes a task. Only the lease owner may complete it.
// artifacts (legacy, by contract name) and outputs carry what the runner
// reported about its workspace; either may be nil.
func (r *RunnersRepo) CompleteRunnerTask(id, runnerID, status string, exitCode *int, result, errMsg string, artifacts map[string]string, outputs *domain.RunnerTaskOutputs, now time.Time) error {
	if status != domain.RunnerTaskSuccess && status != domain.RunnerTaskFailed && status != domain.RunnerTaskCanceled {
		return fmt.Errorf("invalid final task status %q", status)
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row, ok := r.takeRunnerTaskForLease(tx, id, runnerID)
		if !ok {
			return ErrRunnerTaskLeaseLost
		}
		task := runnerTaskFromRow(row)
		task.Status = status
		task.ExitCode = exitCode
		task.Result = result
		task.Error = errMsg
		if artifacts != nil {
			task.Artifacts = artifacts
		}
		if outputs != nil {
			task.Outputs = outputs
		}
		scrubRunnerTaskCredentials(&task)
		completed := now
		task.CompletedAt = &completed
		task.LeaseOwner = ""
		task.LeaseUntil = nil
		task.UpdatedAt = now
		return tx.Model(&runnerTaskRow{}).Where("id = ?", id).Updates(runnerTaskRowValues(task)).Error
	})
}

func scrubRunnerTaskCredentials(task *domain.RunnerTask) {
	if task == nil || task.Invocation == nil {
		return
	}
	task.Invocation.CodeBuddyAPIKey = ""
	task.Invocation.CodeBuddyInternetEnvironment = ""
	task.Invocation.CodexAPIKey = ""
	task.Invocation.CodexBaseURL = ""
	task.Invocation.ClaudeCodeAPIKey = ""
	task.Invocation.ClaudeCodeBaseURL = ""
	task.Invocation.TaihuPAT = ""
	if task.Invocation.Repository != nil {
		task.Invocation.Repository.GitToken = ""
	}
	// repositories[*].gitToken must be scrubbed too: unlike the single legacy
	// repository field the array entries would otherwise keep per-checkout
	// tokens in the durable payload after the task reaches a terminal state.
	for i := range task.Invocation.Repositories {
		task.Invocation.Repositories[i].GitToken = ""
	}
}

// CancelRunnerTask asks for cancellation. Queued tasks are canceled outright;
// live tasks are flagged and the flag reaches the runner on lease renewal.
func (r *RunnersRepo) CancelRunnerTask(id string, now time.Time) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&runnerTaskRow{}).
			Where("id = ? AND status = ?", id, domain.RunnerTaskQueued).
			Updates(map[string]any{"status": domain.RunnerTaskCanceled, "lease_owner": "", "lease_until": nil, "desired_state": "", "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			// Scrub credentials in the payload of the canceled task.
			if row, ok := takeRunnerTask(tx, id); ok {
				task := runnerTaskFromRow(row)
				scrubRunnerTaskCredentials(&task)
				completed := now
				task.CompletedAt = &completed
				return tx.Model(&runnerTaskRow{}).Where("id = ?", id).Updates(map[string]any{
					"payload": runnerTaskPayloadBytes(task), "updated_at": now,
				}).Error
			}
			return nil
		}
		return tx.Model(&runnerTaskRow{}).
			Where("id = ? AND status IN ?", id, []string{domain.RunnerTaskLeased, domain.RunnerTaskRunning}).
			Updates(map[string]any{"desired_state": domain.RunnerTaskDesiredCancel, "updated_at": now}).Error
	})
}

// ExpireRunnerTasks recovers tasks whose lease was never renewed (runner
// slept, crashed, or lost network). Retryable tasks go back to QUEUED;
// exhausted tasks fail. Returns the recovered tasks for observability.
func (r *RunnersRepo) ExpireRunnerTasks(now time.Time) ([]domain.RunnerTask, error) {
	var recovered []domain.RunnerTask
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var rows []runnerTaskRow
		if err := r.locking(tx).
			Where("status IN ? AND lease_until IS NOT NULL AND lease_until < ?", []string{domain.RunnerTaskLeased, domain.RunnerTaskRunning}, now).
			Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			task := runnerTaskFromRow(row)
			if task.Attempt < task.MaxAttempts {
				task.Status = domain.RunnerTaskQueued
			} else {
				task.Status = domain.RunnerTaskFailed
				task.Error = "lease expired"
				completed := now
				task.CompletedAt = &completed
				scrubRunnerTaskCredentials(&task)
			}
			task.LeaseOwner = ""
			task.LeaseUntil = nil
			task.DesiredState = ""
			task.UpdatedAt = now
			if err := tx.Model(&runnerTaskRow{}).Where("id = ?", task.ID).Updates(runnerTaskRowValues(task)).Error; err != nil {
				return err
			}
			recovered = append(recovered, task)
		}
		return nil
	})
	return recovered, err
}

// AppendRunnerTaskEvents stores batched runner output. (task_id, seq) is the
// idempotency key: an HTTP retry resends the same seq and is ignored.
func (r *RunnersRepo) AppendRunnerTaskEvents(taskID string, events []domain.RunnerTaskEvent) (int, error) {
	if taskID = strings.TrimSpace(taskID); taskID == "" {
		return 0, fmt.Errorf("task id is required")
	}
	if len(events) == 0 {
		return 0, nil
	}
	accepted := 0
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		accepted = 0
		for _, ev := range events {
			inserted, err := insertIgnore(tx, "runner_task_events", map[string]any{
				"task_id": taskID, "seq": ev.Seq, "type": ev.Type, "data": ev.Data, "ts": timePtr(ev.Ts),
			})
			if err != nil {
				return err
			}
			if inserted {
				accepted++
			}
		}
		return nil
	})
	return accepted, err
}

func (r *RunnersRepo) RunnerTaskEvents(taskID string) []domain.RunnerTaskEvent {
	return r.RunnerTaskEventsAfter(taskID, 0)
}

// RunnerTaskEventsAfter returns only events newer than the last sequence the
// worker has bridged. This keeps live observability polling proportional to
// new output instead of repeatedly loading an entire CLI transcript.
func (r *RunnersRepo) RunnerTaskEventsAfter(taskID string, after int64) []domain.RunnerTaskEvent {
	var rows []runnerTaskEventRow
	if err := r.DB.Where("task_id = ? AND seq > ?", taskID, after).Order("seq").Find(&rows).Error; err != nil {
		return []domain.RunnerTaskEvent{}
	}
	out := make([]domain.RunnerTaskEvent, 0, len(rows))
	for _, row := range rows {
		ev := domain.RunnerTaskEvent{Seq: row.Seq, Type: row.Type, Data: row.Data}
		if row.Ts != nil {
			ev.Ts = *row.Ts
		}
		out = append(out, ev)
	}
	return out
}

// DeregisterRunner removes a runner and everything that points at it: the
// machine record, default-execution-machine selections that target it, and
// its queued tasks (running tasks recover through lease expiry). Called when
// the daemon uninstalls or stops gracefully.
func (r *RunnersRepo) DeregisterRunner(id string, now time.Time) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("runner id is required")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var rows []runnerTaskRow
		if err := tx.Where("runner_id = ? AND status = ?", id, domain.RunnerTaskQueued).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			task := runnerTaskFromRow(row)
			task.Status = domain.RunnerTaskCanceled
			task.LeaseOwner = ""
			task.LeaseUntil = nil
			scrubRunnerTaskCredentials(&task)
			completed := now
			task.CompletedAt = &completed
			task.UpdatedAt = now
			if err := tx.Model(&runnerTaskRow{}).Where("id = ?", task.ID).Updates(runnerTaskRowValues(task)).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("default_runner_id = ?", id).Delete(&userRunnerPrefRow{}).Error; err != nil {
			return err
		}
		res := tx.Where("id = ?", id).Delete(&runnerRow{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrRunnerNotFound
		}
		return nil
	})
}

// ---------- default execution machine (执行机器) ----------

// DefaultRunner returns the user's default execution machine, if any. An
// empty result means the cloud mode (control-plane sandbox + platform API
// keys).
func (r *RunnersRepo) DefaultRunner(userID int64) string {
	if userID <= 0 {
		return ""
	}
	var row userRunnerPrefRow
	if err := r.DB.Where("user_id = ?", userID).Take(&row).Error; err != nil {
		return ""
	}
	return strings.TrimSpace(row.DefaultRunnerID)
}

// SetDefaultRunner selects (or clears, with an empty id) the user's default
// execution machine.
func (r *RunnersRepo) SetDefaultRunner(userID int64, runnerID string, now time.Time) error {
	if userID <= 0 {
		return fmt.Errorf("user id is required")
	}
	runnerID = strings.TrimSpace(runnerID)
	if runnerID != "" {
		if _, ok := r.Runner(runnerID); !ok {
			return ErrRunnerNotFound
		}
	}
	if runnerID == "" {
		return r.DB.Where("user_id = ?", userID).Delete(&userRunnerPrefRow{}).Error
	}
	return upsert(r.DB, "user_runner_prefs", map[string]any{
		"user_id": userID, "default_runner_id": runnerID, "updated_at": timePtr(now),
	})
}

// ---------- helpers ----------

func containsFold(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// sortRunnerTasksByCreated is used by tests that need deterministic order.
func sortRunnerTasksByCreated(tasks []domain.RunnerTask) {
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.Before(tasks[j].CreatedAt) })
}

// ActiveRunnerTasksForRun lists the queued/leased/running tasks of a Run.
func (r *RunnersRepo) ActiveRunnerTasksForRun(runID string) ([]domain.RunnerTask, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	var rows []runnerTaskRow
	if err := r.DB.Where("status IN ?", []string{domain.RunnerTaskQueued, domain.RunnerTaskLeased, domain.RunnerTaskRunning}).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.RunnerTask, 0, len(rows))
	for _, row := range rows {
		task := runnerTaskFromRow(row)
		if task.RunID == runID {
			out = append(out, task)
		}
	}
	return out, nil
}

// CancelRunnerTasksForRun interrupts all active remote work for a Run. Queued
// tasks become terminal immediately; leased/running tasks receive the durable
// desired_state=cancel signal consumed by the runner control channel.
func (r *RunnersRepo) CancelRunnerTasksForRun(runID string, now time.Time) ([]string, error) {
	tasks, err := r.ActiveRunnerTasksForRun(runID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if err := r.CancelRunnerTask(task.ID, now); err != nil {
			return ids, err
		}
		ids = append(ids, task.ID)
	}
	return ids, nil
}

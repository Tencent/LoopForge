package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

// BaselineAlignResolverAgent is the shared platform role for scheduled
// baseline alignment. No project hooks or issue-fix verification gates.
func BaselineAlignResolverAgent() domain.AgentDefinition {
	return domain.AgentDefinition{
		ID:           domain.BaselineAlignAgentID,
		Name:         "基线对齐执行员",
		Version:      1,
		ProjectID:    "platform",
		Shared:       true,
		Description:  "全平台共用：把默认分支合入集成分支并解决冲突。不跑项目测试，不写修复报告，不发 MR。",
		Instructions: "你只做基线对齐，不是需求修复。\n1. 在当前工作分支执行 git fetch 并 merge 默认分支\n2. 逐块解决冲突，保留双方有效实现；拿不准时选对业务影响更小的一侧并在提交说明里写清\n3. git add 冲突文件并创建合并提交，提交说明列出冲突文件与取舍\n4. 不要 push，不要改默认分支，不要跑项目单测或 E2E，不要写 fix-plan.md / fix-report.md / review-report.md\n5. 结束后用几句话说明每个冲突文件的取舍即可",
		ExecutorID:   "codebuddy",
	}
}

// ---------- Agents ----------

func (r *CatalogRepo) Agents() []domain.AgentDefinition {
	out := payloadList[domain.AgentDefinition](r.DB, "agents", "")
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *CatalogRepo) Agent(id string) (domain.AgentDefinition, bool) {
	return payloadOne[domain.AgentDefinition](r.DB, "agents", id)
}

func (r *CatalogRepo) PutAgent(v domain.AgentDefinition) error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Name) == "" {
		return errors.New("agent id and name are required")
	}
	if v.Loop.Enabled {
		if v.Loop.MaxIterations < 1 || v.Loop.MaxIterations > 12 {
			return errors.New("agent loop maxIterations must be between 1 and 12")
		}
		if v.Loop.ExitCondition == "" {
			v.Loop.ExitCondition = "all_gates_pass"
		}
	}
	for _, output := range v.ArtifactOutputs {
		clean := filepath.Clean(strings.TrimSpace(output.Path))
		if output.Name == "" || clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("invalid agent artifact contract %q", output.Name)
		}
	}
	validStages := map[string]bool{"before_execution": true, "after_execution": true, "before_publish": true, "on_failure": true}
	for _, point := range v.Interventions {
		if point.ID == "" || point.Name == "" || !validStages[point.Stage] {
			return fmt.Errorf("invalid agent intervention point %q", point.ID)
		}
	}
	validHookEvents := map[string]bool{"SessionStart": true, "UserPromptSubmit": true, "PreToolUse": true, "PostToolUse": true, "Stop": true}
	hookIDs := map[string]bool{}
	for index := range v.Hooks {
		hook := &v.Hooks[index]
		hook.ID = strings.TrimSpace(hook.ID)
		hook.Name = strings.TrimSpace(hook.Name)
		hook.Event = strings.TrimSpace(hook.Event)
		hook.Language = strings.ToLower(strings.TrimSpace(hook.Language))
		hook.Script = strings.TrimSpace(hook.Script)
		if hook.ID == "" || hook.ID == "." || hook.ID == ".." || filepath.Base(hook.ID) != hook.ID || strings.ContainsAny(hook.ID, " \\/") {
			return fmt.Errorf("invalid agent hook id %q", hook.ID)
		}
		if hookIDs[hook.ID] {
			return fmt.Errorf("duplicate agent hook %q", hook.ID)
		}
		hookIDs[hook.ID] = true
		if hook.Name == "" || !validHookEvents[hook.Event] {
			return fmt.Errorf("invalid agent hook %q", hook.ID)
		}
		if hook.Language != "shell" && hook.Language != "python" {
			return fmt.Errorf("agent hook %q has invalid language %q", hook.ID, hook.Language)
		}
		if hook.Script == "" {
			return fmt.Errorf("agent hook %q script is required", hook.ID)
		}
		if hook.TimeoutSec == 0 {
			hook.TimeoutSec = 10
		}
		if hook.TimeoutSec < 1 || hook.TimeoutSec > 600 {
			return fmt.Errorf("agent hook %q timeoutSec must be between 1 and 600", hook.ID)
		}
	}
	v.UpdatedAt = time.Now().UTC()
	v.Rules = normalizeRefs(v.Rules, "rules/")
	v.Skills = normalizeRefs(v.Skills, "skills/")
	if old, ok := r.Agent(v.ID); ok {
		v.ProjectID = domain.KeepCatalogProjectID(v.ProjectID, old.ProjectID)
		if err := r.snapshotRevision("agent", old.ID, old.Name, old.Version, 0, old); err != nil {
			return err
		}
	}
	return upsert(r.DB, "agents", map[string]any{
		"id": v.ID, "name": v.Name, "payload": mustJSON(v), "updated_at": timePtr(v.UpdatedAt),
	})
}

// ---------- Rules / Skills / MCP servers ----------

func (r *CatalogRepo) Rules() []domain.RuleDefinition {
	out := payloadList[domain.RuleDefinition](r.DB, "rules", "")
	sort.Slice(out, func(i, j int) bool { return out[i].Priority > out[j].Priority })
	return out
}

func (r *CatalogRepo) Rule(id string) (domain.RuleDefinition, bool) {
	return payloadOne[domain.RuleDefinition](r.DB, "rules", id)
}

func (r *CatalogRepo) PutRule(v domain.RuleDefinition) error {
	v.UpdatedAt = time.Now().UTC()
	return upsert(r.DB, "rules", map[string]any{
		"id": v.ID, "name": v.Name, "payload": mustJSON(v), "updated_at": timePtr(v.UpdatedAt),
	})
}

func (r *CatalogRepo) DeleteRule(id string) error {
	if _, ok := r.Rule(id); !ok {
		return fmt.Errorf("rule not found")
	}
	return r.DB.Table("rules").Where("id = ?", id).Delete(nil).Error
}

func (r *CatalogRepo) Skills() []domain.SkillDefinition {
	return payloadList[domain.SkillDefinition](r.DB, "skills", "")
}

func (r *CatalogRepo) Skill(id string) (domain.SkillDefinition, bool) {
	return payloadOne[domain.SkillDefinition](r.DB, "skills", id)
}

func (r *CatalogRepo) PutSkill(v domain.SkillDefinition) error {
	v.UpdatedAt = time.Now().UTC()
	return upsert(r.DB, "skills", map[string]any{
		"id": v.ID, "name": v.Name, "payload": mustJSON(v), "updated_at": timePtr(v.UpdatedAt),
	})
}

func (r *CatalogRepo) DeleteSkill(id string) error {
	if _, ok := r.Skill(id); !ok {
		return fmt.Errorf("skill not found")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("skill_id = ?", id).Delete(&skillFileRow{}).Error; err != nil {
			return err
		}
		return tx.Table("skills").Where("id = ?", id).Delete(nil).Error
	})
}

func (r *CatalogRepo) MCPServers() []domain.MCPServerDefinition {
	return payloadList[domain.MCPServerDefinition](r.DB, "mcp_servers", "")
}

func (r *CatalogRepo) MCPServer(id string) (domain.MCPServerDefinition, bool) {
	return payloadOne[domain.MCPServerDefinition](r.DB, "mcp_servers", id)
}

func (r *CatalogRepo) PutMCPServer(v domain.MCPServerDefinition) error {
	// Uploaded contents and their manifest are owned by the mcp_files API.
	// Definition-only updates (including JSON import) must not orphan existing
	// rows merely because fileRefs was omitted by an older client.
	if v.FileRefs == nil {
		if existing, ok := r.MCPServer(v.ID); ok {
			v.FileRefs = existing.FileRefs
		}
	}
	if err := domain.ValidateMCPServer(v); err != nil {
		return err
	}
	v.UpdatedAt = time.Now().UTC()
	return upsert(r.DB, "mcp_servers", map[string]any{
		"id": v.ID, "name": v.Name, "payload": mustJSON(v), "updated_at": timePtr(v.UpdatedAt),
	})
}

func (r *CatalogRepo) DeleteMCPServer(id string) error {
	if _, ok := r.MCPServer(id); !ok {
		return fmt.Errorf("MCP not found")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("mcp_id = ?", id).Delete(&mcpFileRow{}).Error; err != nil {
			return err
		}
		return tx.Table("mcp_servers").Where("id = ?", id).Delete(nil).Error
	})
}

func (r *CatalogRepo) DeleteAgent(id string) error {
	if _, ok := payloadOne[domain.AgentDefinition](r.DB, "agents", id); !ok {
		return fmt.Errorf("agent not found")
	}
	return r.DB.Table("agents").Where("id = ?", id).Delete(nil).Error
}

// ---------- Executors ----------

func (r *CatalogRepo) Executors() []domain.ExecutorDefinition {
	return payloadList[domain.ExecutorDefinition](r.DB, "executors", "")
}

func (r *CatalogRepo) Executor(id string) (domain.ExecutorDefinition, bool) {
	return payloadOne[domain.ExecutorDefinition](r.DB, "executors", id)
}

func (r *CatalogRepo) PutExecutor(v domain.ExecutorDefinition) error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Name) == "" || strings.TrimSpace(v.Kind) == "" {
		return errors.New("executor id, name and kind are required")
	}
	return upsert(r.DB, "executors", map[string]any{
		"id": v.ID, "name": v.Name, "kind": v.Kind, "payload": mustJSON(v),
	})
}

// BuiltInExecutors is the complete user-selectable CLI catalog. Executor
// definitions remain persisted for historical snapshots and compatibility,
// but they are no longer a user-managed runtime asset.
func BuiltInExecutors() []domain.ExecutorDefinition {
	return []domain.ExecutorDefinition{
		{ID: "codebuddy", Name: "CodeBuddy", Kind: "codebuddy", Binary: "codebuddy", Args: []string{"-p", "--output-format", "stream-json", "--include-partial-messages", "-y", "--setting-sources", "none"}, Protocol: "stream-json"},
		{ID: "codex", Name: "Codex", Kind: "codex", Binary: "codex", Args: []string{"exec", "--json", "--sandbox", "workspace-write", "--skip-git-repo-check"}, Protocol: "jsonl"},
		{ID: "claude", Name: "Claude Code", Kind: "claude-code", Binary: "claude", Args: []string{"-p", "--output-format", "stream-json", "--permission-mode", "dontAsk"}, Protocol: "stream-json"},
	}
}

func BuiltInExecutor(id string) (domain.ExecutorDefinition, bool) {
	for _, executor := range BuiltInExecutors() {
		if executor.ID == strings.TrimSpace(id) {
			return executor, true
		}
	}
	return domain.ExecutorDefinition{}, false
}

func BuiltInExecutorForKind(kind string) (domain.ExecutorDefinition, bool) {
	for _, executor := range BuiltInExecutors() {
		if executor.Kind == strings.TrimSpace(kind) {
			return executor, true
		}
	}
	return domain.ExecutorDefinition{}, false
}

// ---------- Dockerfiles / Environments / Sandboxes ----------

func (r *CatalogRepo) Dockerfiles() []domain.DockerfileDefinition {
	return payloadList[domain.DockerfileDefinition](r.DB, "dockerfiles", "")
}

func (r *CatalogRepo) Dockerfile(id string) (domain.DockerfileDefinition, bool) {
	return payloadOne[domain.DockerfileDefinition](r.DB, "dockerfiles", id)
}

func (r *CatalogRepo) PutDockerfile(v domain.DockerfileDefinition) error {
	if err := domain.ValidateDockerfile(v); err != nil {
		return err
	}
	v.UpdatedAt = time.Now().UTC()
	return upsert(r.DB, "dockerfiles", map[string]any{
		"id": v.ID, "name": v.Name, "payload": mustJSON(v), "updated_at": timePtr(v.UpdatedAt),
	})
}

func (r *CatalogRepo) DeleteDockerfile(id string) error {
	if _, ok := r.Dockerfile(id); !ok {
		return fmt.Errorf("dockerfile not found")
	}
	return r.DB.Table("dockerfiles").Where("id = ?", id).Delete(nil).Error
}

func (r *CatalogRepo) Environments() []domain.EnvironmentDefinition {
	return payloadList[domain.EnvironmentDefinition](r.DB, "environments", "")
}

func (r *CatalogRepo) Environment(id string) (domain.EnvironmentDefinition, bool) {
	return payloadOne[domain.EnvironmentDefinition](r.DB, "environments", id)
}

func (r *CatalogRepo) PutEnvironment(v domain.EnvironmentDefinition) error {
	if err := domain.ValidateEnvironment(v); err != nil {
		return err
	}
	v.UpdatedAt = time.Now().UTC()
	return upsert(r.DB, "environments", map[string]any{
		"id": v.ID, "name": v.Name, "payload": mustJSON(v), "updated_at": timePtr(v.UpdatedAt),
	})
}

func (r *CatalogRepo) DeleteEnvironment(id string) error {
	if _, ok := r.Environment(id); !ok {
		return fmt.Errorf("environment not found")
	}
	return r.DB.Table("environments").Where("id = ?", id).Delete(nil).Error
}

func (r *CatalogRepo) Sandboxes() []domain.SandboxProfile {
	return payloadList[domain.SandboxProfile](r.DB, "sandboxes", "")
}

func (r *CatalogRepo) Sandbox(id string) (domain.SandboxProfile, bool) {
	return payloadOne[domain.SandboxProfile](r.DB, "sandboxes", id)
}

func (r *CatalogRepo) PutSandbox(v domain.SandboxProfile) error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Name) == "" {
		return errors.New("sandbox id and name are required")
	}
	if err := domain.ValidateEnvironmentVariables(v.Environment); err != nil {
		return fmt.Errorf("sandbox environment: %w", err)
	}
	switch strings.TrimSpace(v.Driver) {
	case "local", "seatbelt", "bwrap", "docker":
	default:
		return fmt.Errorf("unsupported sandbox driver %q", v.Driver)
	}
	stored := normalizeSandboxProfile(v)
	if stored.Driver == "docker" && stored.DockerfileID == "" {
		return errors.New("docker sandbox requires a dockerfileId")
	}
	if stored.DockerfileID != "" {
		if _, ok := r.Dockerfile(stored.DockerfileID); !ok {
			return fmt.Errorf("dockerfile %q not found", stored.DockerfileID)
		}
	}
	if stored.EnvironmentID != "" {
		if _, ok := r.Environment(stored.EnvironmentID); !ok {
			return fmt.Errorf("environment %q not found", stored.EnvironmentID)
		}
	}
	return upsert(r.DB, "sandboxes", map[string]any{
		"id": stored.ID, "name": stored.Name, "payload": mustJSON(stored),
	})
}

func (r *CatalogRepo) DeleteSandbox(id string) error {
	if _, ok := r.Sandbox(id); !ok {
		return fmt.Errorf("sandbox not found")
	}
	return r.DB.Table("sandboxes").Where("id = ?", id).Delete(nil).Error
}

// ---------- Workflows ----------

// Workflows lists project SOPs plus stored generic templates. A leftover
// workflows-table row occupying a generic id is filtered out so the template
// stays authoritative. Generic templates always sort after the stored SOPs
// (master semantics: the stored list is sorted by UpdatedAt, generics are
// appended).
func (r *CatalogRepo) Workflows() []domain.WorkflowDefinition {
	generic := r.GenericWorkflows()
	reserved := make(map[string]bool, len(generic))
	for _, template := range generic {
		reserved[template.ID] = true
	}
	stored := payloadList[domain.WorkflowDefinition](r.DB, "workflows", "")
	out := make([]domain.WorkflowDefinition, 0, len(stored)+len(generic))
	for _, workflow := range stored {
		workflow = normalizeWorkflowDefinition(workflow)
		if reserved[workflow.ID] {
			continue
		}
		out = append(out, workflow)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return append(out, generic...)
}

// Workflow resolves a custom SOP or a generic_templates row. Generic ids stay
// reserved so a leftover workflows-table collision cannot shadow the template.
func (r *CatalogRepo) Workflow(id string) (domain.WorkflowDefinition, bool) {
	if wf, ok := r.GenericWorkflowByID(id); ok {
		return wf, true
	}
	workflow, ok := payloadOne[domain.WorkflowDefinition](r.DB, "workflows", id)
	if !ok {
		return domain.WorkflowDefinition{}, false
	}
	return normalizeWorkflowDefinition(workflow), ok
}

// storedWorkflow reads only the physical workflows table, ignoring the
// generic-template fallback. Writers use it for existence checks so a legacy
// colliding row stays writable and never conflicts with template revisions.
func (r *CatalogRepo) storedWorkflow(id string) (domain.WorkflowDefinition, bool) {
	workflow, ok := payloadOne[domain.WorkflowDefinition](r.DB, "workflows", id)
	if !ok {
		return domain.WorkflowDefinition{}, false
	}
	return normalizeWorkflowDefinition(workflow), ok
}

func (r *CatalogRepo) PutWorkflow(v domain.WorkflowDefinition) error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Name) == "" {
		return errors.New("workflow id and name are required")
	}
	if err := domain.ValidateWorkflowPreparation(v); err != nil {
		return err
	}
	if err := domain.ValidateWorkflowMetricStages(v); err != nil {
		return err
	}
	v = normalizeWorkflowDefinition(v)
	if strings.TrimSpace(v.SandboxProfileID) == "" {
		v.SandboxProfileID = "local"
	}
	if _, ok := r.Sandbox(v.SandboxProfileID); !ok {
		return fmt.Errorf("sandbox profile %q not found", v.SandboxProfileID)
	}
	now := time.Now().UTC()
	old, ok := r.storedWorkflow(v.ID)
	if ok {
		v.ProjectID = domain.KeepCatalogProjectID(v.ProjectID, old.ProjectID)
		if v.Revision != old.Revision {
			return fmt.Errorf("workflow revision conflict: current revision is %d", old.Revision)
		}
		v.Revision = old.Revision + 1
		v.CreatedAt = old.CreatedAt
		if v.Version == 0 {
			v.Version = old.Version
		}
	} else {
		v.Revision = 1
		v.CreatedAt = now
		if v.Version == 0 {
			v.Version = 1
		}
	}
	if err := r.validateWorkflowNotifications(v); err != nil {
		return err
	}
	if v.Status == "" {
		v.Status = "draft"
	}
	v.UpdatedAt = now
	if ok {
		if err := r.snapshotRevision("workflow", old.ID, old.Name, old.Version, old.Revision, old); err != nil {
			return err
		}
	}
	return upsert(r.DB, "workflows", map[string]any{
		"id": v.ID, "name": v.Name, "version": v.Version, "revision": v.Revision, "status": v.Status,
		"payload": mustJSON(v), "created_at": timePtr(v.CreatedAt), "updated_at": timePtr(v.UpdatedAt),
	})
}

// DeleteWorkflow removes a SOP definition. Historical runs keep their own
// WorkflowSnapshot, so already-finished runs stay fully readable afterwards.
func (r *CatalogRepo) DeleteWorkflow(id string) error {
	if _, ok := r.storedWorkflow(id); !ok {
		return fmt.Errorf("workflow not found")
	}
	return r.DB.Table("workflows").Where("id = ?", id).Delete(nil).Error
}

// ---------- Repositories ----------

func (r *CatalogRepo) Repositories() []domain.Repository {
	out := payloadList[domain.Repository](r.DB, "repositories", "")
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectID == out[j].ProjectID {
			return out[i].Name < out[j].Name
		}
		return out[i].ProjectID < out[j].ProjectID
	})
	return out
}

func (r *CatalogRepo) Repository(id string) (domain.Repository, bool) {
	return payloadOne[domain.Repository](r.DB, "repositories", id)
}

func (r *CatalogRepo) PutRepository(v domain.Repository) error {
	if _, ok := r.projects.Project(v.ProjectID); !ok {
		return fmt.Errorf("project not found")
	}
	now := time.Now().UTC()
	if old, ok := r.Repository(v.ID); ok {
		v.CreatedAt = old.CreatedAt
	} else {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	return upsert(r.DB, "repositories", map[string]any{
		"id": v.ID, "project_id": v.ProjectID, "name": v.Name, "payload": mustJSON(v),
		"created_at": timePtr(v.CreatedAt), "updated_at": timePtr(v.UpdatedAt),
	})
}

func (r *CatalogRepo) DeleteRepository(id string) error {
	return r.DB.Table("repositories").Where("id = ?", id).Delete(nil).Error
}

// ---------- Sync config / Schedule jobs ----------

func (r *CatalogRepo) SyncConfig() domain.SyncConfig {
	cfg, _ := payloadOne[domain.SyncConfig](r.DB, "sync_config", mysqlSyncConfigID)
	return cfg
}

func (r *CatalogRepo) PutSyncConfig(cfg domain.SyncConfig) error {
	current := r.SyncConfig()
	cfg.PlaybookSeedRev = current.PlaybookSeedRev
	return upsert(r.DB, "sync_config", map[string]any{
		"id": mysqlSyncConfigID, "payload": mustJSON(cfg), "updated_at": timePtr(time.Now().UTC()),
	})
}

func (r *CatalogRepo) ScheduleJobs() []domain.ScheduleJob {
	out := payloadList[domain.ScheduleJob](r.DB, "schedule_jobs", "")
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *CatalogRepo) ScheduleJob(id string) (domain.ScheduleJob, bool) {
	return payloadOne[domain.ScheduleJob](r.DB, "schedule_jobs", id)
}

func (r *CatalogRepo) PutScheduleJob(v domain.ScheduleJob) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row scheduleJobRow
		var old domain.ScheduleJob
		err := r.locking(tx).Where("id = ?", v.ID).First(&row).Error
		if err == nil {
			if err := json.Unmarshal(row.Payload, &old); err != nil {
				return err
			}
		} else if err != gorm.ErrRecordNotFound {
			return err
		}
		if !(old.LastStatus == "running" && v.LastStatus != "running") {
			if err := r.notifications.validateNotificationBindings(tx, v.ProjectID, v.Notifications, true); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		if old.ID != "" {
			v.CreatedAt = old.CreatedAt
		} else {
			v.CreatedAt = now
		}
		v.UpdatedAt = now
		if strings.TrimSpace(v.Cron) == "" && v.IntervalSec < 30 {
			v.IntervalSec = 30
		}
		v.NotificationSnapshot = domain.CloneNotificationBindings(old.NotificationSnapshot)
		if v.LastStatus == "running" && (old.LastStatus != "running" || old.LastRunAt.UnixMilli() != v.LastRunAt.UnixMilli()) {
			v.NotificationSnapshot = domain.CloneNotificationBindings(v.Notifications)
		}
		enabled := 0
		if v.Enabled {
			enabled = 1
		}
		if err := upsert(tx, "schedule_jobs", map[string]any{"id": v.ID, "name": v.Name, "enabled": enabled, "payload": mustJSON(v), "updated_at": timePtr(v.UpdatedAt)}); err != nil {
			return err
		}
		trigger := map[string]string{"success": "succeeded", "failed": "failed"}[v.LastStatus]
		if old.ID != "" && old.LastStatus == "running" && trigger != "" && !v.LastRunAt.IsZero() {
			result := v.NotificationResult
			if result == "" {
				result = v.LastAssistantMessage
			}
			return r.notifications.enqueueTaskNotifications(tx, "schedule_job", v.ID, v.ProjectID, v.Name, trigger, v.LastRunAt, old.NotificationSnapshot, result, v.LastError)
		}
		return nil
	})
}

func (r *CatalogRepo) DeleteScheduleJob(id string) error {
	if _, ok := r.ScheduleJob(id); !ok {
		return fmt.Errorf("schedule job not found")
	}
	return r.DB.Table("schedule_jobs").Where("id = ?", id).Delete(nil).Error
}

// ---------- Definition revisions ----------

const maxRevisionsPerEntity = 20

// snapshotRevision archives the previous version of a definition before it is
// overwritten.
func (r *CatalogRepo) snapshotRevision(kind, entityID, name string, version, revision int, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	item := domain.DefinitionRevision{
		ID:        fmt.Sprintf("%s-%s-%d-%d-%d", kind, entityID, version, revision, time.Now().UTC().UnixNano()),
		Kind:      kind,
		EntityID:  entityID,
		Version:   version,
		Revision:  revision,
		Name:      name,
		Summary:   fmt.Sprintf("%s v%d #%d", name, version, revision),
		Payload:   raw,
		CreatedAt: time.Now().UTC(),
	}
	if err := upsert(r.DB, "definition_revisions", map[string]any{
		"id": item.ID, "kind": item.Kind, "entity_id": item.EntityID, "version": item.Version,
		"revision": item.Revision, "name": item.Name, "payload": mustJSON(item), "created_at": timePtr(item.CreatedAt),
	}); err != nil {
		return err
	}
	var dropped []string
	// MySQL 要求 OFFSET 必须搭配 LIMIT（master 原实现为
	// LIMIT 18446744073709551615 OFFSET ?）；gorm 在仅有 Offset 时会生成
	// 非法 SQL。sqlite 不接受超出 int64 的 LIMIT 字面量，故按方言分别处理。
	if r.isMySQL() {
		if err := r.DB.Raw(
			"SELECT id FROM definition_revisions WHERE kind = ? AND entity_id = ? "+
				"ORDER BY created_at DESC LIMIT 18446744073709551615 OFFSET ?",
			kind, entityID, maxRevisionsPerEntity).Scan(&dropped).Error; err != nil {
			return err
		}
	} else {
		if err := r.DB.Table("definition_revisions").Select("id").
			Where("kind = ? AND entity_id = ?", kind, entityID).
			Order("created_at DESC").
			Limit(math.MaxInt64).Offset(maxRevisionsPerEntity).
			Scan(&dropped).Error; err != nil {
			return err
		}
	}
	for _, id := range dropped {
		if err := r.DB.Table("definition_revisions").Where("id = ?", id).Delete(nil).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *CatalogRepo) Revisions(kind, entityID string) []domain.DefinitionRevision {
	out := payloadList[domain.DefinitionRevision](r.DB, "definition_revisions", "kind = ? AND entity_id = ?", kind, entityID)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (r *CatalogRepo) Revision(id string) (domain.DefinitionRevision, bool) {
	return payloadOne[domain.DefinitionRevision](r.DB, "definition_revisions", id)
}

func (r *CatalogRepo) RestoreWorkflowRevision(id string) (domain.WorkflowDefinition, error) {
	rev, ok := r.Revision(id)
	if !ok || rev.Kind != "workflow" {
		return domain.WorkflowDefinition{}, fmt.Errorf("revision not found")
	}
	var wf domain.WorkflowDefinition
	if err := json.Unmarshal(rev.Payload, &wf); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	current, found := r.Workflow(wf.ID)
	if !found {
		return domain.WorkflowDefinition{}, fmt.Errorf("workflow %s not found", wf.ID)
	}
	wf.Revision = current.Revision
	return wf, r.PutWorkflow(wf)
}

func (r *CatalogRepo) RestoreAgentRevision(id string) (domain.AgentDefinition, error) {
	rev, ok := r.Revision(id)
	if !ok || rev.Kind != "agent" {
		return domain.AgentDefinition{}, fmt.Errorf("revision not found")
	}
	var agent domain.AgentDefinition
	if err := json.Unmarshal(rev.Payload, &agent); err != nil {
		return domain.AgentDefinition{}, err
	}
	if current, found := r.Agent(agent.ID); found {
		agent.Version = current.Version + 1
	}
	return agent, r.PutAgent(agent)
}

// ---------- Production SOP ids ----------

const (
	issueFixWorkflowID    = "issue-fix-production"
	requirementWorkflowID = "requirement-development-production"
)

// DefaultWorkflowID returns the production SOP to bind when an issue has none.
// Gongfeng issues use the repair loop; TAPD / local requirements use delivery.
func DefaultWorkflowID(source, kind string) string {
	if source == "gongfeng" || kind == "issue" {
		return issueFixWorkflowID
	}
	return requirementWorkflowID
}

// isRequirement reports whether an issue is a requirement (delivery) rather than
// a defect/repair item.
func isRequirement(source, kind string) bool {
	return source != "gongfeng" && kind != "issue"
}

// bindDefaultWorkflow assigns the SOP an issue should run when it has none yet.
func bindDefaultWorkflow(v *domain.Issue, projectWorkflowID, projectIssueWorkflowID string) {
	if v.Source == "gongfeng" && v.Kind == "" {
		v.Kind = "issue"
	}
	if v.WorkflowID != "" {
		return
	}
	if isRequirement(v.Source, v.Kind) {
		if wf := strings.TrimSpace(projectWorkflowID); wf != "" {
			v.WorkflowID = wf
			return
		}
	} else if wf := strings.TrimSpace(projectIssueWorkflowID); wf != "" {
		v.WorkflowID = wf
		return
	} else if wf := strings.TrimSpace(projectWorkflowID); wf != "" && isGenericWorkflowID(wf) {
		// Projects defaulting to a built-in generic template (e.g. the
		// LoopForge backend delivery SOP a new project starts with) run their
		// synced Gongfeng issues on it too; projects bound to a production SOP
		// keep the production default below.
		v.WorkflowID = wf
		return
	}
	v.WorkflowID = DefaultWorkflowID(v.Source, v.Kind)
}

// isGenericWorkflowID reports whether the id is one of the platform generic
// templates stored in generic_templates.
func isGenericWorkflowID(id string) bool {
	switch strings.TrimSpace(id) {
	case GenericBackendDeliveryWorkflowID, GenericProductDeliveryWorkflowID, GenericFrontendDeliveryWorkflowID:
		return true
	default:
		return false
	}
}

// ProjectIssueWorkflowID is the conventional per-project defect SOP id.
func ProjectIssueWorkflowID(projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return ""
	}
	return projectID + "-issue-fix"
}

func normalizeSandboxProfile(profile domain.SandboxProfile) domain.SandboxProfile {
	profile.Driver = strings.TrimSpace(profile.Driver)
	profile.DockerfileID = strings.TrimSpace(profile.DockerfileID)
	profile.EnvironmentID = strings.TrimSpace(profile.EnvironmentID)
	switch profile.Driver {
	case "local":
		profile.DockerfileID = ""
		profile.ReadOnlyRootFS = false
	case "seatbelt", "bwrap":
		profile.DockerfileID = ""
		profile.ReadOnlyRootFS = false
	case "docker":
		profile.EnvironmentID = ""
	}
	return profile
}

func normalizeWorkflowDefinition(workflow domain.WorkflowDefinition) domain.WorkflowDefinition {
	if strings.TrimSpace(workflow.SandboxProfileID) == "" {
		workflow.SandboxProfileID = "local"
	}
	for index := range workflow.Nodes {
		node := &workflow.Nodes[index]
		if node.Type == "command" {
			node.Type = "script"
			if strings.TrimSpace(node.Script) == "" {
				node.Script = node.Command
			}
		}
		if node.Type == "script" {
			if strings.TrimSpace(node.ScriptLanguage) == "" {
				node.ScriptLanguage = "shell"
			}
			node.Command = ""
		}
	}
	return workflow
}

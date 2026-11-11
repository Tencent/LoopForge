package repo

import (
	"agentflow/internal/domain"
	"time"
)

// gorm row models. These describe the physical tables shared by MySQL
// (mysql_schema.sql) and the sqlite test database (AutoMigrate). Domain
// entities live in a JSON payload column; only filterable/locking fields are
// real columns.
//
// Time fields are deliberately named Created/Updated (not CreatedAt/UpdatedAt)
// so gorm never auto-fills them; every writer sets them explicitly.

const (
	mysqlSyncConfigID = "default"
	mysqlSeedMarkerID = -1
)

type userRow struct {
	ID           int64      `gorm:"column:id;primaryKey;autoIncrement"`
	StaffID      int        `gorm:"column:staff_id"`
	LoginName    string     `gorm:"column:login_name;uniqueIndex:uq_users_login;size:64"`
	ChineseName  string     `gorm:"column:chinese_name;size:128"`
	DeptID       int        `gorm:"column:dept_id"`
	DeptName     string     `gorm:"column:dept_name;size:128"`
	PlatformRole string     `gorm:"column:platform_role;size:32"`
	Created      *time.Time `gorm:"column:created_at"`
	Updated      *time.Time `gorm:"column:updated_at"`
}

func (userRow) TableName() string { return "users" }

type userConfigRow struct {
	UserID                       int64      `gorm:"column:user_id;primaryKey"`
	GitToken                     *string    `gorm:"column:git_token;type:text"`
	GongfengRefreshToken         *string    `gorm:"column:gongfeng_refresh_token;type:text"`
	GongfengTokenExpiresAt       *time.Time `gorm:"column:gongfeng_token_expires_at"`
	CodeBuddyAPIKey              *string    `gorm:"column:codebuddy_api_key;type:text"`
	CodeBuddyInternetEnvironment string     `gorm:"column:codebuddy_internet_environment;size:64"`
	CodexAPIKey                  *string    `gorm:"column:codex_api_key;type:text"`
	CodexBaseURL                 *string    `gorm:"column:codex_base_url;type:text"`
	ClaudeCodeAPIKey             *string    `gorm:"column:claude_code_api_key;type:text"`
	ClaudeCodeBaseURL            *string    `gorm:"column:claude_code_base_url;type:text"`
	AgentCLICredentials          *string    `gorm:"column:agent_cli_credentials;type:text"`
	Created                      *time.Time `gorm:"column:created_at"`
	Updated                      *time.Time `gorm:"column:updated_at"`
}

func (userConfigRow) TableName() string { return "user_configs" }

type apiTokenRow struct {
	ID         string     `gorm:"column:id;primaryKey;size:64"`
	UserID     int64      `gorm:"column:user_id;index:idx_api_tokens_user"`
	Name       string     `gorm:"column:name;size:128"`
	Prefix     string     `gorm:"column:prefix;size:24"`
	TokenHash  string     `gorm:"column:token_hash;uniqueIndex:uq_api_tokens_hash;size:64"`
	LastUsedAt *time.Time `gorm:"column:last_used_at"`
	ExpiresAt  *time.Time `gorm:"column:expires_at"`
	Created    time.Time  `gorm:"column:created_at"`
	RevokedAt  *time.Time `gorm:"column:revoked_at"`
}

func (apiTokenRow) TableName() string { return "api_tokens" }

type projectRow struct {
	BusinessDomainID string     `gorm:"column:business_domain_id;size:64;not null;default:'';index:idx_projects_business_domain"`
	ID               string     `gorm:"column:id;primaryKey;size:64"`
	ProjectKey       string     `gorm:"column:project_key;index:idx_projects_key;size:64"`
	Name             string     `gorm:"column:name;size:255"`
	OwnerUserID      *int64     `gorm:"column:owner_user_id;index:idx_projects_owner"`
	Payload          []byte     `gorm:"column:payload;type:json"`
	Created          *time.Time `gorm:"column:created_at"`
	Updated          *time.Time `gorm:"column:updated_at"`
}

func (projectRow) TableName() string { return "projects" }

type projectMemberRow struct {
	ProjectID string     `gorm:"column:project_id;primaryKey:idx_project_members_project;size:64"`
	UserID    int64      `gorm:"column:user_id;primaryKey:idx_project_members_user"`
	Role      string     `gorm:"column:role;size:32"`
	Created   *time.Time `gorm:"column:created_at"`
}

func (projectMemberRow) TableName() string { return "project_members" }

type repositoryRow struct {
	ID        string     `gorm:"column:id;primaryKey;size:64"`
	ProjectID string     `gorm:"column:project_id;index:idx_repos_project;size:64"`
	Name      string     `gorm:"column:name;size:255"`
	Payload   []byte     `gorm:"column:payload;type:json"`
	Created   *time.Time `gorm:"column:created_at"`
	Updated   *time.Time `gorm:"column:updated_at"`
}

func (repositoryRow) TableName() string { return "repositories" }

type agentRow struct {
	ID      string     `gorm:"column:id;primaryKey;size:64"`
	Name    string     `gorm:"column:name;size:255"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Updated *time.Time `gorm:"column:updated_at"`
}

func (agentRow) TableName() string { return "agents" }

type ruleRow struct {
	ID      string     `gorm:"column:id;primaryKey;size:64"`
	Name    string     `gorm:"column:name;size:255"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Updated *time.Time `gorm:"column:updated_at"`
}

func (ruleRow) TableName() string { return "rules" }

type skillRow struct {
	ID      string     `gorm:"column:id;primaryKey;size:64"`
	Name    string     `gorm:"column:name;size:255"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Updated *time.Time `gorm:"column:updated_at"`
}

func (skillRow) TableName() string { return "skills" }

// skillFileRow stores one binary skill file. Content lives in a MEDIUMBLOB
// column so binary payloads never bloat the skills payload JSON.
type skillFileRow struct {
	SkillID  string     `gorm:"column:skill_id;primaryKey;size:64"`
	Path     string     `gorm:"column:path;primaryKey;size:512"`
	Content  []byte     `gorm:"column:content;type:mediumblob"`
	Size     int64      `gorm:"column:size"`
	SHA256   string     `gorm:"column:sha256;size:64"`
	UnixMode uint32     `gorm:"column:unix_mode"`
	Updated  *time.Time `gorm:"column:updated_at"`
}

func (skillFileRow) TableName() string { return "skill_files" }

type mcpFileRow struct {
	MCPID    string     `gorm:"column:mcp_id;primaryKey;size:64"`
	Path     string     `gorm:"column:path;primaryKey;size:512"`
	Content  []byte     `gorm:"column:content;type:mediumblob"`
	Size     int64      `gorm:"column:size"`
	SHA256   string     `gorm:"column:sha256;size:64"`
	UnixMode uint32     `gorm:"column:unix_mode"`
	Updated  *time.Time `gorm:"column:updated_at"`
}

func (mcpFileRow) TableName() string { return "mcp_files" }

type mcpServerRow struct {
	ID      string     `gorm:"column:id;primaryKey;size:64"`
	Name    string     `gorm:"column:name;size:255"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Updated *time.Time `gorm:"column:updated_at"`
}

func (mcpServerRow) TableName() string { return "mcp_servers" }

type executorRow struct {
	ID      string `gorm:"column:id;primaryKey;size:64"`
	Name    string `gorm:"column:name;size:255"`
	Kind    string `gorm:"column:kind;size:32"`
	Payload []byte `gorm:"column:payload;type:json"`
}

func (executorRow) TableName() string { return "executors" }

type dockerfileRow struct {
	ID      string     `gorm:"column:id;primaryKey;size:64"`
	Name    string     `gorm:"column:name;size:255"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Updated *time.Time `gorm:"column:updated_at"`
}

func (dockerfileRow) TableName() string { return "dockerfiles" }

type environmentRow struct {
	ID      string     `gorm:"column:id;primaryKey;size:64"`
	Name    string     `gorm:"column:name;size:255"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Updated *time.Time `gorm:"column:updated_at"`
}

func (environmentRow) TableName() string { return "environments" }

type sandboxRow struct {
	ID      string `gorm:"column:id;primaryKey;size:64"`
	Name    string `gorm:"column:name;size:255"`
	Payload []byte `gorm:"column:payload;type:json"`
}

func (sandboxRow) TableName() string { return "sandboxes" }

type workflowRow struct {
	ID       string     `gorm:"column:id;primaryKey;size:64"`
	Name     string     `gorm:"column:name;size:255"`
	Version  int        `gorm:"column:version"`
	Revision int        `gorm:"column:revision"`
	Status   string     `gorm:"column:status;index:idx_workflows_status;size:32"`
	Payload  []byte     `gorm:"column:payload;type:json"`
	Created  *time.Time `gorm:"column:created_at"`
	Updated  *time.Time `gorm:"column:updated_at"`
}

func (workflowRow) TableName() string { return "workflows" }

type syncConfigRow struct {
	ID      string     `gorm:"column:id;primaryKey;size:32"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Updated *time.Time `gorm:"column:updated_at"`
}

func (syncConfigRow) TableName() string { return "sync_config" }

type scheduleJobRow struct {
	ID      string     `gorm:"column:id;primaryKey;size:64"`
	Name    string     `gorm:"column:name;size:255"`
	Enabled int        `gorm:"column:enabled"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Updated *time.Time `gorm:"column:updated_at"`
}

func (scheduleJobRow) TableName() string { return "schedule_jobs" }

type sharedScheduleVersionRow struct {
	ID           string     `gorm:"column:id;primaryKey;size:64"`
	SharedTaskID string     `gorm:"column:shared_task_id;size:64;uniqueIndex:idx_shared_schedule_version"`
	Version      int        `gorm:"column:version;uniqueIndex:idx_shared_schedule_version"`
	Payload      []byte     `gorm:"column:payload;type:json"`
	Created      *time.Time `gorm:"column:created_at"`
}

func (sharedScheduleVersionRow) TableName() string { return "shared_schedule_versions" }

type issueRow struct {
	ID             string     `gorm:"column:id;primaryKey;size:64"`
	ProjectID      string     `gorm:"column:project_id;index:idx_issues_project_status;size:64"`
	Number         int        `gorm:"column:number"`
	Title          string     `gorm:"column:title;size:512"`
	Status         string     `gorm:"column:status;index:idx_issues_project_status,priority:2;size:32"`
	Kind           string     `gorm:"column:kind;size:32"`
	Source         string     `gorm:"column:source;index:idx_issues_source_ext;size:32"`
	WorkflowID     string     `gorm:"column:workflow_id;size:64"`
	AssigneeUserID *int64     `gorm:"column:assignee_user_id;index:idx_issues_assignee_user"`
	ExternalID     string     `gorm:"column:external_id;index:idx_issues_source_ext,priority:2;size:128"`
	Payload        []byte     `gorm:"column:payload;type:json"`
	Created        *time.Time `gorm:"column:created_at"`
	Updated        *time.Time `gorm:"column:updated_at"`
}

func (issueRow) TableName() string { return "issues" }

type issueCommentRow struct {
	ID      string     `gorm:"column:id;primaryKey;size:64"`
	IssueID string     `gorm:"column:issue_id;index:idx_comments_issue;size:64"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Created *time.Time `gorm:"column:created_at"`
}

func (issueCommentRow) TableName() string { return "issue_comments" }

type taskRow struct {
	ID      string     `gorm:"column:id;primaryKey;size:64"`
	IssueID string     `gorm:"column:issue_id;index:idx_tasks_issue;size:64"`
	RunID   string     `gorm:"column:run_id;index:idx_tasks_run;size:64"`
	Status  string     `gorm:"column:status;size:32"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Created *time.Time `gorm:"column:created_at"`
	Updated *time.Time `gorm:"column:updated_at"`
}

func (taskRow) TableName() string { return "tasks" }

type runRow struct {
	Kind               string     `gorm:"column:kind;size:32"`
	ID                 string     `gorm:"column:id;primaryKey;size:64"`
	WorkflowID         string     `gorm:"column:workflow_id;index:idx_runs_workflow_updated;size:64"`
	Status             string     `gorm:"column:status;index:idx_runs_status;index:idx_runs_quota_bucket,priority:2;size:32"`
	QuotaBucket        *string    `gorm:"column:quota_bucket;size:128;index:idx_runs_quota_bucket,priority:1"`
	QuotaProjectID     *string    `gorm:"column:quota_project_id;size:64"`
	QuotaParentRunID   *string    `gorm:"column:quota_parent_run_id;size:64"`
	QuotaExempt        *bool      `gorm:"column:quota_exempt"`
	AdmissionState     string     `gorm:"column:admission_state;size:32"`
	QueueEntered       *time.Time `gorm:"column:queue_entered_at;index:idx_runs_quota_bucket,priority:3"`
	Revision           int        `gorm:"column:revision"`
	Input              string     `gorm:"column:input;type:text"`
	HumanContext       string     `gorm:"column:human_context;type:text"`
	CancelRequested    bool       `gorm:"column:cancel_requested"`
	PauseRequested     bool       `gorm:"column:pause_requested"`
	SandboxID          string     `gorm:"column:sandbox_id;size:64"`
	Repositories       []byte     `gorm:"column:repositories;type:json"`
	Repository         []byte     `gorm:"column:repository;type:json"`
	Snapshot           []byte     `gorm:"column:snapshot;type:json"`
	ExecutionContext   []byte     `gorm:"column:execution_context;type:json"`
	UsageJSON          []byte     `gorm:"column:usage_json;type:json"`
	ToolCalls          int        `gorm:"column:tool_calls"`
	TelemetrySequence  int64      `gorm:"column:telemetry_sequence"`
	LogSequence        int64      `gorm:"column:log_sequence"`
	OwnerInstanceID    string     `gorm:"column:owner_instance_id;size:255"`
	LeaseUntil         *time.Time `gorm:"column:lease_until"`
	Generation         int        `gorm:"column:execution_generation"`
	WorkspaceHost      string     `gorm:"column:workspace_host;size:255"`
	WorkspaceRoot      string     `gorm:"column:workspace_root;size:1024"`
	WorkspaceEditorID  int64      `gorm:"column:workspace_editor_id"`
	WorkspaceEditUntil *time.Time `gorm:"column:workspace_edit_until"`
	WorkspaceCleaned   *time.Time `gorm:"column:workspace_cleaned_at"`
	CleanupNotBefore   *time.Time `gorm:"column:workspace_cleanup_not_before"`
	Created            *time.Time `gorm:"column:created_at"`
	Updated            *time.Time `gorm:"column:updated_at"`
}

func (runRow) TableName() string { return "runs" }

type runNodeRow struct {
	RunID   string `gorm:"column:run_id;primaryKey;size:64"`
	NodeID  string `gorm:"column:node_id;primaryKey;size:64"`
	Status  string `gorm:"column:status;index:idx_run_nodes_status;size:32"`
	Attempt int    `gorm:"column:attempt"`
	Payload []byte `gorm:"column:payload;type:json"`
}

func (runNodeRow) TableName() string { return "run_nodes" }

type runEventRow struct {
	RunID   string     `gorm:"column:run_id;primaryKey;size:64"`
	Seq     int64      `gorm:"column:seq;primaryKey"`
	EventID string     `gorm:"column:event_id;size:64"`
	NodeID  string     `gorm:"column:node_id;size:64"`
	Type    string     `gorm:"column:type;size:64"`
	Message string     `gorm:"column:message;type:text"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Ts      *time.Time `gorm:"column:ts"`
}

func (runEventRow) TableName() string { return "run_events" }

type runTelemetryRow struct {
	RunID   string `gorm:"column:run_id;primaryKey;size:64"`
	Seq     int64  `gorm:"column:seq;primaryKey"`
	Payload []byte `gorm:"column:payload;type:json"`
}

func (runTelemetryRow) TableName() string { return "run_telemetry" }

type runLogRow struct {
	RunID   string `gorm:"column:run_id;primaryKey;size:64"`
	Seq     int64  `gorm:"column:seq;primaryKey"`
	Payload []byte `gorm:"column:payload;type:json"`
}

func (runLogRow) TableName() string { return "run_logs" }

type runArtifactRow struct {
	ID            string     `gorm:"column:id;primaryKey;size:64"`
	RunID         string     `gorm:"column:run_id;index:idx_artifacts_run;size:64"`
	NodeID        string     `gorm:"column:node_id;size:64"`
	Attempt       int        `gorm:"column:attempt"`
	Name          string     `gorm:"column:name;size:128"`
	Path          string     `gorm:"column:path;size:512"`
	MediaType     string     `gorm:"column:media_type;size:128"`
	SizeBytes     int64      `gorm:"column:size_bytes"`
	SHA256        string     `gorm:"column:sha256;size:64"`
	RepositoryDir string     `gorm:"column:repository_dir;size:128"`
	Content       string     `gorm:"column:content;type:text"`
	Created       *time.Time `gorm:"column:created_at"`
}

func (runArtifactRow) TableName() string { return "run_artifacts" }

type runEvalRow struct {
	ID        string     `gorm:"column:id;primaryKey;size:64"`
	RunID     string     `gorm:"column:run_id;index:idx_run_evals_run_created,priority:1;size:64"`
	Status    string     `gorm:"column:status;size:32"`
	Score     int        `gorm:"column:score"`
	Passed    bool       `gorm:"column:passed"`
	Model     string     `gorm:"column:model;size:128"`
	CreatedBy int64      `gorm:"column:created_by"`
	Payload   []byte     `gorm:"column:payload;type:json"`
	Created   *time.Time `gorm:"column:created_at;index:idx_run_evals_run_created,priority:2"`
	Finished  *time.Time `gorm:"column:finished_at"`
}

func (runEvalRow) TableName() string { return "run_evals" }

type runPreviewRow struct {
	ID                string     `gorm:"column:id;primaryKey;size:64"`
	RunID             string     `gorm:"column:run_id;uniqueIndex:uq_run_previews_run;size:64"`
	ProjectID         string     `gorm:"column:project_id;index:idx_run_previews_project;size:64"`
	Kind              string     `gorm:"column:kind;size:32"`
	Status            string     `gorm:"column:status;index:idx_run_previews_status_expiry;size:32"`
	SourcePath        string     `gorm:"column:source_path;size:1024"`
	StorageKey        string     `gorm:"column:storage_key;size:1024"`
	ContentSHA256     string     `gorm:"column:content_sha256;size:64"`
	CompressedSize    int64      `gorm:"column:compressed_size"`
	ExpandedSize      int64      `gorm:"column:expanded_size"`
	FileCount         int        `gorm:"column:file_count"`
	EntryFile         string     `gorm:"column:entry_file;size:512"`
	SPAFallback       bool       `gorm:"column:spa_fallback"`
	BackendURL        string     `gorm:"column:backend_url;size:1024"`
	OwnerRunnerID     string     `gorm:"column:owner_runner_id;size:64"`
	ScreenshotKey     string     `gorm:"column:screenshot_key;size:1024"`
	ErrorText         string     `gorm:"column:error_text;type:text"`
	UploadTokenHash   string     `gorm:"column:upload_token_hash;size:64"`
	UploadMaxBytes    int64      `gorm:"column:upload_max_bytes"`
	UploadExpiresAt   *time.Time `gorm:"column:upload_expires_at"`
	ArchiveLeaseUntil *time.Time `gorm:"column:archive_lease_until"`
	LastAccessedAt    *time.Time `gorm:"column:last_accessed_at"`
	ExpiresAt         *time.Time `gorm:"column:expires_at;index:idx_run_previews_status_expiry,priority:2"`
	Created           *time.Time `gorm:"column:created_at;index:idx_run_previews_project,priority:2"`
	Updated           *time.Time `gorm:"column:updated_at"`
}

func (runPreviewRow) TableName() string { return "run_previews" }

type previewSessionRow struct {
	ID             string     `gorm:"column:id;primaryKey;size:64"`
	PreviewID      string     `gorm:"column:preview_id;index:idx_preview_sessions_preview;size:64"`
	TokenHash      string     `gorm:"column:token_hash;uniqueIndex:uq_preview_sessions_hash;size:64"`
	BackendURL     string     `gorm:"column:backend_url;size:1024"`
	ExpiresAt      *time.Time `gorm:"column:expires_at"`
	Created        *time.Time `gorm:"column:created_at"`
	LastAccessedAt *time.Time `gorm:"column:last_accessed_at"`
	RevokedAt      *time.Time `gorm:"column:revoked_at"`
}

func (previewSessionRow) TableName() string { return "preview_sessions" }

type previewBlobRow struct {
	SHA256  string     `gorm:"column:sha256;primaryKey;size:64"`
	Content []byte     `gorm:"column:content;type:longblob"`
	Size    int64      `gorm:"column:size"`
	Created *time.Time `gorm:"column:created_at"`
}

func (previewBlobRow) TableName() string { return "preview_blobs" }

type mrDeliveryStateRow struct {
	MRProject    string     `gorm:"column:mr_project;primaryKey;size:255"`
	MRIID        int        `gorm:"column:mr_iid;primaryKey"`
	MRURL        string     `gorm:"column:mr_url;size:512"`
	State        string     `gorm:"column:state;index:idx_mr_delivery_refresh;size:32"`
	TargetBranch string     `gorm:"column:target_branch;size:255"`
	IssueID      string     `gorm:"column:issue_id;index:idx_mr_delivery_issue;size:64"`
	MergedAt     *time.Time `gorm:"column:merged_at;index:idx_mr_delivery_merged_at"`
	CheckedAt    *time.Time `gorm:"column:checked_at"`
	Created      *time.Time `gorm:"column:created_at"`
	Updated      *time.Time `gorm:"column:updated_at"`
}

func (mrDeliveryStateRow) TableName() string { return "mr_delivery_states" }

type definitionRevisionRow struct {
	ID       string     `gorm:"column:id;primaryKey;size:128"`
	Kind     string     `gorm:"column:kind;index:idx_rev_entity;size:32"`
	EntityID string     `gorm:"column:entity_id;index:idx_rev_entity,priority:2;size:64"`
	Version  int        `gorm:"column:version"`
	Revision int        `gorm:"column:revision"`
	Name     string     `gorm:"column:name;size:255"`
	Payload  []byte     `gorm:"column:payload;type:json"`
	Created  *time.Time `gorm:"column:created_at"`
}

func (definitionRevisionRow) TableName() string { return "definition_revisions" }

type runnerRow struct {
	ID               string     `gorm:"column:id;primaryKey;size:64"`
	Name             string     `gorm:"column:name;size:255"`
	Status           string     `gorm:"column:status;index:idx_runners_status;size:32"`
	OS               string     `gorm:"column:os;size:32"`
	Arch             string     `gorm:"column:arch;size:32"`
	RunnerVersion    string     `gorm:"column:runner_version;size:64"`
	Labels           []byte     `gorm:"column:labels;type:json"`
	MaxConcurrency   int        `gorm:"column:max_concurrency"`
	RunLimit         *int       `gorm:"column:run_limit"`
	OwnerUserID      *int64     `gorm:"column:owner_user_id;index:idx_runners_owner"`
	SessionTokenHash string     `gorm:"column:session_token_hash;index:idx_runners_session;size:64"`
	SessionExpiresAt *time.Time `gorm:"column:session_expires_at"`
	Runtimes         []byte     `gorm:"column:runtimes;type:json"`
	LastSeenAt       *time.Time `gorm:"column:last_seen_at"`
	Created          *time.Time `gorm:"column:created_at"`
	Updated          *time.Time `gorm:"column:updated_at"`
}

func (runnerRow) TableName() string { return "runners" }

type runnerTaskRow struct {
	ID           string     `gorm:"column:id;primaryKey;size:64"`
	RunnerID     string     `gorm:"column:runner_id;index:idx_runner_tasks_runner;size:64"`
	Provider     string     `gorm:"column:provider;index:idx_runner_tasks_dispatch,priority:2;size:64"`
	Status       string     `gorm:"column:status;index:idx_runner_tasks_dispatch;index:idx_runner_tasks_lease;size:32"`
	LeaseOwner   string     `gorm:"column:lease_owner;size:64"`
	LeaseUntil   *time.Time `gorm:"column:lease_until;index:idx_runner_tasks_lease,priority:2"`
	DesiredState string     `gorm:"column:desired_state;size:32"`
	Attempt      int        `gorm:"column:attempt"`
	MaxAttempts  int        `gorm:"column:max_attempts"`
	Payload      []byte     `gorm:"column:payload;type:json"`
	Created      *time.Time `gorm:"column:created_at"`
	Updated      *time.Time `gorm:"column:updated_at"`
}

func (runnerTaskRow) TableName() string { return "runner_tasks" }

type runnerTaskEventRow struct {
	TaskID string     `gorm:"column:task_id;primaryKey;size:64"`
	Seq    int64      `gorm:"column:seq;primaryKey"`
	Type   string     `gorm:"column:type;size:32"`
	Data   string     `gorm:"column:data;type:text"`
	Ts     *time.Time `gorm:"column:ts"`
}

func (runnerTaskEventRow) TableName() string { return "runner_task_events" }

type userRunnerPrefRow struct {
	UserID          int64      `gorm:"column:user_id;primaryKey"`
	DefaultRunnerID string     `gorm:"column:default_runner_id;size:64"`
	Updated         *time.Time `gorm:"column:updated_at"`
}

func (userRunnerPrefRow) TableName() string { return "user_runner_prefs" }

type assistantSessionRow struct {
	ID        string     `gorm:"column:id;primaryKey;size:64"`
	UserID    int64      `gorm:"column:user_id;index:idx_assistant_sessions_user_updated"`
	ProjectID string     `gorm:"column:project_id;size:64"`
	Title     string     `gorm:"column:title;size:128"`
	Model     string     `gorm:"column:model;size:128"`
	Source    string     `gorm:"column:source;size:32;index:idx_assistant_sessions_source"`
	TaskID    string     `gorm:"column:task_id;size:64"`
	Payload   []byte     `gorm:"column:payload;type:json"`
	Created   *time.Time `gorm:"column:created_at"`
	Updated   *time.Time `gorm:"column:updated_at;index:idx_assistant_sessions_user_updated,priority:2"`
}

func (assistantSessionRow) TableName() string { return "assistant_sessions" }

type assistantTaskRow struct {
	ID          string     `gorm:"column:id;primaryKey;size:64"`
	UserID      int64      `gorm:"column:user_id;index:idx_assistant_tasks_user"`
	ProjectID   string     `gorm:"column:project_id;size:64"`
	Name        string     `gorm:"column:name;size:255"`
	Enabled     int        `gorm:"column:enabled;index:idx_assistant_tasks_due"`
	IntervalSec int        `gorm:"column:interval_sec"`
	LastRunAt   *time.Time `gorm:"column:last_run_at;index:idx_assistant_tasks_due,priority:2"`
	Payload     []byte     `gorm:"column:payload;type:json"`
	Created     *time.Time `gorm:"column:created_at"`
	Updated     *time.Time `gorm:"column:updated_at"`
}

func (assistantTaskRow) TableName() string { return "assistant_tasks" }

type engineInstanceRow struct {
	InstanceID    string    `gorm:"column:instance_id;primaryKey;size:255"`
	Role          string    `gorm:"column:role;size:32"`
	ControlURL    string    `gorm:"column:control_url;size:512"`
	WorkspaceHost string    `gorm:"column:workspace_host;size:255"`
	HeartbeatAt   time.Time `gorm:"column:heartbeat_at"`
	LeaseUntil    time.Time `gorm:"column:lease_until;index:idx_engine_instances_live"`
}

func (engineInstanceRow) TableName() string { return "engine_instances" }

type genericTemplateRow struct {
	ID      string     `gorm:"column:id;primaryKey;size:64"`
	Name    string     `gorm:"column:name;size:255"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Updated *time.Time `gorm:"column:updated_at"`
}

func (genericTemplateRow) TableName() string { return "generic_templates" }

type chatRoomRow struct {
	ID        string     `gorm:"column:id;primaryKey;size:64"`
	ProjectID string     `gorm:"column:project_id;index:idx_chat_rooms_project;size:64"`
	Name      string     `gorm:"column:name;size:128"`
	CreatedBy int64      `gorm:"column:created_by"`
	Payload   []byte     `gorm:"column:payload;type:json"`
	Created   *time.Time `gorm:"column:created_at"`
	Updated   *time.Time `gorm:"column:updated_at"`
}

func (chatRoomRow) TableName() string { return "chat_rooms" }

type chatMessageRow struct {
	ID      string `gorm:"column:id;primaryKey;size:64"`
	RoomID  string `gorm:"column:room_id;index:idx_chat_messages_room;size:64"`
	UserID  int64  `gorm:"column:user_id"`
	Kind    string `gorm:"column:kind;size:32"`
	IssueID string `gorm:"column:issue_id;size:64"`
	// ThreadID is empty for the main timeline.
	ThreadID string     `gorm:"column:thread_id;size:64;index:idx_chat_messages_thread"`
	Payload  []byte     `gorm:"column:payload;type:json"`
	Created  *time.Time `gorm:"column:created_at;index:idx_chat_messages_room,priority:2"`
}

func (chatMessageRow) TableName() string { return "chat_messages" }

type requirementSpaceRow struct {
	ID        string     `gorm:"column:id;primaryKey;size:64"`
	ProjectID string     `gorm:"column:project_id;index:idx_requirement_spaces_project;size:64"`
	IssueID   string     `gorm:"column:issue_id;index:idx_requirement_spaces_issue;size:64"`
	Payload   []byte     `gorm:"column:payload;type:json"`
	Created   *time.Time `gorm:"column:created_at"`
	Updated   *time.Time `gorm:"column:updated_at"`
}

func (requirementSpaceRow) TableName() string { return "requirement_spaces" }

type spaceMemberRow struct {
	SpaceID string     `gorm:"column:space_id;primaryKey;size:64"`
	UserID  int64      `gorm:"column:user_id;primaryKey;autoIncrement:false"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Created *time.Time `gorm:"column:created_at"`
}

func (spaceMemberRow) TableName() string { return "space_members" }

type spaceArtifactRow struct {
	ID      string     `gorm:"column:id;primaryKey;size:64"`
	SpaceID string     `gorm:"column:space_id;index:idx_space_artifacts_space;size:64"`
	Kind    string     `gorm:"column:kind;size:32"`
	Version int        `gorm:"column:version"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Created *time.Time `gorm:"column:created_at"`
	Updated *time.Time `gorm:"column:updated_at"`
}

func (spaceArtifactRow) TableName() string { return "space_artifacts" }

type spaceArtifactRevisionRow struct {
	ArtifactID string     `gorm:"column:artifact_id;primaryKey;size:64"`
	Version    int        `gorm:"column:version;primaryKey;autoIncrement:false"`
	SpaceID    string     `gorm:"column:space_id;size:64"`
	Payload    []byte     `gorm:"column:payload;type:json"`
	Created    *time.Time `gorm:"column:created_at"`
}

func (spaceArtifactRevisionRow) TableName() string { return "space_artifact_revisions" }

type spaceThreadRow struct {
	ID      string     `gorm:"column:id;primaryKey;size:64"`
	SpaceID string     `gorm:"column:space_id;index:idx_space_threads_space;size:64"`
	RunID   string     `gorm:"column:run_id;size:64"`
	Payload []byte     `gorm:"column:payload;type:json"`
	Created *time.Time `gorm:"column:created_at"`
}

func (spaceThreadRow) TableName() string { return "space_threads" }

type knowledgeSubmissionRow struct {
	ID         string     `gorm:"column:id;primaryKey;size:64"`
	ProjectID  string     `gorm:"column:project_id;index:idx_knowledge_submissions_project;size:64"`
	RunID      string     `gorm:"column:run_id;index:idx_knowledge_submissions_run;size:64"`
	ArtifactID string     `gorm:"column:artifact_id;index:idx_knowledge_submissions_artifact;size:64"`
	Status     string     `gorm:"column:status;index:idx_knowledge_submissions_status;size:32"`
	Payload    []byte     `gorm:"column:payload;type:json"`
	Created    *time.Time `gorm:"column:created_at"`
	Updated    *time.Time `gorm:"column:updated_at"`
}

func (knowledgeSubmissionRow) TableName() string { return "knowledge_submissions" }

// 内置知识引擎控制面（主库，KnowledgeRepo）。向量配置列由 028 脚本补建；
// gorm 的增量 DDL 建得出普通列，这里注册后新装也能带全列。
type knowledgeBaseRow struct {
	ID                  string     `gorm:"column:id;primaryKey;size:64"`
	ProjectID           *string    `gorm:"column:project_id;size:64;index:idx_knowledge_bases_project;uniqueIndex:uq_knowledge_bases_project_layer"`
	Name                string     `gorm:"column:name;size:255"`
	Layer               string     `gorm:"column:layer;size:32;uniqueIndex:uq_knowledge_bases_project_layer"`
	OwnerUserID         int64      `gorm:"column:owner_user_id"`
	VectorEnabled       bool       `gorm:"column:vector_enabled"`
	EmbeddingModel      string     `gorm:"column:embedding_model;size:255"`
	EmbeddingDim        int        `gorm:"column:embedding_dim"`
	QdrantCollection    string     `gorm:"column:qdrant_collection;size:255"`
	SedimentPublishMode string     `gorm:"column:sediment_publish_mode;size:16"`
	SedimentRules       []byte     `gorm:"column:sediment_rules;type:json"`
	Created             *time.Time `gorm:"column:created_at"`
	Updated             *time.Time `gorm:"column:updated_at"`
}

func (knowledgeBaseRow) TableName() string { return "knowledge_bases" }

// knowledgeTombstoneRow 是撤回墓碑（控制面）。主键是项目加内容指纹；不再
// 保存正文，只保留出处与撤回元数据。
type knowledgeTombstoneRow struct {
	ProjectID    string     `gorm:"column:project_id;primaryKey;size:64"`
	Fingerprint  string     `gorm:"column:fingerprint;primaryKey;size:64"`
	BaseID       string     `gorm:"column:base_id;size:64"`
	SourceID     string     `gorm:"column:source_id;size:64"`
	Path         string     `gorm:"column:path;size:512"`
	SubmissionID string     `gorm:"column:submission_id;size:64"`
	Title        string     `gorm:"column:title;size:512"`
	Reason       string     `gorm:"column:reason;size:255"`
	CreatedBy    string     `gorm:"column:created_by;size:255"`
	Created      *time.Time `gorm:"column:created_at"`
}

func (knowledgeTombstoneRow) TableName() string { return "knowledge_tombstones" }

type knowledgeSourceRow struct {
	CredentialCiphertext string     `gorm:"column:credential_ciphertext;type:text"`
	ID                   string     `gorm:"column:id;primaryKey;size:64"`
	BaseID               string     `gorm:"column:base_id;size:64;index:idx_knowledge_sources_base"`
	Kind                 string     `gorm:"column:kind;size:32"`
	Name                 string     `gorm:"column:name;size:255"`
	GitURL               string     `gorm:"column:git_url;size:1024"`
	Branch               string     `gorm:"column:branch;size:255"`
	PathPrefix           string     `gorm:"column:path_prefix;size:512"`
	HeadCommit           string     `gorm:"column:head_commit;size:128"`
	HeadRevision         int64      `gorm:"column:head_revision"`
	Status               string     `gorm:"column:status;size:32;index:idx_knowledge_sources_status"`
	IndexStatus          string     `gorm:"column:index_status;size:32"`
	DocCount             int        `gorm:"column:doc_count"`
	LastError            string     `gorm:"column:last_error;type:text"`
	LastSyncedAt         *time.Time `gorm:"column:last_synced_at"`
	LeaseOwner           string     `gorm:"column:lease_owner;size:255"`
	LeaseUntil           *time.Time `gorm:"column:lease_until"`
}

func (knowledgeSourceRow) TableName() string { return "knowledge_sources" }

type knowledgeBindingRow struct {
	ProjectID  string `gorm:"column:project_id;primaryKey;size:64"`
	BaseID     string `gorm:"column:base_id;primaryKey;size:64"`
	Permission string `gorm:"column:permission;size:32"`
}

func (knowledgeBindingRow) TableName() string { return "project_knowledge_bindings" }

type knowledgeSyncJobRow struct {
	ID           int64      `gorm:"column:id;primaryKey;autoIncrement"`
	SourceID     string     `gorm:"column:source_id;size:64;index:idx_knowledge_sync_jobs_source"`
	Kind         string     `gorm:"column:kind;size:32"`
	Status       string     `gorm:"column:status;size:32"`
	FromCommit   string     `gorm:"column:from_commit;size:128"`
	ToCommit     string     `gorm:"column:to_commit;size:128"`
	ChangedCount int        `gorm:"column:changed_count"`
	DeletedCount int        `gorm:"column:deleted_count"`
	Log          string     `gorm:"column:log;type:text"`
	StartedAt    *time.Time `gorm:"column:started_at"`
	FinishedAt   *time.Time `gorm:"column:finished_at"`
}

func (knowledgeSyncJobRow) TableName() string { return "knowledge_sync_jobs" }

// knowledgeActivityRow 是 Agent 知识库工具调用记录。只属于控制面，按项目
// 保留最近若干条；写入在工具调用路径上尽力完成。
type knowledgeActivityRow struct {
	ID        int64      `gorm:"column:id;primaryKey;autoIncrement;index:idx_knowledge_activity_project,priority:2"`
	ProjectID string     `gorm:"column:project_id;size:64;index:idx_knowledge_activity_project,priority:1"`
	AgentID   string     `gorm:"column:agent_id;size:64"`
	UserID    int64      `gorm:"column:user_id"`
	RunID     string     `gorm:"column:run_id;size:64"`
	SessionID string     `gorm:"column:session_id;size:64"`
	Tool      string     `gorm:"column:tool;size:64"`
	Query     string     `gorm:"column:query_text;size:500"`
	Hits      int        `gorm:"column:hits"`
	OK        bool       `gorm:"column:ok"`
	Error     string     `gorm:"column:error_text;size:512"`
	LatencyMS int64      `gorm:"column:latency_ms"`
	At        *time.Time `gorm:"column:created_at"`
}

func (knowledgeActivityRow) TableName() string { return "knowledge_activity" }

// knowledgeWritebackRow 记录审核条目发布到 Git 来源的分支与合并请求。
// 同一审核条目一行；重试沿用同一分支。属于控制面，不存正文。
type knowledgeWritebackRow struct {
	ID           string     `gorm:"column:id;primaryKey;size:64"`
	SubmissionID string     `gorm:"column:submission_id;size:64;uniqueIndex:uq_knowledge_writebacks_submission"`
	SourceID     string     `gorm:"column:source_id;size:64;index:idx_knowledge_writebacks_source"`
	ProjectID    string     `gorm:"column:project_id;size:64"`
	Path         string     `gorm:"column:path;size:512"`
	Branch       string     `gorm:"column:branch;size:255"`
	Commit       string     `gorm:"column:commit;size:64"`
	MRURL        string     `gorm:"column:mr_url;size:1024"`
	MRProvider   string     `gorm:"column:mr_provider;size:32"`
	MRIID        int        `gorm:"column:mr_iid"`
	MRState      string     `gorm:"column:mr_state;size:32"`
	Status       string     `gorm:"column:status;size:32;index:idx_knowledge_writebacks_status,priority:1"`
	Note         string     `gorm:"column:note;type:text"`
	Error        string     `gorm:"column:error_text;type:text"`
	Attempts     int        `gorm:"column:attempts"`
	CheckedAt    *time.Time `gorm:"column:checked_at;index:idx_knowledge_writebacks_status,priority:2"`
	Created      *time.Time `gorm:"column:created_at"`
	Updated      *time.Time `gorm:"column:updated_at"`
}

func (knowledgeWritebackRow) TableName() string { return "knowledge_writebacks" }

// 内置知识引擎内容面（KnowledgeContentRepo，可能指向独立的 MySQL）。
type knowledgeDocumentRow struct {
	SourceID           string     `gorm:"column:source_id;primaryKey;size:64"`
	Path               string     `gorm:"column:path;primaryKey;size:512"`
	Title              string     `gorm:"column:title;size:512"`
	Body               string     `gorm:"column:body;type:mediumtext"`
	LineCount          int        `gorm:"column:line_count"`
	SHA256             string     `gorm:"column:sha256;size:64"`
	Commit             string     `gorm:"column:commit;size:128"`
	Revision           int64      `gorm:"column:revision"`
	Owner              string     `gorm:"column:owner;size:255"`
	DocStatus          string     `gorm:"column:doc_status;size:32"`
	FrontmatterUpdated *time.Time `gorm:"column:frontmatter_updated_at;type:date"`
	Updated            *time.Time `gorm:"column:updated_at"`
}

func (knowledgeDocumentRow) TableName() string { return "knowledge_documents" }

type knowledgeRevisionRow struct {
	ID       string     `gorm:"column:id;primaryKey;size:64"`
	SourceID string     `gorm:"column:source_id;size:64;uniqueIndex:uq_knowledge_revisions_path_rev"`
	Path     string     `gorm:"column:path;size:512;uniqueIndex:uq_knowledge_revisions_path_rev"`
	Revision int64      `gorm:"column:revision;uniqueIndex:uq_knowledge_revisions_path_rev"`
	Body     string     `gorm:"column:body;type:mediumtext"`
	Author   string     `gorm:"column:author;size:255"`
	Created  *time.Time `gorm:"column:created_at"`
}

func (knowledgeRevisionRow) TableName() string { return "knowledge_revisions" }

type knowledgeChunkRow struct {
	ID        int64  `gorm:"column:id;primaryKey;autoIncrement"`
	SourceID  string `gorm:"column:source_id;size:64;uniqueIndex:uq_knowledge_chunks_path_idx"`
	Path      string `gorm:"column:path;size:512;uniqueIndex:uq_knowledge_chunks_path_idx"`
	Idx       int    `gorm:"column:idx;uniqueIndex:uq_knowledge_chunks_path_idx"`
	Heading   string `gorm:"column:heading;size:512"`
	Title     string `gorm:"column:title;size:512"`
	StartLine int    `gorm:"column:start_line"`
	EndLine   int    `gorm:"column:end_line"`
	Commit    string `gorm:"column:commit;size:128"`
	Revision  int64  `gorm:"column:revision"`
	Text      string `gorm:"column:text;type:mediumtext"`
}

func (knowledgeChunkRow) TableName() string { return "knowledge_chunks" }

// knowledgeFeedbackRow 是知识中心、Main Bot 回答旁和 Agent 标记问题的统一
// 反馈。属于控制面，不存正文；Path 为空时表示只对回答反馈。
type knowledgeFeedbackRow struct {
	ID        string     `gorm:"column:id;primaryKey;size:64"`
	ProjectID string     `gorm:"column:project_id;size:64;index:idx_knowledge_feedback_project,priority:1"`
	BaseID    string     `gorm:"column:base_id;size:64"`
	SourceID  string     `gorm:"column:source_id;size:64"`
	Path      string     `gorm:"column:path;size:512"`
	Title     string     `gorm:"column:title;size:512"`
	Query     string     `gorm:"column:query_text;size:500"`
	Kind      string     `gorm:"column:kind;size:32"`
	Note      string     `gorm:"column:note;type:text"`
	Surface   string     `gorm:"column:surface;size:32"`
	Actor     string     `gorm:"column:actor;size:255"`
	UserID    int64      `gorm:"column:user_id"`
	AgentID   string     `gorm:"column:agent_id;size:64"`
	RunID     string     `gorm:"column:run_id;size:64"`
	SessionID string     `gorm:"column:session_id;size:64"`
	MessageID string     `gorm:"column:message_id;size:64"`
	Created   *time.Time `gorm:"column:created_at;index:idx_knowledge_feedback_project,priority:2"`
}

func (knowledgeFeedbackRow) TableName() string { return "knowledge_feedback" }

// knowledgeGapRow 是搜不到结果的查询。项目加归一化查询做主键，重复出现只
// 增加计数与最近时间，不无限追加行。
type knowledgeGapRow struct {
	ProjectID       string     `gorm:"column:project_id;primaryKey;size:64"`
	Normalized      string     `gorm:"column:normalized_query;primaryKey;size:500"`
	Query           string     `gorm:"column:query_text;size:500"`
	OccurrenceCount int        `gorm:"column:occurrence_count"`
	FirstSeen       *time.Time `gorm:"column:first_seen"`
	LastSeen        *time.Time `gorm:"column:last_seen;index:idx_knowledge_gaps_last_seen"`
}

func (knowledgeGapRow) TableName() string { return "knowledge_gaps" }

// knowledgeGovernanceIssueRow 是健康检查结果。按知识库整批替换；行上不存
// 正文，只保留定位和中文说明。
type knowledgeGovernanceIssueRow struct {
	ID         int64      `gorm:"column:id;primaryKey;autoIncrement"`
	BaseID     string     `gorm:"column:base_id;size:64;index:idx_knowledge_governance_issues_base,priority:1"`
	SourceID   string     `gorm:"column:source_id;size:64"`
	Path       string     `gorm:"column:path;size:512"`
	Title      string     `gorm:"column:title;size:512"`
	IssueType  string     `gorm:"column:issue_type;size:32;index:idx_knowledge_governance_issues_base,priority:2"`
	Line       int        `gorm:"column:line"`
	Summary    string     `gorm:"column:summary;type:text"`
	DetectedAt *time.Time `gorm:"column:detected_at"`
}

func (knowledgeGovernanceIssueRow) TableName() string { return "knowledge_governance_issues" }

// knowledgeGovernanceScanRow 是知识库最近一次扫描的摘要。扫描成功但 0 个
// 问题时仍保留这一行，面板才能显示“最近检查时间”。
type knowledgeGovernanceScanRow struct {
	BaseID     string     `gorm:"column:base_id;primaryKey;size:64"`
	ScannedAt  *time.Time `gorm:"column:scanned_at"`
	DocCount   int        `gorm:"column:doc_count"`
	IssueCount int        `gorm:"column:issue_count"`
}

func (knowledgeGovernanceScanRow) TableName() string { return "knowledge_governance_scans" }

// allModels lists every table for the sqlite test schema (AutoMigrate).
func allModels() []any {
	return []any{domain.BotUserBinding{}, domain.BotBindingChallenge{}, domain.BotConnection{}, domain.BotConversation{}, domain.BotInteraction{}, notificationBotRow{}, botCredentialRow{}, domain.NotificationDelivery{},
		userRow{}, localAccountRow{}, userConfigRow{}, authIntegrationRow{}, platformCodeBuddyKeyRow{}, apiTokenRow{}, genericTemplateRow{},
		insightSourceRow{}, insightExecutionRow{},
		projectRow{}, projectMemberRow{}, repositoryRow{}, businessDomainRow{}, businessDomainAssignmentRow{},
		agentRow{}, ruleRow{}, skillRow{}, skillFileRow{}, mcpServerRow{}, mcpFileRow{}, executorRow{},
		dockerfileRow{}, environmentRow{}, sandboxRow{},
		workflowRow{}, syncConfigRow{}, scheduleJobRow{}, sharedScheduleVersionRow{},
		issueRow{}, issueCommentRow{}, taskRow{},
		runAdmissionLockRow{}, runRow{}, runNodeRow{}, runEventRow{}, runTelemetryRow{}, runLogRow{}, runArtifactRow{}, runEvalRow{},
		runPreviewRow{}, previewSessionRow{}, previewBlobRow{}, WorkspaceCheckpoint{}, workspaceCheckpointChunk{},
		mrDeliveryStateRow{}, definitionRevisionRow{},
		runnerRow{}, runnerTaskRow{}, runnerTaskEventRow{}, userRunnerPrefRow{},
		assistantSessionRow{}, assistantTaskRow{}, assistantGenerationRow{},
		chatRoomRow{}, chatMessageRow{},
		requirementSpaceRow{}, spaceMemberRow{}, spaceArtifactRow{}, spaceArtifactRevisionRow{}, spaceThreadRow{},
		engineInstanceRow{},
		knowledgeSubmissionRow{},
		knowledgeBaseRow{}, knowledgeSourceRow{}, knowledgeBindingRow{}, knowledgeSyncJobRow{}, knowledgeActivityRow{}, knowledgeWritebackRow{}, knowledgeTombstoneRow{},
		knowledgeFeedbackRow{}, knowledgeGapRow{}, knowledgeGovernanceIssueRow{}, knowledgeGovernanceScanRow{},
		knowledgeDocumentRow{}, knowledgeRevisionRow{}, knowledgeChunkRow{},
		pageFeedbackRow{}, pageFeedbackImageRow{},
	}
}

// Package repo is the gorm-backed persistence layer of AgentFlow. MySQL is the
// only production datasource; tests open an in-process sqlite database through
// the same gorm code paths. There is no second in-memory implementation.
package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var (
	ErrRunDecisionStale  = errors.New("run changed before the decision was committed")
	ErrRunLeaseHeld      = errors.New("run lease is held by another instance")
	ErrRunLeaseLost      = errors.New("run lease ownership or generation changed")
	ErrRunWorkspaceBound = errors.New("run workspace is already bound to another host path")
)

// deps 是所有领域 Repo 共享的依赖（repo 层的共享依赖只有 gorm 句柄）。
type deps struct {
	DB *gorm.DB
}

func (d *deps) isMySQL() bool {
	return d.DB != nil && d.DB.Dialector.Name() == "mysql"
}

// sqlJSONText returns a portable "extract text at JSON path" expression.
// MySQL needs the JSON_UNQUOTE wrapper; sqlite's json_extract already returns
// unquoted text for string values.
func (d *deps) sqlJSONText(path string) string {
	if d.isMySQL() {
		return "JSON_UNQUOTE(JSON_EXTRACT(payload, ?))"
	}
	return "json_extract(payload, ?)"
}

// locking returns the query with a row-lock clause on MySQL. sqlite serializes
// writers already (single connection in tests), so no clause is needed there.
func (d *deps) locking(q *gorm.DB) *gorm.DB {
	if d.isMySQL() {
		return q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return q
}

// CatalogRepo owns the reusable definitions: agents, rules, skills, MCP
// servers, executors, dockerfiles, environments, sandboxes, workflows,
// repositories, sync config, schedule jobs and definition revisions.
type CatalogRepo struct {
	*deps
	projects      *ProjectsRepo
	notifications *NotificationsRepo
	*BotInteractionsRepo
}

// ProjectsRepo owns projects and their members.
type BusinessDomainsRepo struct {
	*deps
}

type ProjectsRepo struct {
	*deps
}

// IssuesRepo owns issues and issue comments.
type IssuesRepo struct {
	*deps
	mu       sync.Mutex
	projects *ProjectsRepo
	users    *UsersRepo
	tasks    *TasksRepo
}

// TasksRepo owns tasks (the cross-instance idempotency records).
type TasksRepo struct {
	*deps
}

// UsersRepo owns users and their saved credentials.
type UsersRepo struct {
	*deps
}

// TokensRepo owns API tokens.
type TokensRepo struct {
	*deps
}

// AssistantRepo owns Studio AI assistant conversations and scheduled prompts.
type AssistantRepo struct {
	*deps
	users         *UsersRepo
	notifications *NotificationsRepo
	*BotInteractionsRepo
}

// RunsRepo owns runs, their children (nodes/events/telemetry/logs/artifacts),
// run leases and workflow metrics.
type RunsRepo struct {
	*deps
	assistant     *AssistantRepo
	issues        *IssuesRepo
	catalog       *CatalogRepo
	notifications *NotificationsRepo
	*BotInteractionsRepo
}

// RunnersRepo owns the CLI runner registry, runner tasks and runner events.
type RunnersRepo struct {
	*deps
}

// DeliveryRepo owns MR delivery state and the delivery locking that follows.
type DeliveryRepo struct {
	*deps
	issues  *IssuesRepo
	tasks   *TasksRepo
	catalog *CatalogRepo
	runs    *RunsRepo
}

// AnalyticsRepo owns the read-only operational analytics view.
type AnalyticsRepo struct {
	*deps
	runs     *RunsRepo
	catalog  *CatalogRepo
	delivery *DeliveryRepo
	issues   *IssuesRepo
	tasks    *TasksRepo
	projects *ProjectsRepo
}

// Store 组合各领域 Repo。领域 Repo 以匿名指针嵌入（未导出类型）：包外仅能
// 通过提升的方法调用，与现有调用形态完全兼容（方法提升对未导出类型同样生效）。
type Store struct {
	*deps
	*CatalogRepo
	*ProjectsRepo
	*BusinessDomainsRepo
	*IssuesRepo
	*TasksRepo
	*UsersRepo
	*TokensRepo
	*AssistantRepo
	*RunsRepo
	*RunnersRepo
	*DeliveryRepo
	*AnalyticsRepo
	*PreviewsRepo
	*EngineInstancesRepo
	*RoomsRepo
	*KnowledgeRepo
	*NotificationsRepo
	*BotInteractionsRepo
}

// newStore wires the shared deps and every cross-domain pointer. Domains have
// no cycles, so a single construction pass is enough.
func newStore(db *gorm.DB) *Store {
	d := &deps{DB: db}
	s := &Store{deps: d}
	s.ProjectsRepo = &ProjectsRepo{deps: d}
	s.BusinessDomainsRepo = &BusinessDomainsRepo{deps: d}
	s.UsersRepo = &UsersRepo{deps: d}
	s.BotInteractionsRepo = &BotInteractionsRepo{deps: d}
	s.TokensRepo = &TokensRepo{deps: d}
	s.AssistantRepo = &AssistantRepo{deps: d, users: s.UsersRepo}
	s.TasksRepo = &TasksRepo{deps: d}
	s.NotificationsRepo = &NotificationsRepo{deps: d, users: s.UsersRepo, projects: s.ProjectsRepo, interactions: s.BotInteractionsRepo}
	s.AssistantRepo.notifications = s.NotificationsRepo
	s.CatalogRepo = &CatalogRepo{deps: d, projects: s.ProjectsRepo, notifications: s.NotificationsRepo}
	s.IssuesRepo = &IssuesRepo{deps: d, projects: s.ProjectsRepo, users: s.UsersRepo, tasks: s.TasksRepo}
	s.NotificationsRepo.issues = s.IssuesRepo
	s.RunsRepo = &RunsRepo{deps: d, assistant: s.AssistantRepo, issues: s.IssuesRepo, catalog: s.CatalogRepo, notifications: s.NotificationsRepo}
	s.RunnersRepo = &RunnersRepo{deps: d}
	s.DeliveryRepo = &DeliveryRepo{deps: d, issues: s.IssuesRepo, tasks: s.TasksRepo, catalog: s.CatalogRepo, runs: s.RunsRepo}
	s.AnalyticsRepo = &AnalyticsRepo{deps: d, runs: s.RunsRepo, catalog: s.CatalogRepo, delivery: s.DeliveryRepo, issues: s.IssuesRepo, tasks: s.TasksRepo, projects: s.ProjectsRepo}
	s.PreviewsRepo = &PreviewsRepo{deps: d}
	s.EngineInstancesRepo = &EngineInstancesRepo{deps: d}
	s.RoomsRepo = &RoomsRepo{deps: d}
	s.KnowledgeRepo = &KnowledgeRepo{deps: d}
	return s
}

// Open connects the store to MySQL, which is where all state lives. An empty
// DSN is a configuration error, not a reason to invent state somewhere else.
func Open(dsn string) (*Store, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("AGENTFLOW_MYSQL_DSN"))
	}
	if dsn == "" {
		return nil, errors.New("AGENTFLOW_MYSQL_DSN 未配置：MySQL 是唯一数据源，缺少 DSN 时拒绝启动")
	}
	if !strings.Contains(dsn, "charset=") {
		if strings.Contains(dsn, "?") {
			dsn += "&charset=utf8mb4&collation=utf8mb4_unicode_ci"
		} else {
			dsn += "?charset=utf8mb4&collation=utf8mb4_unicode_ci"
		}
	}
	if !strings.Contains(dsn, "parseTime=") {
		dsn += "&parseTime=true&loc=UTC"
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("mysql open: %w", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(32)
		sqlDB.SetMaxIdleConns(8)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
		if err := sqlDB.Ping(); err != nil {
			return nil, fmt.Errorf("mysql ping: %w", err)
		}
	}
	s := newStore(db)
	// Incremental DDL only (missing columns/tables/indexes) via gorm's
	// migrator. Schema baselines, data migrations and seed data are NOT run
	// from code: they are plain SQL scripts under migrations/, applied
	// manually by the operator.
	if err := autoMigrateIncremental(s.DB); err != nil {
		s.Close()
		return nil, fmt.Errorf("auto migrate: %w", err)
	}
	return s, nil
}

// Close releases the underlying connection pool. The gorm handle is kept so
// in-flight background goroutines fail gracefully ("sql: database is closed")
// instead of panicking on a nil handle.
func (s *Store) Close() {
	if s.DB == nil {
		return
	}
	if sqlDB, err := s.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

func (s *Store) StorageInfo() map[string]any {
	return map[string]any{
		"driver":   s.DB.Dialector.Name(),
		"database": true,
		"note":     "所有状态都在 MySQL：项目、Issue、SOP、运行记录、助手对话和定时任务都直接读写数据库。",
	}
}

func IssueBoundToTracker(issue domain.Issue) bool {
	if issue.Source != "gongfeng" && issue.Source != "tapd" {
		return false
	}
	if issue.ExternalRef == nil {
		return false
	}
	return strings.TrimSpace(issue.ExternalRef.ExternalID) != "" || issue.ExternalRef.ExternalIID != 0
}

// RestoreTrackerBinding fills Source/ExternalRef from a still-bound snapshot, or
// from a stable local id (tapd-… / gf-…) after a save wiped the pointer.
func RestoreTrackerBinding(issue *domain.Issue, existing domain.Issue) {
	if issue == nil || IssueBoundToTracker(*issue) {
		return
	}
	if IssueBoundToTracker(existing) {
		issue.Source = existing.Source
		issue.ExternalRef = existing.ExternalRef
		if strings.TrimSpace(issue.ID) == "" {
			issue.ID = existing.ID
		}
		return
	}
	id := strings.TrimSpace(issue.ID)
	if id == "" {
		id = strings.TrimSpace(existing.ID)
		issue.ID = id
	}
	if extID, ok := strings.CutPrefix(id, "tapd-"); ok && strings.TrimSpace(extID) != "" {
		issue.Source = "tapd"
		ref := domain.ExternalRef{Source: "tapd", ExternalID: extID, ExternalType: "story"}
		if existing.ExternalRef != nil {
			ref.ExternalURL = existing.ExternalRef.ExternalURL
			ref.ExternalState = existing.ExternalRef.ExternalState
		}
		issue.ExternalRef = &ref
		return
	}
	if _, iid := parseStoredGongfengIssueID(id); iid > 0 {
		issue.Source = "gongfeng"
		if existing.ExternalRef != nil {
			issue.ExternalRef = existing.ExternalRef
			return
		}
		issue.ExternalRef = &domain.ExternalRef{Source: "gongfeng", ExternalIID: iid, ExternalType: "issue"}
	}
}

func parseStoredGongfengIssueID(id string) (repo string, iid int) {
	body := strings.TrimPrefix(strings.TrimSpace(id), "gf-")
	if body == "" || body == strings.TrimSpace(id) {
		return "", 0
	}
	i := strings.LastIndex(body, "-")
	if i <= 0 {
		return "", 0
	}
	n := 0
	for _, ch := range body[i+1:] {
		if ch < '0' || ch > '9' {
			return "", 0
		}
		n = n*10 + int(ch-'0')
	}
	if n <= 0 {
		return "", 0
	}
	return body[:i], n
}

func cloneRunNodes(nodes map[string]*domain.NodeRun) map[string]*domain.NodeRun {
	if len(nodes) == 0 {
		return nil
	}
	out := make(map[string]*domain.NodeRun, len(nodes))
	for id, node := range nodes {
		if node == nil {
			continue
		}
		cp := *node
		out[id] = &cp
	}
	return out
}

func cloneRun(run domain.Run) domain.Run {
	run.Nodes = cloneRunNodes(run.Nodes)
	run.Events = append([]domain.RunEvent(nil), run.Events...)
	run.Telemetry = append([]domain.TelemetryEvent(nil), run.Telemetry...)
	run.Logs = append([]domain.LogRecord(nil), run.Logs...)
	run.Artifacts = append([]domain.Artifact(nil), run.Artifacts...)
	return run
}

func normalizeRefs(values []string, prefix string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimPrefix(v, prefix)
		v = strings.TrimSuffix(v, ".md")
		out = append(out, v)
	}
	return out
}

func sortSlice[T any](values []T, less func(a, b T) bool) {
	sort.Slice(values, func(i, j int) bool { return less(values[i], values[j]) })
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

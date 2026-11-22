package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"agentflow/internal/engine"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

// diagnosticsHandlers serves the read-only platform diagnostics page:
// deployment checks that previously required docker exec + SQL access.
type diagnosticsHandlers struct {
	*deps
}

type platformDiagnosticCheck struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Status   string `json:"status"` // ok | warn | error | info
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Hint     string `json:"hint,omitempty"`
	Command  string `json:"command,omitempty"`
}

type platformDiagnosticsView struct {
	GeneratedAt string                    `json:"generatedAt"`
	Checks      []platformDiagnosticCheck `json:"checks"`
}

const (
	diagnosticStatusOK    = "ok"
	diagnosticStatusWarn  = "warn"
	diagnosticStatusError = "error"
	diagnosticStatusInfo  = "info"
)

// diagnostics returns the deployment checks. Read-only: nothing is written and
// no migration is executed, matching the repository rule that operators run
// migrations/ by hand.
func (h *diagnosticsHandlers) diagnostics(c *gin.Context) {
	if !h.isPlatformAdmin(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以查看运维诊断"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, platformDiagnosticsView{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Checks:      h.collectDiagnostics(c.Request.Context()),
	})
}

func (h *diagnosticsHandlers) collectDiagnostics(ctx context.Context) []platformDiagnosticCheck {
	_, checks := h.databaseChecks(ctx)
	checks = append(checks, h.artifactChecks()...)
	checks = append(checks, h.browserChecks(ctx)...)
	checks = append(checks, h.runtimeChecks(ctx)...)
	return checks
}

func (h *diagnosticsHandlers) databaseChecks(ctx context.Context) (string, []platformDiagnosticCheck) {
	if h.repo == nil {
		return "", []platformDiagnosticCheck{{
			ID: "database.connection", Category: "database", Status: diagnosticStatusError,
			Title: "数据库未配置", Hint: "检查 AGENTFLOW_MYSQL_DSN 配置。",
		}}
	}
	driver := fmt.Sprint(h.repo.StorageInfo()["driver"])
	pingErr := errors.New("database handle unavailable")
	if sqlDB, err := h.repo.DB.DB(); err == nil {
		pingErr = sqlDB.PingContext(ctx)
	}
	if pingErr != nil {
		return driver, []platformDiagnosticCheck{{
			ID: "database.connection", Category: "database", Status: diagnosticStatusError,
			Title: "数据库连接失败", Detail: pingErr.Error(),
			Hint: "确认 MySQL 容器状态与 AGENTFLOW_MYSQL_DSN 配置。",
		}}
	}
	detail := driver
	if driver == "mysql" {
		var version, database string
		if row := h.repo.DB.WithContext(ctx).Raw("SELECT VERSION(), DATABASE()").Row(); row != nil {
			if scanErr := row.Scan(&version, &database); scanErr == nil {
				detail = fmt.Sprintf("%s · MySQL %s · %s", driver, version, database)
			}
		}
	}
	checks := []platformDiagnosticCheck{{
		ID: "database.connection", Category: "database", Status: diagnosticStatusOK,
		Title: "数据库连接正常", Detail: detail,
	}}
	if driver != "mysql" {
		return driver, append(checks, platformDiagnosticCheck{
			ID: "database.schema", Category: "database", Status: diagnosticStatusInfo,
			Title: "跳过 schema 检查", Detail: "当前存储驱动不是 MySQL；对象检查仅适用于 MySQL 部署。",
		})
	}
	return driver, append(checks, h.schemaChecks(ctx)...)
}

func (h *diagnosticsHandlers) schemaChecks(ctx context.Context) []platformDiagnosticCheck {
	checks := make([]platformDiagnosticCheck, 0, 2)
	checks = append(checks, h.observabilitySchemaCheck(ctx))
	checks = append(checks, h.artifactColumnCheck(ctx))
	return checks
}

// observabilitySchemaCheck verifies the generated columns the installer adds
// through migrations/001_run_obs_generated_columns.sql: without them the run
// telemetry endpoints fail with a raw "Unknown column" error.
func (h *diagnosticsHandlers) observabilitySchemaCheck(ctx context.Context) platformDiagnosticCheck {
	const id = "database.observability_columns"
	rows, err := h.repo.DB.WithContext(ctx).Raw(
		`SELECT table_name, column_name, extra FROM information_schema.columns
		  WHERE table_schema = DATABASE()
		    AND ((table_name = 'run_telemetry' AND column_name = 'adapter')
		      OR (table_name = 'run_logs' AND column_name = 'level'))`).Rows()
	if err != nil {
		return platformDiagnosticCheck{
			ID: id, Category: "database", Status: diagnosticStatusWarn,
			Title: "无法读取 schema 信息", Detail: err.Error(),
		}
	}
	defer rows.Close()
	found := map[string]string{}
	for rows.Next() {
		var table, column, extra string
		if scanErr := rows.Scan(&table, &column, &extra); scanErr == nil {
			found[table+"."+column] = strings.ToUpper(extra)
		}
	}
	var missing []string
	for _, key := range []string{"run_telemetry.adapter", "run_logs.level"} {
		extra, ok := found[key]
		switch {
		case !ok:
			missing = append(missing, key+" 缺失")
		case !strings.Contains(extra, "GENERATED"):
			missing = append(missing, key+" 存在但不是生成列")
		}
	}
	if len(missing) == 0 {
		return platformDiagnosticCheck{
			ID: id, Category: "database", Status: diagnosticStatusOK,
			Title: "观测 schema 已就绪", Detail: "run_telemetry.adapter / run_logs.level 生成列存在。",
		}
	}
	return platformDiagnosticCheck{
		ID: id, Category: "database", Status: diagnosticStatusWarn,
		Title: "观测 schema 未补齐", Detail: strings.Join(missing, "；"),
		Hint:    "安装器和 ./start.sh pull 会补齐 Compose 数据库；外部 MySQL 按 docs/deployment.md 手动执行迁移。",
		Command: "bash scripts/install-schema-extensions.sh",
	}
}

// artifactColumnCheck compares the artifact body column with the engine limit;
// a TEXT column is 64 KiB and rejects larger artifacts with MySQL error 1406.
func (h *diagnosticsHandlers) artifactColumnCheck(ctx context.Context) platformDiagnosticCheck {
	const id = "database.artifact_content"
	limit := diagnosticBytes(engine.ArtifactSizeLimit())
	dataType, ok, err := h.mysqlColumnType(ctx, "run_artifacts", "content")
	if err != nil {
		return platformDiagnosticCheck{
			ID: id, Category: "database", Status: diagnosticStatusWarn,
			Title: "无法读取产物列类型", Detail: err.Error(),
		}
	}
	if !ok {
		return platformDiagnosticCheck{
			ID: id, Category: "database", Status: diagnosticStatusWarn,
			Title: "缺少产物正文列", Detail: "information_schema 中找不到 run_artifacts.content。",
		}
	}
	switch dataType {
	case "tinytext", "text":
		return platformDiagnosticCheck{
			ID: id, Category: "database", Status: diagnosticStatusWarn,
			Title: "产物正文列容量不足", Detail: fmt.Sprintf("run_artifacts.content 为 %s（上限 64 KiB），小于引擎单产物上限 %s；更大的产物会让运行终态写库失败，节点停在 running。", strings.ToUpper(dataType), limit),
			Hint: "把该列手动扩宽为 MEDIUMTEXT（16 MiB）后重启平台；参见 issue #1092。",
		}
	case "mediumtext", "longtext":
		return platformDiagnosticCheck{
			ID: id, Category: "database", Status: diagnosticStatusOK,
			Title: "产物正文列容量充足", Detail: fmt.Sprintf("run_artifacts.content 为 %s，覆盖引擎单产物上限 %s。", strings.ToUpper(dataType), limit),
		}
	default:
		return platformDiagnosticCheck{
			ID: id, Category: "database", Status: diagnosticStatusInfo,
			Title: "产物正文列类型未识别", Detail: fmt.Sprintf("run_artifacts.content 为 %s；引擎单产物上限 %s。", strings.ToUpper(dataType), limit),
		}
	}
}

func (h *diagnosticsHandlers) artifactChecks() []platformDiagnosticCheck {
	return []platformDiagnosticCheck{{
		ID: "artifacts.size_limit", Category: "artifacts", Status: diagnosticStatusInfo,
		Title: "单产物大小上限", Detail: fmt.Sprintf("引擎接受单产物最大 %s；超过数据库列容量时终态写入失败，而不是产生一条产物。", diagnosticBytes(engine.ArtifactSizeLimit())),
		Hint: "产物体积较大时优先拆分文件；同时确认 run_artifacts.content 的列容量足够。",
	}}
}

func (h *diagnosticsHandlers) browserChecks(ctx context.Context) []platformDiagnosticCheck {
	if !runtime.BrowserEnabled() {
		return []platformDiagnosticCheck{{
			ID: "browser.agent_browser", Category: "browser", Status: diagnosticStatusInfo,
			Title: "Agent 浏览器已关闭", Detail: "AGENTFLOW_BROWSER=off。",
		}}
	}
	image, exists, err := runtime.BrowserSidecarImage(ctx)
	if err != nil {
		return []platformDiagnosticCheck{{
			ID: "browser.agent_browser", Category: "browser", Status: diagnosticStatusWarn,
			Title: "无法检查 Agent 浏览器镜像", Detail: err.Error(),
		}}
	}
	if exists {
		return []platformDiagnosticCheck{{
			ID: "browser.agent_browser", Category: "browser", Status: diagnosticStatusOK,
			Title: "Agent 浏览器 sidecar 镜像已就绪", Detail: image,
		}}
	}
	return []platformDiagnosticCheck{{
		ID: "browser.agent_browser", Category: "browser", Status: diagnosticStatusWarn,
		Title: "Agent 浏览器 sidecar 镜像尚未构建", Detail: "首次打开浏览器时会在平台容器内构建该镜像（Chromium + Playwright MCP，约 1 GB），需要访问 deb.debian.org 与 npm；构建超过请求超时会导致首次打开失败。",
		Hint:    "网络较慢时先预构建，网络良好时只需一次。",
		Command: "docker compose exec platform /app/bin/agentflow -browser-image",
	}}
}

func (h *diagnosticsHandlers) runtimeChecks(ctx context.Context) []platformDiagnosticCheck {
	checks := make([]platformDiagnosticCheck, 0, 2)
	if runtime.DockerReady() {
		checks = append(checks, platformDiagnosticCheck{
			ID: "runtime.docker", Category: "runtime", Status: diagnosticStatusOK,
			Title: "Docker 执行环境可用", Detail: "平台可以创建沙箱与 sidecar 容器。",
		})
	} else {
		checks = append(checks, platformDiagnosticCheck{
			ID: "runtime.docker", Category: "runtime", Status: diagnosticStatusWarn,
			Title: "Docker 不可用", Detail: "平台无法创建沙箱容器；Compose 部署需要挂载 /var/run/docker.sock。",
			Hint: "检查 Docker 守护进程与 compose.yaml 的 docker.sock 挂载。",
		})
	}
	if image := strings.TrimSpace(os.Getenv("AGENTFLOW_ISOLATED_IMAGE")); image != "" {
		if runtime.DockerImageExists(ctx, image) {
			checks = append(checks, platformDiagnosticCheck{
				ID: "runtime.isolated_image", Category: "runtime", Status: diagnosticStatusOK,
				Title: "隔离运行时镜像已就绪", Detail: image,
			})
		} else {
			checks = append(checks, platformDiagnosticCheck{
				ID: "runtime.isolated_image", Category: "runtime", Status: diagnosticStatusWarn,
				Title: "隔离运行时镜像不在本地", Detail: image,
				Hint: "首次运行沙箱时可能需要拉取或构建该镜像。",
			})
		}
	}
	return checks
}

func (h *diagnosticsHandlers) mysqlColumnType(ctx context.Context, table, column string) (string, bool, error) {
	var dataType string
	row := h.repo.DB.WithContext(ctx).Raw(
		`SELECT data_type FROM information_schema.columns
		  WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`,
		table, column).Row()
	if err := row.Scan(&dataType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.ToLower(strings.TrimSpace(dataType)), true, nil
}

func diagnosticBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

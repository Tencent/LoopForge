# LoopForge platform

The platform is based on the recorded upstream master in `upstream.json`. Preserve upstream architecture; keep public-specific adapters isolated. The original local workflow runtime lives in `local/`; follow `local/AGENTS.md` there. Never copy private deployment files, credentials, staff data, or internal infrastructure into this repository.

# AGENTS.md

本文件面向所有在仓库内工作的 AI agent 与人类贡献者，约定 repo 层与 handler 层的结构规则。
新增代码必须遵守；发现历史偏差时先归位再继续。

## repo 层（internal/repo）

每个领域一个 Repo，文件与方法一一归属：

| 领域 Repo | 拥有 |
|---|---|
| `CatalogRepo` | agents/rules/skills/MCP servers/executors/dockerfiles/environments/sandboxes/workflows/repositories/sync config/schedule jobs/definition revisions/generic templates |
| `ProjectsRepo` | projects、project members |
| `IssuesRepo` | issues、issue comments |
| `TasksRepo` | tasks（跨实例幂等记录） |
| `UsersRepo` | users、credentials、platform admins |
| `TokensRepo` | API tokens |
| `AssistantRepo` | Studio 助手会话、定时 prompts |
| `RunsRepo` | runs 及其子资源、run leases、workflow metrics、run evals |
| `RunnersRepo` | CLI runner 注册表、runner tasks/events |
| `DeliveryRepo` | MR delivery 状态与锁 |
| `AnalyticsRepo` | 只读运营分析视图 |
| `PreviewsRepo` | run previews |
| `EngineInstancesRepo` | engine instances |

规则：

1. **禁止在 `Store` 上新增领域方法。** `Store`（repo.go）只做两件事：
   - 组合各领域 Repo（匿名嵌入）与连接生命周期（`Open`/`Close`）；
   - 必须跨领域合并的视图（现已全部降级进领域 Repo，如
     `CatalogRepo.Workflow`/`Workflows` 合并 generic_templates）。
   - 合并视图的行为契约：`CatalogRepo.Workflows` 先按 `UpdatedAt` 排序
     workflows 表行，generic 模板恒追加在尾部（不得混入排序）；
     `Workflow(id)` generic 模板优先于同 ID 物理行；写路径
     （`PutWorkflow`/`DeleteWorkflow`）经 `storedWorkflow` 只看物理表。
2. 新实体先找所属领域 Repo；确实没有就新建一个 `XxxRepo`，并在 `newStore`
   中装配。领域间协作用 `newStore` 里的显式指针注入（如 `RunsRepo` 持有
   `issues`/`catalog`），不允许循环依赖。
3. 领域 Repo 的共享依赖只有 gorm 句柄（`deps`），不要往 `deps` 加别的。
4. 写一个文件挂多个领域 receiver 是坏味道；同一领域的方法可分布在多个文件
   （如 `runs.go`/`run_evals.go` 都属 `RunsRepo`），但文件不得为其他领域定义方法。

## 迁移与 seed（migrations/）

1. **代码不做迁移、不做 seed。** `repo.Open()` 只负责连接和 gorm
   AutoMigrate 的增量 DDL（补缺失的表/列/索引）。基线 schema、数据迁移
   （改数据、删行、删列）和 seed 数据一律写成 `migrations/` 下的 SQL
   脚本，由运维**手动执行**，部署时不会自动跑。
2. 每个分支自带自己的迁移脚本，不做任何自动化生成（没有 seedgen 之类的
   工具）。脚本命名 `NNN_描述.sql`，按序号递增。
3. 禁止新增 `ensureXxx`/`migrateXxx`/`seedXxx` 之类的启动自愈逻辑写入
   生产路径。测试专用的默认数据只存在于 `internal/repo/opentest.go`
   （`OpenTest`，仅测试调用，生产代码不得引用）。

## handler 层（internal/httpapi）

1. 每个领域一个 handler struct（`authHandlers`、`usersHandlers`、
   `catalogHandlers`、`issuesHandlers`、`tasksHandlers`、`runsHandlers`、
   `previewsHandlers`、`streamHandlers`、`runnersHandlers`、
   `scheduleHandlers`、`tokenHandlers`、`syncHandlers`、
   `deploymentMetricsHandlers`、`assistantHandlers`、`mcpHandlers`、
   `marketHandlers`…），在 `server.go` 的 `Server` 中以指针装配。
2. **新领域必须新建自己的 handler struct**，不要把方法挂到现有领域 handler
   或 `Server` 上。
3. 同一领域的多个文件允许（`run_eval.go`/`run_workspace.go` 属
   `runsHandlers`），但一个文件不得为别的领域 handler 定义方法。
   纯共享的包级 helper（无 receiver）可以放独立 helper 文件。
4. `Server` 只保留服务级端点与入口装配（`health`、`capabilities`、
   `openAPI`、`bootstrap`、`observabilitySpec`、`otel` 等）。
5. 跨领域协作走显式注入（如 `issuesHandlers` 持有 `tasks`/`stream`），
   与 repo 层同构。

## 历史整改记录

- 2026-09：`platform_admin.go` 全部并入 `UsersRepo`；`generic_store.go`
  模板 CRUD 并入 `CatalogRepo`；`run_preview.go` 拆出 `previewsHandlers`
  （runner 侧上传端点归还 `runners.go`）；`agent_tool_native.go` 的领域
  方法归还 `assistant.go`/`clarify_mcp.go`，只留包级 helper；
  `Workflow`/`Workflows` 合并视图降级进 `CatalogRepo`（写路径经
  `storedWorkflow` 只看物理表）；删除全部代码内 seed/数据迁移
  （`seed`/`ensureGenericDockerDefaults`/`ensurePlatformAdmins`/
  `migrateLegacyProjectTapd`），`Open` 只留连接 + gorm AutoMigrate，
  `migrations/` 清空为手动 SQL 脚本目录，测试默认数据移入
  `internal/repo/opentest.go`。

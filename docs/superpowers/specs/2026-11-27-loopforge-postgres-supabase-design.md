# LoopForge 本地部署 + MySQL→Postgres 移植设计

日期：2026-11-27
状态：已批准，待实施

## 目标

在 macOS 本机把腾讯 LoopForge 跑起来，数据库用本机已有的 Supabase Postgres（而不是 LoopForge 自带的 MySQL 容器）。

## 背景与关键事实

- LoopForge 的持久层写死了 MySQL：`gorm.io/driver/mysql`、DSN 形如 `user:pw@tcp(host:3306)/db`、`internal/repo` 里约 75 处 MySQL 专有 SQL、63 个 MySQL 迁移脚本、`compose.yaml` 带 mysql:8.4 服务。
- 官方 GHCR 镜像（`ghcr.io/tencent/loopforge:0.4.3`）是 MySQL-only，**不能**用来跑 Postgres 后端；它仍然可以作为 **沙箱运行时镜像**（沙箱不连库）。
- 本机 Supabase 是 Postgres 15.8，host 侧只有 Supavisor 暴露端口：`127.0.0.1:5432`（session mode）与 `127.0.0.1:6543`（transaction mode）。租户 id `cesar`，因此连接用户必须写成 `postgres.cesar`。
- 已验证：端口 5432 是 **session mode**（跨事务 backend pid 不变、`application_name` 可保持），可以安全跑 GORM/pgx 的 prepared statement。
- `postgres` 角色具备 CREATEDB/CREATEROLE（但非 superuser）。已建好独立库 `loopforge`。

## 决策（已与用户确认）

| 决策点 | 选择 |
| --- | --- |
| 范围 | 完整移植到 Postgres（非"能跑起来就行"的最小集） |
| 平台进程形态 | macOS 原生二进制，不构建 Docker 镜像 |
| 数据库落点 | Supabase 内新建独立 database `loopforge` |
| 沙箱运行时 | 沿用官方 GHCR 镜像 0.4.3 |
| 代码落点 | `/Users/caijinhai/Documents/projects/loopForge`，分支 `feat/postgres-supabase`，基线 `fd039e5` |

## 架构

```
┌───────────────────────────────────────────────┐
│ macOS host                                   │
│                                               │
│  agentflow (原生二进制)  ──HTTP──▶ :8090      │
│        │                                      │
│        │ DSN postgres://postgres.cesar:***@   │
│        │      127.0.0.1:5432/loopforge       │
│        ▼                                      │
│  supabase-pooler (session mode, 5432)        │
│        ▼                                      │
│  supabase-db  Postgres 15.8                   │
│        database: loopforge                    │
│                                               │
│  docker sandboxes ──▶ ghcr.io/tencent/        │
│                        loopforge:0.4.3        │
└───────────────────────────────────────────────┘
```

## 改动清单

### 1. 连接层

文件：`internal/repo/repo.go`、`cmd/agentflow/main.go`、`internal/httpapi/assistant_acceptance.go`

- DSN 按 scheme 分流：`postgres://` / `postgresql://` → `gorm.io/driver/postgres`；`user:pw@tcp(...)` → 保持现有 mysql 路径。
- 新增 `AGENTFLOW_DATABASE_URL` 环境变量与 `-database-url` flag；保留 `AGENTFLOW_MYSQL_DSN` 与 `-mysql-dsn` 兼容（老部署不受影响）。
- 连接池上限 10：`pool_max_conns=10`。理由：Supavisor session mode 下每个客户端连接独占一个真实 backend，LoopForge 默认连接池不限会造成 backend 膨胀。
- `repo.Open` 中 mysql 专属的 `charset=utf8mb4&collation=utf8mb4_unicode_ci` 追加逻辑只在 mysql 分支生效。

### 2. 方言抽象

文件：`internal/repo/repo.go`

- `deps` 增加 `isPostgres()`。
- `sqlJSONText` / `sqlPayloadJSONText` / `sqlExecutionContextJSONText` 增加 postgres 分支：
  - MySQL：`JSON_UNQUOTE(JSON_EXTRACT(payload, ?))`
  - Postgres：`payload::jsonb #>> '{path}'`（`{path}` 为常量路径段数组，来源必须是代码常量，不得来自用户输入）
- `locking()`：`clause.Locking{Strength: "UPDATE"}` 在 Postgres 同样生成 `FOR UPDATE`，无需分叉；仅需确认没有 MySQL 专属的 `LOCK IN SHARE MODE`。

### 3. SQL 改写

已定位的全部 MySQL 专有点：

| 位置 | MySQL | Postgres |
| --- | --- | --- |
| `internal/repo/analytics*.go`、`runs.go`、`assistant*.go`、`repo.go` 等（30+ 处） | `JSON_EXTRACT(col, '$.a.b')` | `col::jsonb #>> '{a,b}'` |
| 同上（16 处） | `JSON_UNQUOTE(JSON_EXTRACT(...))` | `col::jsonb #>> '{a,b}'`（`#>>` 本身返回 text） |
| `runs.go`、`analytics_dashboard.go`（7 处） | `JSON_OBJECT('k', v, ...)` | `jsonb_build_object('k', v, ...)` |
| `assistant.go`、`assistant_run_meta.go`（2 处） | `JSON_LENGTH(payload)` | `jsonb_array_length(payload::jsonb)` |
| `assistant_context.go` | `JSON_TABLE(payload,'$.messages[*]' COLUMNS (...))` | `jsonb_to_recordset(payload::jsonb -> 'messages') AS m(...)`（键名与列名不一致处显式改写为 `->>` 逐字段抽取） |
| `internal/repo/payload.go` | `INSERT IGNORE` / `ON DUPLICATE KEY UPDATE` | `INSERT ... ON CONFLICT DO NOTHING` / `ON CONFLICT ... DO UPDATE`（gorm `clause.OnConflict` 两边都支持） |
| `internal/repo/payload.go` 等 | `IFNULL(a, b)` | `COALESCE(a, b)` |
| `internal/repo/integration_lock.go` | `GET_LOCK(name, timeout)` / `RELEASE_LOCK(name)` | `pg_advisory_lock(hashtext(name))` / `pg_advisory_unlock(hashtext(name))`，超时语义用 `pg_try_advisory_lock` 轮询实现 |
| `internal/repo/assistant_generations.go`、`workspace_checkpoints.go` | `UTC_TIMESTAMP(3) - INTERVAL ? MICROSECOND` | `now() - (? \|\| ' microseconds')::interval` |
| `internal/repo/catalog.go` | `CONVERT(? USING utf8mb4) COLLATE utf8mb4_unicode_ci = ?` | 删除该模拟（Postgres 默认区分大小写，行为更正确）；保留等值判断本身 |
| `internal/repo/catalog.go` 之外 3 处 `BINARY` 比较 | MySQL 二进制比较 | 逐个确认语义，优先改 `=`/`lower()`；无法等价映射时改用 `bytea` 比较 |

约束：所有 Postgres 改写必须**真实执行过**，不能只编译通过。analytics / observability 页面的每条 SQL 都要在冒烟中跑到。

### 4. Schema

- `type:json` 列（`internal/repo/models.go` 中约 30 个 `Payload []byte` 字段）在 Postgres 必须是 `jsonb`：`json` 类型没有等值运算符，`ON CONFLICT` 与等值查询会失效。
  - 方案：新增方言感知列类型（实现 gorm `GormDBDataType`，mysql/sqlite → `json`，postgres → `jsonb`），随 model tag 使用；避免为 Postgres 单独维护一份模型。
- ~120 个模型逐个过 AutoMigrate，排查：索引名长度（Postgres 限 63 字符）、唯一索引、`NOT NULL DEFAULT`、保留字列名（gorm 自动加引号）。
- MySQL 生成列（observability / run_obs，见 `migrations/001_run_obs_generated_columns.sql` 与 `scripts/install-schema-extensions.sh`）→ PG15 `GENERATED ALWAYS AS (...) STORED`。
- Bot 消息表的 utf8mb4 处理（`internal/botinteraction/persistence.go`、`internal/repo/automigrate.go`）：Postgres 原生 UTF8，无需 charset 升级流程；保留 MySQL 分支原逻辑。

### 5. 迁移与种子

- `migrations/seed/001_public_catalog.sql`、`002_welcome_project.sql` → Postgres 版（`SET NAMES utf8mb4` 删除、`INSERT IGNORE` → `ON CONFLICT DO NOTHING`、JSON 字面量转义核对）。
- `scripts/install-observability-schema.sh`、`scripts/install-schema-extensions.sh` → Postgres 版（`information_schema` 查询语法通用，`table_schema=DATABASE()` 换成 `current_schema()`）。
- **已知缺口**：63 个历史升级迁移脚本（`migrations/0xx_*.sql`）保持 MySQL-only，本次不移植。它们只服务于老版本库升级；新建库走 AutoMigrate，不执行历史迁移。若日后需要从 MySQL 库迁移数据，需要单独做一遍迁移器（不在本次范围）。

### 6. 部署脚本

- `scripts/configure.sh`：生成 `.env` 时写入 Postgres DSN（`AGENTFLOW_DATABASE_URL`），密码从本机 Supabase `.env` 读取或交互输入；不再生成 MySQL 密码。
- `scripts/dev.sh`：不再 `docker compose up mysql`；改为「确保 database 存在 → 建表（AutoMigrate）→ 装生成列 → 灌种子 → 建 admin 账号 → 启动原生进程」。
- `compose.yaml`：移除 mysql service；`artifact-migration` 改用 `postgres:15` 客户端执行 SQL；platform 的 `AGENTFLOW_MYSQL_DSN` 改 `AGENTFLOW_DATABASE_URL`。
- `scripts/install.sh` / `start.sh`：安装流程从 MySQL 切换到 Postgres（本机已有 Supabase 时不再拉 mysql 镜像）。

### 7. 配置与密钥

- DSN 密码不落进 git：写入项目 `.env`（已在 `.gitignore`），从 `/Users/caijinhai/Documents/services/supabase/.env` 的 `POSTGRES_PASSWORD` 读取注入。
- `AGENTFLOW_SESSION_SECRET` 等沿用 `configure.sh` 现有生成逻辑。

## 语义差异（显式处理，不静默）

1. **大小写敏感性**：MySQL `utf8mb4_unicode_ci` 不区分大小写，Postgres 区分。
   - 面向用户的名称搜索：`LIKE` → `ILIKE`，保持交互体验一致。
   - ID 唯一性：维持区分大小写（比 MySQL 更严格，方向安全）。受影响的表需在实施时逐个确认。
2. **json → jsonb**：键序被重排、重复键被丢弃、白空格被规范化。业务不依赖键序，但等值与排序语义变化，需验证 upsert 路径。
3. **锁**：`GET_LOCK` 是连接级命名锁，`pg_advisory_lock` 同样是会话级，但键空间与原 MySQL 实例不同（换库后无影响）。

## 测试策略

- `go build ./...` 全量通过。
- `go test ./internal/repo/... ./internal/httpapi/...`：sqlite 路径不受影响；MySQL 集成用例按现有 `AGENTFLOW_TEST_MYSQL_DSN` 约定自动跳过。
- 新增 Postgres 集成用例：以 `AGENTFLOW_TEST_POSTGRES_DSN` 为开关，在独立测试库上跑核心 repo 读写、upsert、锁、JSON 投影路径。
- 冒烟（人工）：见验收标准 1–5。

## 验收标准

1. `agentflow` 原生进程监听 `127.0.0.1:8090`，`GET /api/health` 返回 200。
2. admin 账号可登录，可创建项目。
3. 内置 demo LOOP（script + human gate）完整跑完并产出 artifact。
4. analytics / observability 页面无 500，所有改写过的 SQL 真实执行过。
5. 重启进程后数据仍在（Postgres 持久化生效）。
6. `git status` 无意外产物；`.env` 与本地数据不进版本库。

## 风险

| 风险 | 应对 |
| --- | --- |
| 遗漏的 MySQL 专有 SQL 只在特定路径触发 | 冒烟覆盖主流程 + analytics 全页面；对每处改写做「旧语句 → 新语句」逐条登记核对 |
| AutoMigrate 在 Postgres 上因类型/索引失败 | 逐模型验证，必要时为 Postgres 单独补 DDL |
| Supavisor session mode backend 膨胀 | 连接池上限 10 |
| 大小写语义变化导致唯一性/搜索行为漂移 | 搜索改 ILIKE；唯一性逐表确认并在实施记录中列出 |
| 本机 Supabase 停机时平台不可用 | 接受（用户自有基础设施） |

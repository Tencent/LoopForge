# Postgres 集成测试

这份文档讲清楚 LoopForge 的 Postgres 部署怎么准备集成测试库、怎么跑测试、
哪些失败与 Postgres 移植无关。

仓库同时保留三种方言：MySQL（上游默认路径，一行不能删）、Postgres（本部署）、
sqlite（仅测试）。本文只讲 Postgres 一侧。

## 1. 两个库不要混

| 库 | 用途 | 由谁写 |
| --- | --- | --- |
| `loopforge` | 运行库 | 平台进程、部署脚本 |
| `loopforge_test` | 集成测试库 | 只由测试和测试准备步骤写 |

`internal/repo/opentest.go` 的 `OpenPostgresTest` 会检查 DSN 指向的库名，
库里没有 `test` 就直接让用例失败。所以 `AGENTFLOW_TEST_POSTGRES_URL` 指错了
不会静默写运行库，但会立刻报错。

## 2. 准备测试库：三步，顺序不能换

| 步骤 | 做什么 | 谁来做 |
| --- | --- | --- |
| 1 | 建库 `loopforge_test` 并改 owner | 运维，手工 `psql` |
| 2 | 平台打开一次 store，由 gorm AutoMigrate 建表 | 集成用例自己做（`OpenPostgresTest` → `Open`） |
| 3 | `scripts/install-schema-extensions-pg.sh` 加生成列和种子行 | 运维，手工跑 |

### 第 1 步：建库并改 owner

owner 变更不能省：Supabase 的 `public` schema 属于 `pg_database_owner`，
不改 owner，建表会报 `permission denied for schema public`。

```bash
cd /Users/caijinhai/Documents/projects/loopForge
set -a; . ./.env; set +a

export PGHOST="127.0.0.1" PGPORT="5432" PGUSER="postgres.REPLACE_TENANT" PGDATABASE="postgres"
export PGPASSWORD="$POSTGRES_PASSWORD"

psql -v ON_ERROR_STOP=1 <<'SQL'
SELECT 'CREATE DATABASE loopforge_test'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'loopforge_test')
\gexec
ALTER DATABASE loopforge_test OWNER TO postgres;
SQL
```

这段可以**重复执行**：库已存在时 `\gexec` 不执行 `CREATE DATABASE`，直接跳过，
不会像裸 `CREATE DATABASE` 那样在 `ON_ERROR_STOP=1` 下报错退出。后面的
`ALTER DATABASE ... OWNER TO postgres` 本身是幂等的，重复执行没有副作用。

密码从 `.env` 读进环境变量，不出现在命令行里。`PGUSER` 里的租户段
（`postgres.<租户>`）必须保留——Supavisor 按 `user.tenant` 路由，
去掉租户段会连到别的库。`REPLACE_TENANT` 换成部署实际的租户名。

在 Supabase 托管的实例上，这两条语句也可以直接在 dashboard 的 SQL 编辑器里跑，
效果一样。

### 第 2 步：建表

不用手工做。第一次跑集成用例时，`OpenPostgresTest` 会调 `Open(dsn)`，
`Open` 做 gorm 的增量 DDL：建表、补列、建索引。跑一次用例就等于做了这一步。

### 第 3 步：加生成列和种子行

两个观测生成列（`run_telemetry.adapter`、`run_logs.level`）**只有这一步会建**。
gorm 的 AutoMigrate 只补表、补列、建索引，它不认生成列——所以只做完前两步的
测试库跑冒烟用例时，`TestPostgresSmokeGeneratedColumns` 一定是失败的。
这是预期的，不是移植的 bug。

installer 读 `AGENTFLOW_DATABASE_URL`，而仓库根目录的 `.env` 里放的是**运行库**
的 DSN。要让它作用于测试库，用 `.env` 里已有的 `AGENTFLOW_TEST_POSTGRES_URL`
另写一个 scratch env 文件：

```bash
cd /Users/caijinhai/Documents/projects/loopForge
set -a; . ./.env; set +a

umask 077
mkdir -p .local && chmod 700 .local
printf "AGENTFLOW_DATABASE_URL='%s'\n" "$AGENTFLOW_TEST_POSTGRES_URL" > .local/pg-test.env
chmod 600 .local/pg-test.env

set -a; . ./.local/pg-test.env; set +a
bash scripts/install-schema-extensions-pg.sh

# 用完回收：scratch 文件里带着密码的 DSN
rm -f .local/pg-test.env
```

installer 先读 `.env`（`.env` 里的其它设置照样生效），再把调用方已经导出的
`AGENTFLOW_DATABASE_URL` 盖回去，所以上面这条命令作用于 `loopforge_test`，
不会碰运行库。
`.local/` 在 `.gitignore` 里，scratch 文件不要往别处放；里面带着密码，
用完按上面那行 `rm -f` 删掉。

installer 是幂等的，跑多少次都行。

## 3. 跑测试

```bash
cd /Users/caijinhai/Documents/projects/loopForge
set -a; . ./.env; set +a; unset AGENTFLOW_AUTH_REQUIRED
go test ./internal/repo/... ./internal/httpapi/...
```

两个要点：

- **必须在仓库根目录跑。** 计划书里写的 `cd` 到 Supabase 服务目录再跑
  `go test ./internal/repo/...` 是错的——那个目录下没有 Go 包。
- **`unset AGENTFLOW_AUTH_REQUIRED` 不能少。** `.env` 里它是 `true`（部署需要），
  而 `internal/httpapi` 的测试夹具是按「不强制鉴权」搭的。带着它跑，
  本机实测 **200 个顶层 httpapi 用例失败**（连子测试共 225 个 `--- FAIL`），
  其中 179 个的失败原因是 `401 {"error":"authentication required"}`。
  那是假警报，与移植无关。

DSN 不要在命令行上拼——那会进 shell 历史。`.env` 里已经有
`AGENTFLOW_TEST_POSTGRES_URL`（指向 `loopforge_test`），`set -a; . ./.env`
就把它导出来了。

只跑冒烟用例：

```bash
cd /Users/caijinhai/Documents/projects/loopForge
set -a; . ./.env; set +a; unset AGENTFLOW_AUTH_REQUIRED
go test ./internal/repo/ -run TestPostgresSmoke -v
```

### 跳过不等于通过

`AGENTFLOW_TEST_POSTGRES_URL` 没设时，Postgres 用例是 **skip**，不是 pass。
`go test` 的输出里 `ok` 也会显示，得看 `-v` 里的 `--- SKIP`。报告里要把
「跳过」和「通过」分开写。

## 4. 哪些用例跑在哪个库

| 环境变量 | 库 | 用例 |
| --- | --- | --- |
| `AGENTFLOW_TEST_POSTGRES_URL` | `loopforge_test` | `internal/repo` 里引用该变量的 5 个测试文件：`postgres_connect_test.go`、`postgres_case_test.go`、`postgres_json_test.go`、`postgres_mysqlisms_test.go`、`postgres_smoke_test.go`，外加 `internal/repo/opentest.go`（`OpenPostgresTest` 在那里读这个变量），共 6 个文件（同目录的 `postgres_dialector_test.go` 不连库） |
| `AGENTFLOW_TEST_MYSQL_DSN` | 独立的 MySQL 库 | 显式引用该变量的文件：`internal/repo` 里 37 个（`assistant_concurrency_mysql_test.go`、`bot_charset_mysql_test.go` 等），`internal/httpapi` 里 5 个，`internal/engine` 里 1 个 |

两个变量都设时，两组用例都跑；只设一个时，另一组是 **skip**。
MySQL 那一组是上游的既有集成测试，本次移植没有把它们移植到 Postgres，
它们在这套部署里不会执行——记为跳过。

sqlite 是默认路径：所有包在没有上面两个变量时都用内存 sqlite 跑，
这部分用例始终执行。

## 5. 冒烟用例断言什么

`internal/repo/postgres_smoke_test.go` 有两个用例。

| 断言 | 实测值（`loopforge_test`） |
| --- | --- |
| `Dialector.Name() == "postgres"` | `postgres` |
| `current_schema()` 里的表数 ≥ 50 | 106 |
| `data_type = 'json'` 的列数 == 0 | 0 |
| 模型声明的每个 `type:json` 列的 `data_type` == `jsonb`（逐列核对，不是比计数） | 62 列全部是 `jsonb` |
| `data_type IN ('json','mediumblob','longblob','mediumtext')` 的列数 == 0 | 0 |
| `is_generated = 'ALWAYS'` 的列 == `run_telemetry.adapter` 与 `run_logs.level` | 2 |

最后一条要求第 3 步跑过。只做完前两步的测试库会在
`TestPostgresSmokeGeneratedColumns` 上失败，报
`the generated columns = 0, want 2`。

运行库 `loopforge` 的生成列由部署那一步的 installer 加，冒烟用例不断言运行库
（只断言测试库）。运行库上的生成列数量取决于部署走到哪一步，与本用例无关。

## 6. 与本次移植无关的已知失败

下面这些用例在本机 macOS 上失败，基线（`fd039e5`）上同样失败，不是回归。
其中两条浏览器用例是**本机环境相关的 flaky 用例**（基线同样失败），不是稳定断言：

| 包 | 用例 | 原因 |
| --- | --- | --- |
| `cmd/agentflow` | `TestSDNotifySendsReadyAndMainPID` | unixgram 套接字路径 |
| `deploy` | `TestBindCLIUninstallStopsTheRunnerNamedInServicePID`、`TestBindCLIReinstallStopsTheRunnerNamedInServicePID`、`TestBindCLIUninstallStopsARunnerWhoseArgv0IsTheScript`、`TestBindCLIUninstallSignalsOnlyTheBackgroundRunner` | launchd 的 `service.pid` |
| `internal/engine` | `TestEnsureRunBrowserRestoresReclaimedWorkspaceWithStaleRun` | 需要真的 Chromium |
| `internal/gitwork` | `TestGitDiscoveryStopsBeforeParentRepository` | 本机 git 目录拓扑 |
| `internal/httpapi` | `TestBrowserStreamPushesFramesAndRunsCommands`、`TestMainBotBrowserStartsAndDrivesRunBrowser` | 本机环境相关的 flaky 用例（基线同样失败）：需要真的 Chromium |
| `internal/runtime` | `TestProcessSandboxPathsProfilesAndCommands`、`TestLocalWorkspaceGitDiscoveryCannotReachParent` | 本机沙箱路径与 git 拓扑 |

另有 10 个文件上游就不是 gofmt 干净的（`internal/domain/observability.go`、
`internal/repo/knowledge_usage.go` 等），与本次移植无关。

## 7. 凭据

密码和 DSN 只放在仓库根目录的 `.env`（0600，已在 `.gitignore` 里）。
不进版本库、不进日志、不进命令行、不进本文档。写文档、提 issue、贴命令时
用 `REPLACE_ME` 之类的占位符。

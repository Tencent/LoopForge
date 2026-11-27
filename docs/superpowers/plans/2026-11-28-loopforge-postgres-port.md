# LoopForge MySQL→Postgres 移植 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 LoopForge 的持久层从 MySQL 移植到 Postgres，让平台在本机原生进程里跑起来，数据落在用户已有的 Supabase Postgres 独立库 `loopforge` 中。

**Architecture:** 保留上游的三方言结构（MySQL 默认 / Postgres 本部署 / sqlite 仅测试），把散落在 `internal/repo` 各处的方言差异收敛到 `internal/repo/dialect.go` 的表达式助手；schema 仍由 gorm AutoMigrate 增量补齐，Postgres 特有的列类型（json→jsonb、blob→bytea）由 AutoMigrate 之后的幂等修正 pass 处理；种子与观测生成列改写为 `migrations/` 下的 Postgres SQL 脚本。

**Tech Stack:** Go 1.26.11、GORM v1.31.2、`gorm.io/driver/postgres` v1.6.0（pgx）、Postgres 15.8（Supabase，经 Supavisor session mode `127.0.0.1:5432`）、Docker（沙箱运行时沿用官方 GHCR 镜像）

**Spec:** `docs/superpowers/specs/2026-11-27-loopforge-postgres-supabase-design.md`

## Global Constraints

- 三方言并存：MySQL（上游默认路径，一行不能删）、Postgres（本部署）、sqlite（仅测试）。任何方言分支都必须能编译通过，且 sqlite 分支的既有测试必须继续通过。
- 遵守仓库 `AGENTS.md`：代码不做数据迁移、不做 seed、不加 `ensureXxx`/`migrateXxx`/`seedXxx` 启动自愈逻辑。schema 归 gorm AutoMigrate，数据迁移与种子归 `migrations/` 下的 SQL 脚本，由运维手动执行。
- 所有 Postgres SQL 改写**必须真实执行过**。编译通过不算完成；每条改写过的语句都要有一条集成测试或一次冒烟覆盖。
- 密码与 DSN 只进 `.env`（0600，已在 `.gitignore`），不进版本库、不进日志、不进 spec。
- 本机 Supabase 连接串形态：`postgres://postgres.cesar:<POSTGRES_PASSWORD>@127.0.0.1:5432/loopforge?sslmode=disable`。租户段 `cesar` 必须保留（Supavisor 按 `user.tenant` 路由）。
- 连接池上限 10：Supavisor session mode 下每个客户端连接独占一个真实 backend。
- 分支 `feat/postgres-supabase`，基线 `fd039e5`。每个 Task 结束提交一次。
- 库名：`loopforge`（运行库）、`loopforge_test`（集成测试库，需单独创建）。

---

### Task 1: Postgres 驱动接入与 DSN 分流

**Files:**
- Modify: `go.mod` / `go.sum`（新增 `gorm.io/driver/postgres`）
- Modify: `internal/repo/repo.go:242-284`（`Open`）、`internal/repo/repo.go:36-40`（`deps.isMySQL` 旁）
- Modify: `cmd/agentflow/main.go:93`（flag）、`cmd/agentflow/main.go:141`（调用）
- Create: `internal/repo/dialect.go`
- Test: `internal/repo/dialect_test.go`

**Interfaces:**
- Consumes: 无（首个任务）
- Produces:
  - `func dsnDialect(dsn string) string` → `"postgres"` / `"mysql"`
  - `func postgresDSN(dsn string) string` → 规范化后的 Postgres URL
  - `func (d *deps) isPostgres() bool`
  - `func openDB(dsn string) (*gorm.DB, error)` → 按方言打开 gorm 连接
  - `repo.Open(dsn)` 同时接受 Postgres URL 与 MySQL DSN；dsn 为空时依次读 `AGENTFLOW_DATABASE_URL`、`AGENTFLOW_MYSQL_DSN`

- [ ] **Step 1: 写失败测试**

创建 `internal/repo/dialect_test.go`：

```go
package repo

import (
	"strings"
	"testing"
)

func TestDSNDialect(t *testing.T) {
	cases := map[string]string{
		"postgres://postgres.cesar:pw@127.0.0.1:5432/loopforge?sslmode=disable": "postgres",
		"postgresql://postgres.cesar:pw@127.0.0.1:5432/loopforge":               "postgres",
		"loopforge:pw@tcp(127.0.0.1:33315)/loopforge":                             "mysql",
		"  postgres://u:p@h:5432/d  ":                                            "postgres",
	}
	for dsn, want := range cases {
		if got := dsnDialect(dsn); got != want {
			t.Fatalf("dsnDialect(%q) = %q, want %q", dsn, got, want)
		}
	}
}

func TestPostgresDSNNormalizes(t *testing.T) {
	got := postgresDSN("postgres://postgres.cesar:pw@127.0.0.1:5432/loopforge")
	if want := "sslmode=disable"; !strings.Contains(got, want) {
		t.Fatalf("postgresDSN missing %q: %s", want, got)
	}
	if want := "pool_max_conns=10"; !strings.Contains(got, want) {
		t.Fatalf("postgresDSN missing %q: %s", want, got)
	}
	again := postgresDSN(got)
	if again != got {
		t.Fatalf("postgresDSN is not idempotent: %s -> %s", got, again)
	}
	kept := postgresDSN("postgres://u:p@h:5432/d?sslmode=require&pool_max_conns=4")
	if !strings.Contains(kept, "sslmode=require") || !strings.Contains(kept, "pool_max_conns=4") {
		t.Fatalf("postgresDSN overwrote caller params: %s", kept)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/repo/ -run 'TestDSNDialect|TestPostgresDSNNormalizes'`
Expected: FAIL — `undefined: dsnDialect`

- [ ] **Step 3: 实现 dialect.go**

创建 `internal/repo/dialect.go`：

```go
package repo

import (
	"fmt"
	"strings"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The store speaks three dialects: MySQL (upstream default), Postgres (this
// deployment) and sqlite (tests only). Every fragment that differs between
// them lives in this file so call sites stay readable.

const (
	dialectMySQL    = "mysql"
	dialectPostgres = "postgres"
	dialectSQLite   = "sqlite"
)

// dsnDialect classifies a DSN by its scheme. Postgres URLs start with
// postgres:// or postgresql://; everything else is the MySQL DSN form
// (user:password@tcp(host:port)/database).
func dsnDialect(dsn string) string {
	lowered := strings.ToLower(strings.TrimSpace(dsn))
	if strings.HasPrefix(lowered, "postgres://") || strings.HasPrefix(lowered, "postgresql://") {
		return dialectPostgres
	}
	return dialectMySQL
}

// postgresDSN normalizes a Postgres URL: the local Supavisor pooler speaks
// plain TCP without TLS, and every pooled connection owns one backend, so the
// pool stays small. Caller-supplied parameters are never overwritten.
func postgresDSN(dsn string) string {
	trimmed := strings.TrimSpace(dsn)
	separator := "?"
	if strings.Contains(trimmed, "?") {
		separator = "&"
	}
	if !strings.Contains(trimmed, "sslmode=") {
		trimmed += separator + "sslmode=disable"
		separator = "&"
	}
	if !strings.Contains(trimmed, "pool_max_conns=") {
		trimmed += separator + "pool_max_conns=10"
	}
	return trimmed
}

// openDB opens the gorm handle for the DSN's dialect.
func openDB(dsn string) (*gorm.DB, error) {
	config := &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)}
	if dsnDialect(dsn) == dialectPostgres {
		db, err := gorm.Open(postgres.Open(postgresDSN(dsn)), config)
		if err != nil {
			return nil, fmt.Errorf("postgres open: %w", err)
		}
		return db, nil
	}
	db, err := gorm.Open(mysql.Open(dsn), config)
	if err != nil {
		return nil, fmt.Errorf("mysql open: %w", err)
	}
	return db, nil
}

// isPostgres reports whether the handle talks to Postgres.
func (d *deps) isPostgres() bool {
	return d.DB != nil && d.DB.Dialector.Name() == dialectPostgres
}
```

- [ ] **Step 4: 引入 Postgres 驱动**

Run: `go get gorm.io/driver/postgres@v1.6.0 && go mod tidy`
Expected: `go.mod` 新增 `gorm.io/driver/postgres v1.6.0` 与 `github.com/jackc/pgx/v5`。

- [ ] **Step 5: 改 Open 支持 Postgres DSN**

修改 `internal/repo/repo.go` 的 `Open`（242-284 行）：

```go
// Open connects the store to the configured database, which is where all
// state lives. The DSN scheme selects the dialect: a postgres:// URL opens a
// Postgres connection, anything else is the MySQL DSN form. An empty DSN is a
// configuration error, not a reason to invent state somewhere else.
func Open(dsn string) (*Store, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("AGENTFLOW_DATABASE_URL"))
	}
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("AGENTFLOW_MYSQL_DSN"))
	}
	if dsn == "" {
		return nil, errors.New("AGENTFLOW_DATABASE_URL 未配置：数据库是唯一数据源，缺少 DSN 时拒绝启动")
	}
	if dsnDialect(dsn) == dialectMySQL {
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
	}
	db, err := openDB(dsn)
	if err != nil {
		return nil, err
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(32)
		sqlDB.SetMaxIdleConns(8)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
		if err := sqlDB.Ping(); err != nil {
			return nil, fmt.Errorf("database ping: %w", err)
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
```

注意：`SetMaxOpenConns(32)` 在 Postgres 下改为 10（Supavisor session mode 的 backend 预算）：

```go
	if sqlDB, err := db.DB(); err == nil {
		maxOpen := 32
		if dsnDialect(dsn) == dialectPostgres {
			maxOpen = 10
		}
		sqlDB.SetMaxOpenConns(maxOpen)
		sqlDB.SetMaxIdleConns(8)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
		...
	}
```

- [ ] **Step 6: 改 main.go 的 flag 与文案**

`cmd/agentflow/main.go:93` 改为：

```go
	databaseURL := flag.String("database-url", "", "Database URL (postgres://…) or MySQL DSN; overrides AGENTFLOW_DATABASE_URL and AGENTFLOW_MYSQL_DSN. The database is the only data source.")
```

`cmd/agentflow/main.go:141` 改为：

```go
	s, err := repo.Open(strings.TrimSpace(*databaseURL))
```

- [ ] **Step 7: 跑测试确认通过**

Run: `go build ./... && go test ./internal/repo/ -run 'TestDSNDialect|TestPostgresDSNNormalizes'`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add go.mod go.sum internal/repo/dialect.go internal/repo/dialect_test.go internal/repo/repo.go cmd/agentflow/main.go
git commit -m "feat(db): 支持 Postgres 连接与 DSN 分流

按 DSN scheme 选择 gorm 驱动，新增 AGENTFLOW_DATABASE_URL 与 -database-url，
保留 MySQL 路径不变。"
```

---

### Task 2: Postgres 列类型修正（json→jsonb、blob→bytea、mediumtext→text）

**Files:**
- Create: `internal/repo/postgres_dialector.go`
- Modify: `internal/repo/dialect.go`（`openDB` 改用包装后的 dialector）
- Test: `internal/repo/postgres_dialector_test.go`

**Interfaces:**
- Consumes: Task 1 的 `dialectPostgres`、`(d *deps) isPostgres()`
- Produces:
  - `type postgresDialector struct{ *postgres.Dialector }`（实现 `gorm.Dialector`）
  - `func postgresTypeFixup(mysqlType string) (string, bool)` → Postgres 类型名
  - `func (d postgresDialector) DataTypeOf(field *schema.Field) string`
  - `func (d postgresDialector) Migrator(db *gorm.DB) gorm.Migrator`

背景：模型列 tag 是 MySQL 味的——`type:json` 62 处、`type:mediumblob` 7 处、`type:longblob` 1 处、`type:mediumtext` 3 处（分布在 `internal/repo/` 下的 models.go、evolution.go、notifications.go、assistant_generations.go、bot_chat.go 等十来个文件；实施期间按全仓 grep 重新统计，旧稿里的 46/2/1/1 是漏算）。gorm 的 postgres 驱动对认不出的类型名直接透传（`getSchemaCustomType`），所以 AutoMigrate 会去建 `mediumblob` / `longblob` / `mediumtext` 列——Postgres 没有这些类型，`CREATE TABLE` 直接失败；而 `json` 类型没有等值运算符，upsert 与等值查询会失效。

**因此类型必须在建表那一刻就是 Postgres 类型，不能等 AutoMigrate 之后再 ALTER**（那样建表就已经失败了）。

方案：包装 postgres dialector，在 `DataTypeOf` 里把 MySQL 类型名映射成 Postgres 类型名；并覆写 `Migrator()`，把 gorm 的 `migrator.Migrator` 绑到这个包装 dialector 上（`migrator.Config.Dialector`）——否则 AutoMigrate 内部仍会用原始 postgres dialector 算类型，映射不生效。

- [ ] **Step 1: 写失败测试**

创建 `internal/repo/postgres_dialector_test.go`：

```go
package repo

import (
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm/schema"
)

func TestPostgresTypeFixup(t *testing.T) {
	cases := []struct {
		mysqlType string
		pgType    string
	}{
		{"json", "jsonb"},
		{"mediumblob", "bytea"},
		{"longblob", "bytea"},
		{"mediumtext", "text"},
	}
	for _, tc := range cases {
		got, ok := postgresTypeFixup(tc.mysqlType)
		if !ok || got != tc.pgType {
			t.Fatalf("postgresTypeFixup(%q) = %q,%v; want %q,true", tc.mysqlType, got, ok, tc.pgType)
		}
	}
	if _, ok := postgresTypeFixup("text"); ok {
		t.Fatal("postgresTypeFixup(text) must not be remapped")
	}
}

func TestPostgresDialectorDataTypeOf(t *testing.T) {
	d := postgresDialector{Dialector: &postgres.Dialector{Config: &postgres.Config{}}}
	cases := map[string]string{
		"json":       "jsonb",
		"mediumblob": "bytea",
		"longblob":   "bytea",
		"mediumtext": "text",
	}
	for mysqlType, want := range cases {
		field := &schema.Field{DataType: schema.DataType(mysqlType)}
		if got := d.DataTypeOf(field); got != want {
			t.Fatalf("DataTypeOf(%q) = %q; want %q", mysqlType, got, want)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/repo/ -run 'TestPostgresTypeFixup|TestPostgresDialectorDataTypeOf'`
Expected: FAIL — `undefined: postgresTypeFixup`

- [ ] **Step 3: 实现 postgres_dialector.go**

创建 `internal/repo/postgres_dialector.go`：

```go
package repo

import (
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

// Postgres types for the MySQL-flavoured column tags the models use. `json`
// has no equality operator in Postgres (upserts and equality lookups break on
// it) and `mediumblob`/`longblob`/`mediumtext` do not exist there at all, so
// the DDL has to be written in Postgres types from the start.
var postgresTypeFixups = map[string]string{
	"json":       "jsonb",
	"mediumblob": "bytea",
	"longblob":   "bytea",
	"mediumtext": "text",
}

// postgresTypeFixup returns the Postgres type for a MySQL column type.
func postgresTypeFixup(mysqlType string) (string, bool) {
	pg, ok := postgresTypeFixups[strings.ToLower(strings.TrimSpace(mysqlType))]
	return pg, ok
}

// postgresDialector is the stock Postgres dialector with the models' MySQL
// column types translated on the way into DDL.
type postgresDialector struct {
	*postgres.Dialector
}

// DataTypeOf maps the MySQL type names used in the model tags onto their
// Postgres equivalents and leaves every other type to the stock driver.
func (d postgresDialector) DataTypeOf(field *schema.Field) string {
	if pg, ok := postgresTypeFixup(string(field.DataType)); ok {
		return pg
	}
	return d.Dialector.DataTypeOf(field)
}

// Migrator binds gorm's migrator to this dialector so AutoMigrate creates the
// columns with the translated types.
func (d postgresDialector) Migrator(db *gorm.DB) gorm.Migrator {
	return postgres.Migrator{Migrator: migrator.Migrator{Config: migrator.Config{
		DB:                          db,
		Dialector:                   d,
		CreateIndexAfterCreateTable: true,
	}}}
}
```

- [ ] **Step 4: 让 openDB 走包装 dialector**

修改 `internal/repo/dialect.go` 的 `openDB`：

```go
	if dsnDialect(dsn) == dialectPostgres {
		pg := postgres.Open(postgresDSN(dsn)).(*postgres.Dialector)
		db, err := gorm.Open(postgresDialector{Dialector: pg}, config)
		if err != nil {
			return nil, fmt.Errorf("postgres open: %w", err)
		}
		return db, nil
	}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go build ./... && go test ./internal/repo/ -run 'TestPostgresTypeFixup|TestPostgresDialectorDataTypeOf'`
Expected: PASS

- [ ] **Step 6: 在真实 Postgres 上验证 AutoMigrate 能建表**

用本机 Supabase 的 `loopforge` 库（DSN 见 dispatch 说明）跑一次真实的 AutoMigrate：

```bash
psql "postgres://postgres.cesar:<password>@127.0.0.1:5432/loopforge?sslmode=disable" \
  -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;'
```

然后用一个小 Go 程序（或 `go test` 的临时用例）调用 `repo.Open(dsn)`，确认：
- AutoMigrate 不报 `type "mediumblob" does not exist` 之类的错；
- `information_schema.columns` 里 `data_type` 只出现 `jsonb` / `bytea` / `text`，不再有 `json` / `mediumblob` / `longblob` / `mediumtext`。

```bash
psql "postgres://postgres.cesar:<password>@127.0.0.1:5432/loopforge?sslmode=disable" \
  -c "SELECT data_type, count(*) FROM information_schema.columns WHERE table_schema='public' GROUP BY data_type ORDER BY 2 DESC;"
```

（`<password>` 由 dispatch 说明给出；密码不得写进版本库。）

- [ ] **Step 7: 提交**

```bash
git add internal/repo/postgres_dialector.go internal/repo/postgres_dialector_test.go internal/repo/dialect.go
git commit -m "feat(db): Postgres 列类型修正（json→jsonb、blob→bytea）"
```

---

### Task 3: 方言表达式助手（dialect.go 扩展）

**Files:**
- Modify: `internal/repo/dialect.go`
- Modify: `internal/repo/repo.go:36-70`（`deps` 的 JSON 助手改为走 dialect.go）
- Modify: `internal/repo/dbtime.go:24-42`（数据库时钟加 Postgres 分支）
- Test: `internal/repo/dialect_test.go`（追加）

**Interfaces:**
- Consumes: Task 1 的 `dialectMySQL`/`dialectPostgres`/`dialectSQLite`、`(d *deps) isPostgres()`
- Produces（后续 Task 统一使用这些助手，签名固定）：
  - `func jsonPathToPGArray(path string) string`
  - `func (d *deps) jsonTextLiteral(column, path string) string` → 文本取值
  - `func (d *deps) jsonTextParam(column string) string` → 路径作为参数绑定的文本取值
  - `func (d *deps) jsonLengthLiteral(column, path string) string` → 数组长度
  - `func (d *deps) jsonTypeLiteral(column, path string) string` → JSON 类型名
  - `func (d *deps) jsonBuildObject(pairs ...string) string` → 偶数个参数，key/value 交替
  - `func (d *deps) likeOperator() string` → `"LIKE"` / `"ILIKE"`
  - `func (d *deps) nowMillisLiteral() string` → `"UTC_TIMESTAMP(3)"` / `"now()"`
  - `func (d *deps) nowUSLiteral() string` → 纪元微秒

- [ ] **Step 1: 写失败测试**

在 `internal/repo/dialect_test.go` 追加：

```go
func TestJSONPathToPGArray(t *testing.T) {
	cases := map[string]string{
		"$.name":            "{name}",
		"$.a.b":             "{a,b}",
		"$.messages[0]":     "{messages,0}",
		"$.a[1].b":          "{a,1,b}",
		"$":                 "{}",
	}
	for path, want := range cases {
		if got := jsonPathToPGArray(path); got != want {
			t.Fatalf("jsonPathToPGArray(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDialectExpressions(t *testing.T) {
	// 助手依赖一个真实的 gorm 句柄来判定方言，纯函数部分单独覆盖；
	// 方言分支由 postgres_types_test.go 与集成测试覆盖。
	if got := jsonPathToPGArray("$.messages[0].content"); got != "{messages,0,content}" {
		t.Fatalf("unexpected path array: %s", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/repo/ -run 'TestJSONPathToPGArray|TestDialectExpressions'`
Expected: FAIL — `undefined: jsonPathToPGArray`

- [ ] **Step 3: 实现助手**

在 `internal/repo/dialect.go` 追加：

```go
// jsonPathToPGArray converts a MySQL JSON path ("$.a.b", "$.messages[0]") into
// the Postgres path array form ("{a,b}", "{messages,0}").
func jsonPathToPGArray(path string) string {
	trimmed := strings.TrimSpace(path)
	trimmed = strings.TrimPrefix(trimmed, "$")
	trimmed = strings.TrimPrefix(trimmed, ".")
	var parts []string
	for _, segment := range strings.Split(trimmed, ".") {
		for {
			open := strings.Index(segment, "[")
			if open < 0 {
				break
			}
			closeIdx := strings.Index(segment, "]")
			if closeIdx < open {
				break
			}
			parts = append(parts, segment[:open])
			parts = append(parts, segment[open+1:closeIdx])
			segment = segment[closeIdx+1:]
		}
		if segment != "" {
			parts = append(parts, segment)
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// jsonTextLiteral extracts a text value at a JSON path. The path is a code
// constant, never user input.
func (d *deps) jsonTextLiteral(column, path string) string {
	if d.isPostgres() {
		return column + "::jsonb #>> '" + jsonPathToPGArray(path) + "'"
	}
	if d.isMySQL() {
		return "JSON_UNQUOTE(JSON_EXTRACT(" + column + ", '" + path + "'))"
	}
	return "json_extract(" + column + ", '" + path + "')"
}

// jsonTextParam extracts a text value at a path bound as a parameter. The
// bound parameter keeps the MySQL path form ("$.runId"); the Postgres branch
// converts it to a path array in SQL, so the call sites do not change.
func (d *deps) jsonTextParam(column string) string {
	if d.isPostgres() {
		return column + "::jsonb #>> string_to_array(ltrim(?, '$.'), '.')"
	}
	if d.isMySQL() {
		return "JSON_UNQUOTE(JSON_EXTRACT(" + column + ", ?))"
	}
	return "json_extract(" + column + ", ?)"
}

// jsonLengthLiteral counts the elements of the JSON array at a path. A
// missing or non-array value counts as zero.
func (d *deps) jsonLengthLiteral(column, path string) string {
	if d.isPostgres() {
		inner := column + "::jsonb #> '" + jsonPathToPGArray(path) + "'"
		return "COALESCE(CASE WHEN jsonb_typeof(" + inner + ") = 'array' THEN jsonb_array_length(" + inner + ") END, 0)"
	}
	if d.isMySQL() {
		return "JSON_LENGTH(JSON_EXTRACT(" + column + ", '" + path + "'))"
	}
	return "json_array_length(" + column + ", '" + path + "')"
}

// jsonTypeLiteral names the JSON type at a path ("STRING", "OBJECT", ...).
func (d *deps) jsonTypeLiteral(column, path string) string {
	if d.isPostgres() {
		inner := column + "::jsonb #> '" + jsonPathToPGArray(path) + "'"
		return "CASE WHEN jsonb_typeof(" + inner + ") IS NULL THEN NULL WHEN jsonb_typeof(" + inner + ") = 'string' THEN 'STRING' WHEN jsonb_typeof(" + inner + ") = 'number' THEN 'INTEGER' WHEN jsonb_typeof(" + inner + ") = 'boolean' THEN 'BOOLEAN' WHEN jsonb_typeof(" + inner + ") = 'object' THEN 'OBJECT' WHEN jsonb_typeof(" + inner + ") = 'array' THEN 'ARRAY' ELSE 'NULL' END"
	}
	if d.isMySQL() {
		return "JSON_TYPE(JSON_EXTRACT(" + column + ", '" + path + "'))"
	}
	return "json_type(" + column + ", '" + path + "')"
}

// jsonBuildObject builds a JSON object from alternating key/value arguments.
func (d *deps) jsonBuildObject(pairs ...string) string {
	if d.isPostgres() {
		return "jsonb_build_object(" + strings.Join(pairs, ", ") + ")"
	}
	if d.isMySQL() {
		return "JSON_OBJECT(" + strings.Join(pairs, ", ") + ")"
	}
	return "json_object(" + strings.Join(pairs, ", ") + ")"
}

// likeOperator returns the pattern-matching operator. MySQL's default
// collation matches case-insensitively, so Postgres uses ILIKE to keep the
// same search behaviour.
func (d *deps) likeOperator() string {
	if d.isPostgres() {
		return "ILIKE"
	}
	return "LIKE"
}

// nowMillisLiteral returns the current timestamp in milliseconds.
func (d *deps) nowMillisLiteral() string {
	if d.isPostgres() {
		return "now()"
	}
	return "UTC_TIMESTAMP(3)"
}
```

把 `internal/repo/repo.go` 里已有的三个助手改为委托到新助手（保持旧函数名与行为，避免大面积改动）：

```go
func (d *deps) sqlJSONText(path string) string {
	if d.isPostgres() {
		return d.jsonTextParam("payload")
	}
	if d.isMySQL() {
		return "JSON_UNQUOTE(JSON_EXTRACT(payload, ?))"
	}
	return "json_extract(payload, ?)"
}
```

`sqlPayloadJSONText(path string)`：

```go
func (d *deps) sqlPayloadJSONText(path string) string {
	if d.isPostgres() {
		return "payload::jsonb #>> '" + jsonPathToPGArray(path) + "'"
	}
	if d.isMySQL() {
		return "JSON_UNQUOTE(JSON_EXTRACT(payload, '$." + path + "'))"
	}
	return "json_extract(payload, '$." + path + "')"
}
```

`sqlExecutionContextJSONText(path string)` 同理，列名换成 `execution_context`。

注意：

1. **绑定参数的路径形态**：`sqlJSONText` 的调用方（例如 `internal/repo/assistant_shared.go:37`）把路径写成 `$.runId` 并**原样绑定**：`Where(r.sqlJSONText("$.runId")+" = ?", "$.runId", runID)`。所以 Postgres 分支不能要求调用方改成数组形态，只能在 SQL 里把 `$.runId` 拆成 `{runId}`——上面用的是 `string_to_array(ltrim(?, '$.'), '.')`（`ltrim` 的第二参是字符集合，会剥掉开头的 `$` 与 `.`）。**调用方一个字都不用改。**
2. **字面量路径的形态**：`sqlPayloadJSONText`/`sqlExecutionContextJSONText` 的调用方传的是不带 `$.` 前缀的路径段（如 `"name"`、`"clarification.startedAt"`），因此 `jsonPathToPGArray("name")` → `{name}`。`jsonPathToPGArray` 同时接受 `$.a.b` 与 `a.b` 两种形态。

- [ ] **Step 4: 数据库时钟加 Postgres 分支**

修改 `internal/repo/dbtime.go`：

```go
const (
	mysqlNowUS    = "CAST(UNIX_TIMESTAMP(NOW(6)) * 1000000 AS SIGNED)"
	sqliteNowUS   = "CAST(ROUND((julianday('now') - 2440587.5) * 86400000000.0) AS INTEGER)"
	postgresNowUS = "CAST((EXTRACT(EPOCH FROM clock_timestamp()) * 1000000)::bigint AS bigint)"
)
```

`dbNowUSSQL` 改为：

```go
func (d *deps) dbNowUSSQL() string {
	if v := dbClockOverrideUS.Load(); v != 0 {
		return strconv.FormatInt(v, 10)
	}
	if d.isMySQL() {
		return mysqlNowUS
	}
	if d.isPostgres() {
		return postgresNowUS
	}
	return sqliteNowUS
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go build ./... && go test ./internal/repo/ -run 'TestJSONPathToPGArray|TestDialectExpressions|TestDSNDialect|TestPostgresDSN'`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/repo/dialect.go internal/repo/dialect_test.go internal/repo/repo.go internal/repo/dbtime.go
git commit -m "feat(db): 收敛方言表达式助手（JSON/时间/大小写）"
```

---

### Task 4: 改写 repo 层的 JSON 调用点

**Files:**
- Modify: `internal/repo/runs.go`（20 处）
- Modify: `internal/repo/assistant.go`（4 处 + JSON_SET/JSON_REMOVE）
- Modify: `internal/repo/assistant_context.go`（JSON_TABLE 分支）
- Modify: `internal/repo/assistant_run_meta.go`（2 处）
- Modify: `internal/repo/runs_sop_detection.go`（2 处）
- Modify: `internal/repo/runners.go`（JSON_SET）
- Modify: `internal/repo/assistant_shared.go`（JSON_REMOVE）
- Modify: `internal/repo/analytics.go`（2 处）
- Modify: `internal/repo/analytics_dashboard.go`、`internal/repo/analytics_dashboard_signals.go`（各 1 处）
- Modify: `internal/repo/analytics_activity_usage.go`（JSON_TABLE + JSON_ARRAYAGG）
- Test: `internal/repo/postgres_json_test.go`（集成测试，Task 9 提供 OpenPostgresTest；本任务先写测试骨架，Task 9 补齐 helper 后运行）

**Interfaces:**
- Consumes: Task 3 的 `jsonTextLiteral`、`jsonLengthLiteral`、`jsonTypeLiteral`、`jsonBuildObject`、`jsonPathToPGArray`
- Produces: 无新增公共接口

改写规则（逐条对应现有调用点）：

| 现有 MySQL 写法 | Postgres 写法 |
| --- | --- |
| `JSON_UNQUOTE(JSON_EXTRACT(col, '$.a.b'))` | `col::jsonb #>> '{a,b}'`（用 `d.jsonTextLiteral(col, "$.a.b")`） |
| `JSON_EXTRACT(col, '$.a.b')`（取值当 JSON 用） | `col::jsonb #> '{a,b}'` |
| `JSON_OBJECT('k', v, ...)` | `jsonb_build_object('k', v, ...)`（用 `d.jsonBuildObject`） |
| `JSON_LENGTH(JSON_EXTRACT(col, '$.a'))` | `d.jsonLengthLiteral(col, "$.a")` |
| `JSON_TYPE(JSON_EXTRACT(col, '$.a'))` | `d.jsonTypeLiteral(col, "$.a")` |
| `JSON_SET(JSON_REMOVE(col, '$.messages'), '$.k', v, ...)` | `(col::jsonb #- '{messages}') \|\| jsonb_build_object('k', v, ...)` |
| `CAST(x AS JSON)` | `x::jsonb` |
| `JSON_ARRAYAGG(...)` / `JSON_ARRAY()` | `jsonb_agg(...)` / `'[]'::jsonb` |
| `JSON_TABLE(col, '$.messages[*]' COLUMNS (...))` | `jsonb_array_elements(col::jsonb -> 'messages')`（逐字段 `->>`） |

**JSON_TABLE 的具体改法**（`internal/repo/assistant_context.go:61-64`）：

MySQL 分支保留原样，新增 Postgres 分支：

```go
func (r *AssistantRepo) assistantParticipantSummarySQL() string {
	if r.DB.Dialector.Name() == dialectSQLite {
		return `CASE WHEN json_extract(payload, '$.participantsVersion') = 1 THEN '[]' ELSE
   (SELECT json_group_array(json_object('role','user','authorId',json_extract(m.value,'$.authorId'),'authorName',json_extract(m.value,'$.authorName')))
    FROM json_each(payload,'$.messages') m WHERE json_extract(m.value,'$.role')='user'
    AND COALESCE(json_extract(m.value,'$.trigger'),'')=''
    AND COALESCE(json_extract(m.value,'$.content'),'') NOT LIKE '已确认并执行 %请核对这条草稿的保存回执，继续完成原始请求%') END`
	}
	if r.isPostgres() {
		return `CASE WHEN payload::jsonb #>> '{participantsVersion}' = '1' THEN '[]'::jsonb ELSE
  (SELECT COALESCE(jsonb_agg(jsonb_build_object('role','user','authorId',m->'authorId','authorName',m->'authorName')), '[]'::jsonb)
   FROM jsonb_array_elements(COALESCE(payload::jsonb -> 'messages', '[]'::jsonb)) AS m
   WHERE m->>'role' = 'user' AND COALESCE(m->>'trigger','')='' AND COALESCE(m->>'content','') NOT LIKE '已确认并执行 %请核对这条草稿的保存回执，继续完成原始请求%') END`
	}
	return `CASE WHEN JSON_EXTRACT(payload, '$.participantsVersion') = 1 THEN '[]' ELSE
  (SELECT COALESCE(JSON_ARRAYAGG(JSON_OBJECT('role','user','authorId',m.author_id,'authorName',m.author_name)),JSON_ARRAY())
   FROM JSON_TABLE(payload,'$.messages[*]' COLUMNS (role VARCHAR(32) PATH '$.role', author_id BIGINT PATH '$.authorId', author_name VARCHAR(255) PATH '$.authorName', message_trigger VARCHAR(64) PATH '$.trigger', content LONGTEXT PATH '$.content')) m
   WHERE m.role='user' AND COALESCE(m.message_trigger,'')='' AND COALESCE(m.content,'') NOT LIKE '已确认并执行 %请核对这条草稿的保存回执，继续完成原始请求%') END`
}
```

同时 `internal/repo/assistant.go:66-70` 的整条 Select 需要按方言改写。MySQL 保持原样，新增 Postgres 分支：

```go
func (r *AssistantRepo) assistantSessionSummaries() *gorm.DB {
	summary := "json(" + r.assistantParticipantSummarySQL() + ")"
	if r.DB.Dialector.Name() != dialectSQLite {
		summary = "CAST((" + r.assistantParticipantSummarySQL() + ") AS JSON)"
	}
	countExpr := r.assistantExecutionMessageCountSQL()
	removeMessages := "JSON_REMOVE(payload, '$.messages')"
	outer := "JSON_SET(" + removeMessages + ", '$.executionMessageCount', " + countExpr + ", '$.participantSummaryMessages', " + summary + ")"
	if r.isPostgres() {
		summary = "(" + r.assistantParticipantSummarySQL() + ")"
		countExpr = r.assistantExecutionMessageCountSQL()
		outer = "(payload::jsonb #- '{messages}') || jsonb_build_object('executionMessageCount', " + countExpr + ", 'participantSummaryMessages', " + summary + ")"
	}
	return r.DB.Table("assistant_sessions").Select("id, user_id, project_id, title, model, source, task_id, created_at, updated_at, " + outer + " AS payload")
}
```

`assistantExecutionMessageCountSQL`（`internal/repo/assistant.go:73-78`）改为：

```go
func (r *AssistantRepo) assistantExecutionMessageCountSQL() string {
	if r.DB.Dialector.Name() == dialectSQLite {
		return "COALESCE(JSON_ARRAY_LENGTH(payload, '$.messages'), 0)"
	}
	if r.isPostgres() {
		return r.jsonLengthLiteral("payload", "$.messages")
	}
	return "COALESCE(JSON_LENGTH(JSON_EXTRACT(payload, '$.messages')), 0)"
}
```

`runs.go` 的 20 处按上表逐条替换，示例（`runs.go:695-706` 的 `runViewNodesQuery`）：

```go
	for _, field := range []struct {
		name  string
		limit int
	}{{"output", 2501}, {"error", 1201}, {"feedback", 801}, {"activity", 301}} {
		path := "'$." + field.name + "'"
		extract := "JSON_EXTRACT(payload, " + path + ")"
		text := extract
		if r.isMySQL() {
			text = "JSON_UNQUOTE(" + extract + ")"
		}
		parts = append(parts, path, fmt.Sprintf("CASE WHEN JSON_TYPE(%s) = 'STRING' THEN SUBSTR(%s, 1, %d) ELSE %s END", extract, text, field.limit, extract))
		if !r.isMySQL() {
			parts[len(parts)-1] = fmt.Sprintf("CASE WHEN JSON_TYPE(payload, %s) = 'text' THEN SUBSTR(%s, 1, %d) ELSE %s END", path, text, field.limit, extract)
		}
	}
```

Postgres 分支单独成段（MySQL 与 sqlite 分支保持原样）：

```go
	if r.isPostgres() {
		parts := []string{"payload"}
		for _, field := range []struct {
			name  string
			limit int
		}{{"output", 2501}, {"error", 1201}, {"feedback", 801}, {"activity", 301}} {
			jsonPath := jsonPathToPGArray("$." + field.name)
			extract := "payload::jsonb #> '" + jsonPath + "'"
			text := "payload::jsonb #>> '" + jsonPath + "'"
			parts = append(parts, "'$."+field.name+"'",
				fmt.Sprintf("CASE WHEN jsonb_typeof(%s) = 'string' THEN SUBSTR(%s, 1, %d) ELSE %s END", extract, text, field.limit, extract))
		}
		return r.DB.Select("run_id, node_id, (" + strings.Join(parts, ", ") + ")::jsonb AS payload")
	}
```

**`analytics_activity_usage.go` 的 JSON_TABLE 改法**（该文件 35 行）：

MySQL 分支原样保留，Postgres 分支：

```sql
(SELECT COALESCE(jsonb_agg(jsonb_build_object('index', m.ord - 1, 'createdAt', m.msg->'createdAt', 'startedAt', m.msg->'startedAt', 'usage', m.msg->'usage')), '[]'::jsonb)
 FROM (SELECT ord, msg FROM jsonb_array_elements(COALESCE(s.payload::jsonb -> 'messages', '[]'::jsonb)) WITH ORDINALITY AS t(msg, ord)) m
 WHERE m.msg->>'role' = 'assistant')
```

- [ ] **Step 1: 逐文件改写**

按上表把 12 个文件里的 JSON 调用点全部改成三方言分支。改写时遵守：
- MySQL 分支代码**逐字保留**（只在其前后插入 Postgres 分支），保证上游行为零变化。
- Postgres 分支的路径一律用 `jsonPathToPGArray` 生成，不手写 `{a,b}` 字面量。
- 每处改写都要能说出对应的旧语句（实施记录里写进 commit message 或 PR 描述）。

- [ ] **Step 2: 编译**

Run: `go build ./...`
Expected: 无错误（编译错误说明漏了变量名/分支）

- [ ] **Step 3: 用 psql 预检每条新语句**

对每条改写后的 Postgres 语句，用 psql 在 `loopforge` 库上验证语法。

若此时 `loopforge` 库还没有表（平台尚未启动过，AutoMigrate 还没跑），先建表再验：

```bash
cd /Users/caijinhai/Documents/projects/loopForge && \
AGENTFLOW_DATABASE_URL="postgres://postgres.cesar:${POSTGRES_PASSWORD}@127.0.0.1:5432/loopforge?sslmode=disable" \
  go run ./cmd/agentflow -database-url "$AGENTFLOW_DATABASE_URL" -role api &
```

（`role api` 只起 API 不起 worker；启动后 `Ctrl-C` 结束。）

然后逐条验证：

```bash
cd /Users/caijinhai/Documents/services/supabase && set -a && source .env && set +a && \
PGPASSWORD="$POSTGRES_PASSWORD" psql -h 127.0.0.1 -p 5432 -U postgres.cesar -d loopforge \
  -c "SELECT (payload::jsonb #>> '{name}') FROM runs LIMIT 1;"
```

每条语句都要能执行（表为空时返回 0 行也算通过）。

- [ ] **Step 4: 提交**

```bash
git add internal/repo/
git commit -m "feat(db): JSON 调用点增加 Postgres 分支

按 JSON_EXTRACT/JSON_OBJECT/JSON_LENGTH/JSON_TYPE/JSON_SET/JSON_TABLE
逐条改写，MySQL 分支保持原样。"
```

---

### Task 5: 改写非 JSON 的 MySQL 专有点

**Files:**
- Modify: `internal/repo/payload.go:82-107`（`insertIgnore`）
- Modify: `internal/repo/payload.go:59-80`（`upsert`/`upsertColumns` 的冲突目标）
- Modify: `internal/repo/integration_lock.go:20-62`（`GET_LOCK`）
- Modify: `internal/repo/assistant_generations.go:152,171,179`（`UTC_TIMESTAMP`/`INTERVAL`）
- Modify: `internal/repo/workspace_checkpoints.go:470,488`（同上）
- Modify: `internal/repo/catalog.go:481`（COLLATE 模拟）
- Modify: `internal/repo/catalog.go:80`（`CAST(payload AS CHAR)` 等值比较）
- Modify: `internal/repo/run_evals.go:120`（`LIMIT 18446744073709551615`）
- Modify: `internal/repo/rooms.go:114`（`CAST(payload AS CHAR)`）
- Modify: `internal/repo/assistant_cli_notice.go:100-105`（`INSTR(CAST(payload AS CHAR), ?)`）
- Modify: `internal/repo/schedule_job_executions.go:256-260`（`INSTR(LOWER(CAST(payload AS CHAR)), ?)`）
- Modify: `internal/repo/payload_mentions.go:22-24`（`INSTR(CAST(payload AS CHAR), ?)`）
- Modify: `internal/repo/dialect.go`（新增 `payloadContainsExpr` 助手）
- Test: `internal/repo/postgres_mysqlisms_test.go`（集成测试）

**Interfaces:**
- Consumes: Task 1/3 的方言助手
- Produces: `func OpenPostgresTest(t *testing.T) *Store`（放在 `internal/repo/opentest.go`，Task 5 与 Task 9 共用；Task 5 是第一个需要它的任务，所以在这里建）

改写规则：

| 位置 | 旧 | 新（Postgres） |
| --- | --- | --- |
| `payload.go:96` | 列名用反引号 `` `name` `` | Postgres 用双引号；MySQL/sqlite 保持反引号 → 按方言选引号 |
| `payload.go:100-103` | `INSERT IGNORE` | `ON CONFLICT DO NOTHING`（Postgres）；sqlite `INSERT OR IGNORE`、mysql `INSERT IGNORE` 保持 |
| `payload.go:68,79` | `clause.OnConflict{DoUpdates: ...}`（无冲突目标） | Postgres 必须给冲突目标，否则 `ON CONFLICT DO UPDATE` 缺 inference specification 直接报 42601 → `ON CONFLICT ON CONSTRAINT <table>_pkey DO UPDATE`（gorm AutoMigrate 建出的主键约束名恒为 `<table>_pkey`；已在真实库核对 106/106 张表都是这个命名）。MySQL/sqlite 的 clause 保持原样 |
| `catalog.go:80` | `CAST(payload AS CHAR) = CAST(? AS CHAR)` | Postgres 用 `payload = ?::jsonb`：`CAST(x AS CHAR)` 在 Postgres 是 `bpchar(1)`，只比首字符，会把「乐观并发」守卫退化成几乎恒真、静默丢失并发更新；jsonb 规范化也会让文本等值不成立（键序/空白被重排） |
| `integration_lock.go:37` | `SELECT GET_LOCK(?, ?)` | `SELECT pg_try_advisory_lock(?)` 循环直到超时 |
| `integration_lock.go:46` | `SELECT RELEASE_LOCK(?)` | `SELECT pg_advisory_unlock(?)` |
| `assistant_generations.go:152,179` | `UTC_TIMESTAMP(3)` | `now()`（用 `d.nowMillisLiteral()`） |
| `assistant_generations.go:171` | `UTC_TIMESTAMP(3) - INTERVAL ? MICROSECOND` | `now() - (? \|\| ' microseconds')::interval` |
| `workspace_checkpoints.go:470,488` | 同上 | 同上 |
| `catalog.go:481` | `CONVERT(? USING utf8mb4) COLLATE utf8mb4_unicode_ci = ?` | Postgres 直接 `? = ?`（Postgres 区分大小写，与 ID 语义一致） |
| `run_evals.go:120` | `LIMIT 18446744073709551615 OFFSET ?` | `LIMIT ? OFFSET ?` 传 `math.MaxInt64`（PG 不接受 2^64-1） |
| `rooms.go:114` | `CAST(payload AS CHAR)` | `payload::text` |
| `assistant_cli_notice.go:105`、`payload_mentions.go:23` | `INSTR(CAST(payload AS CHAR), ?) > 0` | Postgres 没有 `INSTR`，而 `CAST(payload AS CHAR)` 是 `bpchar(1)`（只剩首字符），子串搜索会永远落空 → `strpos(lower(payload::text), lower(?)) > 0`（MySQL 的 `utf8mb4_unicode_ci` 让 `INSTR` 不区分大小写，所以两边都 `lower`） |
| `schedule_job_executions.go:257` | `INSTR(LOWER(CAST(payload AS CHAR)), ?) > 0` | 同上：`strpos(lower(payload::text), lower(?)) > 0` |

`insertIgnore` 的 Postgres 写法：

```go
func insertIgnore(db *gorm.DB, table string, values map[string]any) (bool, error) {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	cols := make([]string, len(names))
	placeholders := make([]string, len(names))
	args := make([]any, 0, len(names))
	quote := "`"
	if db.Dialector.Name() == dialectPostgres {
		quote = `"`
	}
	for i, name := range names {
		cols[i] = quote + name + quote
		placeholders[i] = "?"
		args = append(args, values[name])
	}
	verb := "INSERT OR IGNORE"
	switch db.Dialector.Name() {
	case dialectMySQL:
		verb = "INSERT IGNORE"
	case dialectPostgres:
		verb = "INSERT INTO"
	}
	stmt := fmt.Sprintf("%s INTO %s (%s) VALUES (%s)", verb, table, strings.Join(cols, ","), strings.Join(placeholders, ","))
	if db.Dialector.Name() == dialectPostgres {
		stmt += " ON CONFLICT DO NOTHING"
	}
	res := db.Exec(stmt, args...)
	return res.RowsAffected > 0, res.Error
}
```

`upsert`/`upsertColumns` 的 Postgres 写法：

```go
// upsert inserts a row, replacing every column when the primary key already
// exists. Callers pass the full desired row; CreatedAt preservation is the
// caller's responsibility (it copies the old value before writing).
func upsert(db *gorm.DB, table string, values map[string]any) error {
	cols := make([]string, 0, len(values))
	for name := range values {
		cols = append(cols, name)
	}
	sort.Strings(cols)
	cl := clause.OnConflict{DoUpdates: clause.AssignmentColumns(cols)}
	if db.Dialector.Name() == dialectPostgres {
		// Postgres requires a conflict target for DO UPDATE. gorm's AutoMigrate
		// names the primary key constraint of every table "<table>_pkey".
		cl.OnConstraint = table + "_pkey"
	}
	if err := db.Table(table).Clauses(cl).Create(values).Error; err != nil {
		return fmt.Errorf("%s write: %w", table, err)
	}
	return nil
}
```

`upsertColumns` 同样只加 `cl.OnConstraint = table + "_pkey"`（Postgres 分支），MySQL/sqlite 一行不改。

为什么用 `ON CONSTRAINT <table>_pkey` 而不是 `ON CONFLICT (列名)`：upsert 的调用点有 50 多处，主键各不相同（`id`、`user_id`、`space_id+user_id`、`mcp_id+path`、`artifact_id+version`…），逐个传列名要改几十个调用点；而 Postgres 把表级主键约束统一命名为 `<table>_pkey`（真实库里 106/106 张表核对一致），约束名可以从表名直接推出来。

与 MySQL 的行为差异（要写进 Task 5 的报告）：MySQL 的 `ON DUPLICATE KEY UPDATE` 撞**任意**唯一键都算冲突并更新那一行；Postgres 的 `ON CONFLICT ON CONSTRAINT <table>_pkey` 只处理主键冲突，撞到别的唯一索引（`users.uq_users_login`、`run_previews.uq_run_previews_run`、`preview_sessions.uq_preview_sessions_hash` 等 11 个）会报唯一约束冲突而不是静默改别人的行。这是更安全的方向，但属于行为差异，必须在集成测试里显式覆盖。

`catalog.go:80` 的 Postgres 写法：

```go
	// Postgres 的 CAST(x AS CHAR) 是 bpchar(1)，只保留首字符；jsonb 的文本表示
	// 还会被规范化（键序、空白重排），文本等值不可靠。用 jsonb 语义等值。
	where := "id = ? AND CAST(payload AS CHAR) = CAST(? AS CHAR)"
	args := []any{id, string(original)}
	if tx.Dialector.Name() == dialectPostgres {
		where = "id = ? AND payload = ?::jsonb"
	}
	result := tx.Table(table).Where(where, args...).Updates(map[string]any{"payload": payload, "updated_at": timePtr(now)})
```

三处「payload 文本里是否包含某个子串」的 Postgres 写法（`assistant_cli_notice.go`、`schedule_job_executions.go`、`payload_mentions.go`）共用一个助手，加在 `internal/repo/dialect.go`：

```go
// payloadContainsExpr returns an expression that is true when the payload
// column's text contains the bound needle.
//
// MySQL's INSTR is case-insensitive under the utf8mb4_unicode_ci collation.
// Postgres has no INSTR, and its CAST(payload AS CHAR) is bpchar(1) — only
// the first character — so the Postgres branch searches the lowercased jsonb
// text instead.
func payloadContainsExpr(db *gorm.DB) string {
	switch db.Dialector.Name() {
	case dialectPostgres:
		return "strpos(lower(payload::text), lower(?)) > 0"
	case dialectMySQL:
		return "INSTR(CAST(payload AS CHAR), ?) > 0"
	default:
		return "instr(CAST(payload AS TEXT), ?) > 0"
	}
}
```

三个调用点各自插入 Postgres 分支，**MySQL/sqlite 的字符串保持原样**：

`assistant_cli_notice.go` 的 `wherePayloadHas`：

```go
	expr := "instr(CAST(payload AS TEXT), ?) > 0"
	if db.Dialector.Name() == dialectMySQL {
		expr = "INSTR(CAST(payload AS CHAR), ?) > 0"
	}
	if db.Dialector.Name() == dialectPostgres {
		expr = payloadContainsExpr(db)
	}
```

`payload_mentions.go` 的 `payloadMentions`：

```go
	has := "instr(CAST(payload AS TEXT), ?) > 0"
	if db.Dialector.Name() == dialectMySQL {
		has = "INSTR(CAST(payload AS CHAR), ?) > 0"
	}
	if db.Dialector.Name() == dialectPostgres {
		has = payloadContainsExpr(db)
	}
```

`schedule_job_executions.go` 的搜索（它的 MySQL 形态多一层 `LOWER`，所以这里只换 Postgres 分支）：

```go
	cast := "LOWER(CAST(payload AS TEXT))"
	if r.DB.Dialector.Name() == dialectMySQL {
		cast = "LOWER(CAST(payload AS CHAR))"
	}
	contains := "INSTR(" + cast + ", ?) > 0"
	if r.DB.Dialector.Name() == dialectPostgres {
		contains = payloadContainsExpr(r.DB)
	}
	if err := db.Where(contains, search).Order("started_at DESC").Order("id DESC").Find(&rows).Error; err != nil {
```

注意 `payload::text` 是 jsonb 的规范化文本表示（键序、空白被重排），子串搜索不受影响；但 `payload_mentions.go` 找的是**完整 ID**，`schedule_job_executions.go` 找的是失败文案，`assistant_cli_notice.go` 找的是错误文案——三者的 needle 都不是 jsonb 结构的一部分，文本规范化不会让它们漏匹配。集成测试要为这三处各写一条用例（Postgres 下能命中、needle 不存在时不命中）。

`integration_lock.go` 的 Postgres 写法：

锁的 key 由 Postgres 内置的 `hashtext()` 在 SQL 里算，Go 侧不自己实现哈希（Postgres 的 `hashtext` 是内部函数，算法没有稳定性承诺，Go 侧复刻更容易失配）。

```go
	if r.isPostgres() {
		deadline := time.Now().Add(wait)
		for {
			var got bool
			if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext(?))", name).Scan(&got); err != nil {
				_ = conn.Close()
				return nil, err
			}
			if got {
				break
			}
			if time.Now().After(deadline) {
				_ = conn.Close()
				return nil, fmt.Errorf("等待仓库 %s 的合入锁超时（%s）", repositoryID, wait)
			}
			time.Sleep(50 * time.Millisecond)
		}
		return func() {
			_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(hashtext(?))", name)
			_ = conn.Close()
		}, nil
	}
```

注意：`pg_try_advisory_lock` 是**会话级**锁，必须绑定在同一条连接上（上面的 `conn` 已经是独占连接），释放时用同一条连接。MySQL 分支原样保留。另外 `!r.isMySQL()` 的本地互斥分支要改成「MySQL 走命名锁、Postgres 走 advisory lock、sqlite 走本地互斥」三路，不能让 Postgres 落到本地互斥上。

`assistant_generations.go:171` 的 Postgres 写法：

```go
func (r *AssistantRepo) staleGenerationFilter(q *gorm.DB) *gorm.DB {
	if r.isPostgres() {
		return q.Where("updated_at < now() - (? || ' microseconds')::interval", assistantHeartbeatTimeout.Microseconds())
	}
	return q.Where("updated_at < UTC_TIMESTAMP(3) - INTERVAL ? MICROSECOND", assistantHeartbeatTimeout.Microseconds())
}
```

（若该函数当前形态与上例不同，按同样的方言分支改写即可；`workspace_checkpoints.go:470` 同理。）

- [ ] **Step 1: 先建 Postgres 集成测试基建**

Task 5 是第一个需要真实 Postgres 的任务，所以测试基建在这里建（后面的 Task 6/9 复用）：

1. 在 `internal/repo/opentest.go` 追加：

```go
// OpenPostgresTest opens a store against AGENTFLOW_TEST_POSTGRES_URL and
// skips the test when the variable is unset. The database must exist before
// the test runs; the store creates its schema through the same incremental
// DDL the platform uses.
func OpenPostgresTest(t *testing.T) *Store {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_POSTGRES_URL is not set")
	}
	store, err := Open(dsn)
	if err != nil {
		t.Fatalf("open postgres test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
```

（需要在该文件补 `"strings"` 与 `"testing"` import。注意 `opentest.go` 属于生产包但只被测试调用，与 `AGENTS.md` 里「测试专用的默认数据只存在 internal/repo/opentest.go」一致。）

2. 建测试库（`loopforge` 是运行库，测试用独立的 `loopforge_test`）：

```bash
docker exec supabase-db psql -U supabase_admin -d postgres -tAc "CREATE DATABASE loopforge_test" 2>&1 || true
docker exec supabase-db psql -U supabase_admin -d postgres -tAc "ALTER DATABASE loopforge_test OWNER TO postgres" 2>&1 || true
```

（`ALTER DATABASE ... OWNER TO postgres` 不能省：Supabase 的 `public` schema 归 `pg_database_owner`，`postgres` 角色不是库 owner 时建表会报 `permission denied for schema public`。）

- [ ] **Step 2: 写集成测试**

创建 `internal/repo/postgres_mysqlisms_test.go`：

```go
package repo

import (
	"testing"
	"time"
)

func TestPostgresInsertIgnoreKeepsExistingRow(t *testing.T) {
	store := OpenPostgresTest(t)
	values := map[string]any{"id": "pg-insert-ignore", "name": "first", "payload": []byte(`{"v":1}`)}
	if err := insertIgnore(store.DB, "generic_templates", values); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	values["name"] = "second"
	if err := insertIgnore(store.DB, "generic_templates", values); err != nil {
		t.Fatalf("second insert: %v", err)
	}
	var name string
	if err := store.DB.Table("generic_templates").Select("name").Where("id = ?", "pg-insert-ignore").Scan(&name).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if name != "first" {
		t.Fatalf("insertIgnore overwrote the row: name=%q", name)
	}
}

func TestPostgresUpsertUpdatesExistingRow(t *testing.T) {
	store := OpenPostgresTest(t)
	values := map[string]any{"id": "pg-upsert", "name": "first", "payload": []byte(`{"v":1}`)}
	if err := upsert(store.DB, "generic_templates", values); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	values["name"] = "second"
	if err := upsert(store.DB, "generic_templates", values); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	var name string
	if err := store.DB.Table("generic_templates").Select("name").Where("id = ?", "pg-upsert").Scan(&name).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if name != "second" {
		t.Fatalf("upsert did not update the row: name=%q", name)
	}
}

func TestPostgresIntegrationLockSerializes(t *testing.T) {
	store := OpenPostgresTest(t)
	unlock, err := store.DeliveryRepo.LockIntegration(t.Context(), "pg-lock-repo", 2*time.Second)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer unlock()
	// 同一把锁在另一条连接上必须拿不到：用一条独立连接验证 advisory lock 的排他性。
	sqlDB, err := store.DB.DB()
	if err != nil {
		t.Fatalf("sql handle: %v", err)
	}
	other, err := sqlDB.Conn(t.Context())
	if err != nil {
		t.Fatalf("second connection: %v", err)
	}
	defer other.Close()
	var got bool
	if err := other.QueryRowContext(t.Context(), "SELECT pg_try_advisory_lock(hashtext(?))", integrationLockName("pg-lock-repo")).Scan(&got); err != nil {
		t.Fatalf("advisory lock probe: %v", err)
	}
	if got {
		t.Fatal("the advisory lock was not exclusive across connections")
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `AGENTFLOW_TEST_POSTGRES_URL='postgres://postgres.cesar:'"$POSTGRES_PASSWORD"'@127.0.0.1:5432/loopforge_test?sslmode=disable' go test ./internal/repo/ -run 'TestPostgresInsertIgnore|TestPostgresUpsert'`
Expected: FAIL（`INSERT IGNORE` 在 Postgres 上是语法错误；`upsert` 缺冲突目标，报 `ON CONFLICT DO UPDATE requires inference specification or constraint name`）

- [ ] **Step 4: 实现改写**

按上表逐处改写 13 个文件。

- [ ] **Step 5: 编译 + 既有测试**

Run: `go build ./... && go test ./internal/repo/...`
Expected: PASS（sqlite 用例全绿；MySQL 用例因 `AGENTFLOW_TEST_MYSQL_DSN` 未设置而跳过）

- [ ] **Step 6: 提交**

```bash
git add internal/repo/
git commit -m "feat(db): 非 JSON 的 MySQL 专有点增加 Postgres 分支

GET_LOCK→pg_advisory_lock、INSERT IGNORE→ON CONFLICT、UTC_TIMESTAMP→now()、
去掉 COLLATE 模拟、去掉 MySQL 的无限 LIMIT。"
```

---

### Task 6: LIKE → ILIKE（大小写语义对齐）

**Files:**
- Modify: `internal/repo/delivery.go:746,1441`
- Modify: `internal/repo/analytics_business_merge.go:30`
- Modify: `internal/repo/rooms.go:114`
- Modify: `internal/repo/assistant_context.go:59,64`
- Modify: `internal/repo/runs_sop_detection.go:43,83`
- Modify: `internal/repo/run_evals.go:81,107,120,123`
- Modify: `internal/repo/knowledge_usage.go:542`
- Modify: `internal/repo/assistant_shared.go:74,94`
- Modify: `internal/repo/run_admission.go:192`
- Modify: `internal/repo/run_auto_admission.go:286`
- Modify: `internal/repo/workspace_checkpoints.go:188`
- Modify: `internal/repo/run_knowledge.go:28`
- Test: `internal/repo/postgres_case_test.go`

**Interfaces:**
- Consumes: Task 3 的 `(d *deps) likeOperator()`
- Produces: 无新增公共接口

背景：MySQL 的 `utf8mb4_unicode_ci` 让 `LIKE` 不区分大小写，Postgres 区分。为了保持搜索行为一致，Postgres 下所有 `LIKE` 改成 `ILIKE`。

改写方式：在每个含 `LIKE` 的查询构造处，用 `r.likeOperator()` 拼出操作符，例如：

```go
	like := r.likeOperator()
	if err := r.DB.Where("run_id IN ? AND content "+like+" ? AND name NOT "+like+" ? AND name NOT "+like+" ?", ids, "%merge_requests/%", "%patch%", "%diff%"). ...
```

对于内嵌在 SQL 字符串常量里的 `NOT LIKE`（`assistant_context.go:59,64` 的 `'已确认并执行 %...%'` 模式），Postgres 分支里改成 `NOT ILIKE`。

注意 `knowledge_usage.go:542` 用了 `ESCAPE '!'`，Postgres 支持 `ILIKE ... ESCAPE '!'`，语法不变。

`run_evals.go:120` 的原生 SQL 字符串同样处理：

```go
	err = db.Raw("SELECT id FROM run_evals WHERE run_id = ? AND id NOT "+r.likeOperator()+" 'sop-check-%' ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?", runID, math.MaxInt64, maxRunEvalsPerRun).Scan(&ids).Error
```

（同时完成 Task 5 里 `LIMIT 18446744073709551615` → `LIMIT ?` 的改写。）

- [ ] **Step 1: 写集成测试**

创建 `internal/repo/postgres_case_test.go`：

```go
package repo

import "testing"

func TestPostgresSearchIsCaseInsensitive(t *testing.T) {
	store := OpenPostgresTest(t)
	if err := upsert(store.DB, "generic_templates", map[string]any{
		"id":         "pg-case-template",
		"name":       "MixedCase Template",
		"payload":    []byte(`{"id":"pg-case-template"}`),
		"updated_at": "2026-01-01 00:00:00",
	}); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	rows := payloadList[map[string]any](store.DB, "generic_templates", "name "+store.likeOperator()+" ?", "mixedcase%")
	if len(rows) == 0 {
		t.Fatal("case-insensitive search found nothing")
	}
}
```

- [ ] **Step 2: 实现改写**

按上表逐处改写 13 个文件。

- [ ] **Step 3: 跑测试**

Run: `go build ./... && go test ./internal/repo/...`
Expected: PASS

- [ ] **Step 4: 提交**

```bash
git add internal/repo/
git commit -m "feat(db): Postgres 下用 ILIKE 对齐 MySQL 的大小写不敏感搜索"
```

---

### Task 7: 种子与观测生成列的 Postgres 脚本

**Files:**
- Create: `migrations/seed/pg/001_public_catalog.sql`
- Create: `migrations/seed/pg/002_welcome_project.sql`
- Create: `migrations/001_run_obs_generated_columns.pg.sql`
- Create: `scripts/install-schema-extensions-pg.sh`
- Modify: `scripts/dev.sh`（改调 pg 版脚本）
- Test: 人工（psql 执行）+ `scripts/install-schema-extensions-pg.sh` 的幂等验证

**Interfaces:**
- Consumes: 无（脚本层任务）
- Produces:
  - `scripts/install-schema-extensions-pg.sh`：在 Postgres 上安装生成列 + 种子数据，可重复执行

改写规则：

1. `migrations/seed/pg/001_public_catalog.sql`：从 `migrations/seed/001_public_catalog.sql` 复制，删除 `SET NAMES utf8mb4;` 与 `START TRANSACTION;`，把每条 `INSERT IGNORE INTO t (...) VALUES (...);` 改为
   `INSERT INTO t (...) VALUES (...) ON CONFLICT DO NOTHING;`
   （保留所有 payload 字面量原样，不改一个字节的 JSON 内容。）
2. `migrations/seed/pg/002_welcome_project.sql`：同样处理。
3. `migrations/001_run_obs_generated_columns.pg.sql`：

```sql
-- Postgres 版观测生成列基线（对应 MySQL 的 migrations/001_run_obs_generated_columns.sql）。
-- Postgres 15 支持 GENERATED ALWAYS AS (...) STORED，语义与 MySQL 生成列一致。

ALTER TABLE run_telemetry
  ADD COLUMN adapter VARCHAR(255)
    GENERATED ALWAYS AS (COALESCE(NULLIF(payload::jsonb #>> '{source,adapter}', ''), 'unknown')) STORED;

CREATE INDEX IF NOT EXISTS idx_run_telemetry_run_adapter ON run_telemetry (run_id, adapter);

ALTER TABLE run_logs
  ADD COLUMN level VARCHAR(32)
    GENERATED ALWAYS AS (COALESCE(NULLIF(payload::jsonb #>> '{level}', ''), 'info')) STORED;

CREATE INDEX IF NOT EXISTS idx_run_logs_run_level ON run_logs (run_id, level);
```

Postgres 的 `ADD COLUMN` 不支持 `IF NOT EXISTS`，脚本用 `DO $$ ... $$` 块做幂等：

```sql
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = current_schema() AND table_name = 'run_telemetry' AND column_name = 'adapter'
  ) THEN
    ALTER TABLE run_telemetry
      ADD COLUMN adapter VARCHAR(255)
        GENERATED ALWAYS AS (COALESCE(NULLIF(payload::jsonb #>> '{source,adapter}', ''), 'unknown')) STORED;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'idx_run_telemetry_run_adapter'
  ) THEN
    CREATE INDEX idx_run_telemetry_run_adapter ON run_telemetry (run_id, adapter);
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = current_schema() AND table_name = 'run_logs' AND column_name = 'level'
  ) THEN
    ALTER TABLE run_logs
      ADD COLUMN level VARCHAR(32)
        GENERATED ALWAYS AS (COALESCE(NULLIF(payload::jsonb #>> '{level}', ''), 'info')) STORED;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'idx_run_logs_run_level'
  ) THEN
    CREATE INDEX idx_run_logs_run_level ON run_logs (run_id, level);
  END IF;
END $$;
```

4. `scripts/install-schema-extensions-pg.sh`：

```bash
#!/usr/bin/env bash
# Install the Postgres equivalents of the Compose MySQL extension steps:
# the observability generated columns and the starter catalog seed.
# The DSN comes from AGENTFLOW_DATABASE_URL in .env.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ -f .env ]]; then
  set -a
  source .env
  set +a
fi
dsn="${AGENTFLOW_DATABASE_URL:-}"
if [[ -z "$dsn" ]]; then
  printf 'AGENTFLOW_DATABASE_URL is not set. Run scripts/configure.sh first.\n' >&2
  exit 1
fi
psql "$dsn" -v ON_ERROR_STOP=1 -f migrations/001_run_obs_generated_columns.pg.sql
psql "$dsn" -v ON_ERROR_STOP=1 -f migrations/seed/pg/001_public_catalog.sql
psql "$dsn" -v ON_ERROR_STOP=1 -f migrations/seed/pg/002_welcome_project.sql
printf 'Postgres schema extensions installed.\n'
```

注意 `source .env` 时 `.env` 的值是 `shlex.quote` 过的，Postgres URL 里的 `?`、`&` 在 shell 中需要引号——`configure.sh` 用 `shlex.quote` 会把整串加引号，source 后即为纯字符串，安全。

- [ ] **Step 1: 生成种子脚本**

```bash
mkdir -p migrations/seed/pg
python3 - <<'PY'
import re, pathlib
for name in ("001_public_catalog", "002_welcome_project"):
    src = pathlib.Path(f"migrations/seed/{name}.sql").read_text()
    out = src.replace("SET NAMES utf8mb4;\n", "").replace("START TRANSACTION;\n", "")
    out = re.sub(r"INSERT IGNORE INTO ", "INSERT INTO ", out)
    out = re.sub(r"(INSERT INTO [^\n]*?VALUES \(.*?\));", r"\1 ON CONFLICT DO NOTHING;", out, flags=re.S)
    pathlib.Path(f"migrations/seed/pg/{name}.sql").write_text(out)
    print(name, "->", len(out))
PY
```

改完后逐行核对：`grep -c "ON CONFLICT DO NOTHING" migrations/seed/pg/*.sql` 应等于原文 `INSERT IGNORE` 的条数；`grep -c "INSERT IGNORE" migrations/seed/pg/*.sql` 应为 0；payload 字面量与原文逐字节相同（用 `diff <(sed 's/ON CONFLICT DO NOTHING;//' migrations/seed/pg/001_public_catalog.sql | sed 's/INSERT INTO /INSERT IGNORE INTO /') migrations/seed/001_public_catalog.sql` 验证差异只剩被改写的部分）。

- [ ] **Step 2: 写生成列脚本**

按上文内容创建 `migrations/001_run_obs_generated_columns.pg.sql`。

- [ ] **Step 3: 写安装脚本并加执行权限**

```bash
chmod +x scripts/install-schema-extensions-pg.sh
```

- [ ] **Step 4: 在真实库上验证幂等**

```bash
cd /Users/caijinhai/Documents/services/supabase && set -a && source .env && set +a && \
export AGENTFLOW_DATABASE_URL="postgres://postgres.cesar:${POSTGRES_PASSWORD}@127.0.0.1:5432/loopforge?sslmode=disable" && \
cd /Users/caijinhai/Documents/projects/loopForge && \
bash scripts/install-schema-extensions-pg.sh && bash scripts/install-schema-extensions-pg.sh
```

Expected: 两次都成功退出（幂等），且

```bash
PGPASSWORD="$POSTGRES_PASSWORD" psql -h 127.0.0.1 -p 5432 -U postgres.cesar -d loopforge -tAc \
  "select count(*) from generic_templates where id like 'generic-%'"
```

返回 ≥ 3。

- [ ] **Step 5: 提交**

```bash
git add migrations/seed/pg migrations/001_run_obs_generated_columns.pg.sql scripts/install-schema-extensions-pg.sh scripts/dev.sh
git commit -m "feat(db): Postgres 版种子与观测生成列脚本"
```

---

### Task 8: 部署脚本与 compose 切换到 Postgres

**Files:**
- Modify: `scripts/configure.sh:62-69`（生成 Postgres DSN）
- Modify: `scripts/dev.sh`（不再起 mysql 容器）
- Modify: `compose.yaml`（去 mysql service、artifact-migration 改 psql、platform 用 PG DSN）
- Modify: `scripts/install.sh`（安装流程不再拉 mysql 镜像）
- Modify: `.env.example`（新增 AGENTFLOW_DATABASE_URL）
- Modify: `docs/deployment.md`（补一段 Postgres 本地部署说明）

**Interfaces:**
- Consumes: Task 1 的 `AGENTFLOW_DATABASE_URL`
- Produces: `.env` 中含 `AGENTFLOW_DATABASE_URL=postgres://...`

- [ ] **Step 1: configure.sh 写 Postgres DSN**

`scripts/configure.sh` 的 python 段（第 62-69 行）改为：

```python
else:
 http_port=os.environ.get('AGENTFLOW_HTTP_PORT','8090')
 data_dir=os.environ.get('LOOPFORGE_DATA_DIR','.data')
 pg_host=os.environ.get('LOOPFORGE_PG_HOST','127.0.0.1')
 pg_port=os.environ.get('LOOPFORGE_PG_PORT','5432')
 pg_user=os.environ.get('LOOPFORGE_PG_USER','postgres.cesar')
 pg_db=os.environ.get('LOOPFORGE_PG_DATABASE','loopforge')
 pg_password=os.environ.get('LOOPFORGE_PG_PASSWORD','')
 if not pg_password:
  raise SystemExit('LOOPFORGE_PG_PASSWORD must be set: it is the password of the Supabase Postgres role')
 dsn=f'postgres://{pg_user}:{pg_password}@{pg_host}:{pg_port}/{pg_db}?sslmode=disable'
 data={'AGENTFLOW_DATABASE_URL':dsn,'COMPOSE_PROJECT_NAME':os.environ.get('COMPOSE_PROJECT_NAME','loopforge'),'AGENTFLOW_HTTP_PORT':http_port,'AGENTFLOW_SESSION_SECRET':secrets.token_hex(32),'AGENTFLOW_CREDENTIAL_KEYS':credential_keys(),'AGENTFLOW_AUTH_REQUIRED':'true','LOOPFORGE_DATA_DIR':data_dir,'LOOPFORGE_BIND':os.environ.get('LOOPFORGE_BIND','0.0.0.0'),'LOOPFORGE_PUBLIC_URL':os.environ.get('LOOPFORGE_PUBLIC_URL',''),'LOOPFORGE_IMAGE':os.environ.get('LOOPFORGE_IMAGE',''),'AGENTFLOW_GITHUB_CLIENT_ID':os.environ.get('AGENTFLOW_GITHUB_CLIENT_ID',''),'AGENTFLOW_PROJECT_CREDENTIAL_KEY':project_credential_key()}
```

（`project_credential_key` 已在原脚本第 66 行单独赋值，保留即可。）

- [ ] **Step 2: dev.sh 去掉 mysql 容器**

`scripts/dev.sh` 改为：

```bash
#!/usr/bin/env bash
# Explicit local installation, schema initialization and catalog installation.
set -euo pipefail
cd "$(dirname "$0")/.."
bash scripts/configure.sh
set -a
source .env
set +a
mkdir -p .local/bin .local/workspaces
bash scripts/build-binaries.sh
if [[ ! -f .local/admin-password ]]; then
  (umask 077; python3 -c 'import secrets; print(secrets.token_urlsafe(18))' > .local/admin-password)
fi
.local/bin/loopforge-account -user admin -admin < .local/admin-password
bash scripts/install-schema-extensions-pg.sh
printf 'Open http://localhost:%s — account: admin — password: .local/admin-password\n' "$AGENTFLOW_HTTP_PORT"
export AGENTFLOW_RUNNER_BINARY="$PWD/.local/bin/agentflow-runner-{os}-{arch}"
export AGENTFLOW_GATE_MCP_BINARY="$PWD/.local/bin/agentflow-gate-mcp-{os}-{arch}"
printf '%s\n' "$$" > .local/platform.pid
exec .local/bin/agentflow -addr "${LOOPFORGE_BIND:-0.0.0.0}:$AGENTFLOW_HTTP_PORT" -workspaces "$PWD/.local/workspaces"
```

注意：`loopforge-account` 依赖 `AGENTFLOW_DATABASE_URL`（`repo.Open("")` 会读环境变量），因此 `.env` 必须在它之前 source。

- [ ] **Step 3: compose.yaml 切换**

改动：
- 删除 `mysql` service 与 `mysql-data` volume。
- `platform.environment` 里 `AGENTFLOW_MYSQL_DSN` 改为
  `AGENTFLOW_DATABASE_URL: ${AGENTFLOW_DATABASE_URL:?Run scripts/configure.sh first}`
- `platform.depends_on` 去掉 `mysql` 与 `artifact-migration`。
- `artifact-migration` service 改为用 `postgres:15` 客户端执行 `migrations/001_run_obs_generated_columns.pg.sql`：

```yaml
  artifact-migration:
    profiles: [platform]
    image: postgres:15
    entrypoint: ["sh", "-ec"]
    command: ["exec psql \"$AGENTFLOW_DATABASE_URL\" -v ON_ERROR_STOP=1 -f /migrations/001_run_obs_generated_columns.pg.sql"]
    environment:
      AGENTFLOW_DATABASE_URL: ${AGENTFLOW_DATABASE_URL:?Run scripts/configure.sh first}
    volumes:
      - type: bind
        source: ./migrations/001_run_obs_generated_columns.pg.sql
        target: /migrations/001_run_obs_generated_columns.pg.sql
        read_only: true
        bind:
          create_host_path: false
```

- `platform` 的 `extra_hosts` 已有 `host.docker.internal:host-gateway`，供容器访问宿主机 Supabase 用。

- [ ] **Step 4: .env.example 补 Postgres 变量**

在 `.env.example` 顶部加：

```
# Postgres DSN (Supabase). LOOPFORGE_PG_PASSWORD is read from your Supabase .env.
AGENTFLOW_DATABASE_URL=postgres://postgres.cesar:REPLACE_ME@127.0.0.1:5432/loopforge?sslmode=disable
LOOPFORGE_PG_PASSWORD=
```

并删除 `AGENTFLOW_MYSQL_DSN`、`LOOPFORGE_DB_PASSWORD`、`LOOPFORGE_DB_ROOT_PASSWORD`、`LOOPFORGE_MYSQL_PORT` 四行（MySQL 路径在本 fork 中不再是部署方式）。

- [ ] **Step 5: install.sh / start.sh 去掉 MySQL 依赖**

`scripts/install.sh` 改动：
- 第 171-198 行的 `docker_access compose pull mysql` 段落整体删除（不再拉 mysql 镜像）。
- 第 198 行 `users=$(docker_access compose exec -T mysql ... 'select count(*) from users')` 改为走 Postgres：

```bash
  users=$(docker_access compose exec -T platform sh -c 'exec agentflow-account-count' 2>/dev/null || echo 0)
```

若没有现成的账号计数命令，改为直接问数据库：

```bash
  users=$(docker_access compose exec -T artifact-migration sh -c 'psql "$AGENTFLOW_DATABASE_URL" -tAc "select count(*) from users"' 2>/dev/null || echo 0)
```

- 安装末尾改为执行 `bash scripts/install-schema-extensions-pg.sh`（替代 `install-schema-extensions.sh` 的 mysql 调用）。

`scripts/start.sh` 改动：
- `sandbox_mysql()` 函数与 `apply_sandbox_migration()`（MySQL 迁移 041）删除或改为 Postgres 版本：

```bash
sandbox_pg() {
  compose exec -T artifact-migration sh -c 'exec psql "$AGENTFLOW_DATABASE_URL" --batch --skip-column-names'
}
```

- `upgrade_sandbox_catalog()` 里用 `sandbox_mysql` 备份/恢复的部分改为 `sandbox_pg`（`pg_dump`/`psql`）。

- [ ] **Step 6: docs/deployment.md 补一节**

在 `docs/deployment.md` 末尾追加：

```markdown
## Postgres 本地部署（本 fork）

平台连接用户自带的 Supabase Postgres，不再启动 MySQL 容器。

1. 在 Supabase Postgres 里建库：`CREATE DATABASE loopforge;`
2. 导出 Supabase 的 `POSTGRES_PASSWORD`，然后：

```bash
export LOOPFORGE_PG_PASSWORD='<POSTGRES_PASSWORD>'
./start.sh dev
```

连接串形如 `postgres://postgres.cesar:<password>@127.0.0.1:5432/loopforge?sslmode=disable`。
Supavisor 租户段（`postgres.cesar` 中的 `cesar`）必须与 Supabase 的 `POOLER_TENANT_ID` 一致。
```

- [ ] **Step 7: 提交**

```bash
git add scripts/configure.sh scripts/dev.sh compose.yaml .env.example docs/deployment.md scripts/install.sh scripts/start.sh
git commit -m "feat(deploy): 部署流程切换到 Postgres（Supabase）"
```

---

### Task 9: Postgres 集成冒烟用例

**Files:**
- Create: `internal/repo/postgres_smoke_test.go`
- Modify: `docs/testing/`（如有测试说明文档，补一段）

**Interfaces:**
- Consumes: Task 5 已建好的 `OpenPostgresTest`、Task 1 的 `repo.Open`、Task 2 的列类型修正
- Produces: 无新增公共接口

- [ ] **Step 1: 写冒烟测试**

创建 `internal/repo/postgres_smoke_test.go`：

```go
package repo

import "testing"

func TestPostgresSmoke(t *testing.T) {
	store := OpenPostgresTest(t)
	if store.DB.Dialector.Name() != dialectPostgres {
		t.Fatalf("expected postgres dialect, got %s", store.DB.Dialector.Name())
	}
	var tableCount int64
	if err := store.DB.Raw("SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema()").Scan(&tableCount).Error; err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if tableCount < 50 {
		t.Fatalf("schema looks incomplete: %d tables", tableCount)
	}
	var jsonColumns int64
	if err := store.DB.Raw("SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND data_type = 'json'").Scan(&jsonColumns).Error; err != nil {
		t.Fatalf("count json columns: %v", err)
	}
	if jsonColumns != 0 {
		t.Fatalf("json columns were not converted to jsonb: %d remain", jsonColumns)
	}
}
```

- [ ] **Step 2: 跑测试**

```bash
cd /Users/caijinhai/Documents/services/supabase && set -a && source .env && set +a && \
AGENTFLOW_TEST_POSTGRES_URL="postgres://postgres.cesar:${POSTGRES_PASSWORD}@127.0.0.1:5432/loopforge_test?sslmode=disable" \
go test ./internal/repo/ -run TestPostgresSmoke -v
```

Expected: PASS

- [ ] **Step 5: 跑全量集成测试**

```bash
cd /Users/caijinhai/Documents/services/supabase && set -a && source .env && set +a && \
AGENTFLOW_TEST_POSTGRES_URL="postgres://postgres.cesar:${POSTGRES_PASSWORD}@127.0.0.1:5432/loopforge_test?sslmode=disable" \
go test ./internal/repo/... ./internal/httpapi/...
```

Expected: PASS（Postgres 用例通过；MySQL 集成用例跳过）

- [ ] **Step 4: 提交**

```bash
git add internal/repo/opentest.go internal/repo/postgres_smoke_test.go
git commit -m "test(db): Postgres 集成测试基建与冒烟用例"
```

---

### Task 10: 端到端启动与验收冒烟

**Files:**
- 无代码改动；如发现缺陷，按缺陷所在文件修复并单独提交。

**Interfaces:**
- Consumes: Task 8 的 `scripts/dev.sh`、Task 7 的安装脚本
- Produces: 可运行的本地实例（`127.0.0.1:8090`）+ 验收记录

- [ ] **Step 1: 准备环境变量并启动**

```bash
cd /Users/caijinhai/Documents/projects/loopForge
export LOOPFORGE_PG_PASSWORD="$(grep -E '^POSTGRES_PASSWORD=' /Users/caijinhai/Documents/services/supabase/.env | cut -d= -f2-)"
bash scripts/dev.sh
```

Expected: 日志显示平台启动，监听 `127.0.0.1:8090`。

- [ ] **Step 2: 健康检查**

```bash
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8090/api/health
```

Expected: `200`

- [ ] **Step 3: 登录与建项目**

用 `.local/admin-password` 里的密码登录 `http://127.0.0.1:8090`，创建项目「PG 冒烟」，确认项目出现在列表里。

- [ ] **Step 4: 跑内置 demo LOOP**

进入「模板」→「Platform check · script & approval」→ 运行 LOOP：脚本节点应写出报告，人工门禁应停住等待审批；批准后运行应走到最终产物。

- [ ] **Step 5: 逐页检查 analytics / observability**

打开以下页面并确认没有 500 与 SQL 报错日志：

- 概览 / Dashboard
- 活动 / Activity
- 用量 / Usage
- 业务域 / Business domains
- 运行详情 / Run detail（含 run_obs 生成列筛选）

检查平台日志没有 `ERROR ... SQLSTATE` 记录：

```bash
grep -iE "SQLSTATE|ERROR.*relation|syntax error" .local/platform.log | head -20
```

Expected: 无输出。

- [ ] **Step 6: 重启持久化验证**

```bash
# Ctrl-C 停掉进程后重新执行 bash scripts/dev.sh，再打开页面，数据仍在
```

Expected: 项目与运行记录仍在（数据在 Supabase 的 `loopforge` 库里）。

- [ ] **Step 7: 记录验收结果**

把验收 1–6 的实际结果写进 `docs/superpowers/plans/2026-11-28-loopforge-postgres-port.md` 末尾的「验收记录」小节（追加，不改动既有内容）。

- [ ] **Step 8: 提交验收记录**

```bash
git add docs/superpowers/plans/2026-11-28-loopforge-postgres-port.md
git commit -m "docs(plan): 记录 Postgres 部署验收结果"
```

---

## 验收记录

（Task 10 完成后填写）

## 已知缺口

- `migrations/0xx_*.sql` 中的 63 个历史升级迁移保持 MySQL-only，未移植。它们只服务老库升级；新建库走 gorm AutoMigrate。若日后要把已有 MySQL 库的数据搬到 Postgres，需要另做数据迁移器。
- `migrations/033_generic_sop_mr_node.sql`、`038_generic_sop_host_credentials.sql`、`043_loop_terminology.sql` 同样保持 MySQL-only。它们的等价内容已包含在 `migrations/seed/pg/001_public_catalog.sql` 里（种子已含「发起 MR」节点与 LOOP 术语），所以新建库不依赖它们。
- `internal/httpapi/assistant_acceptance.go` 的 `LookupCodeBuddyCredentialReadOnly` 仍按 MySQL DSN 工作（腾讯内网验收流程专用），Postgres 部署不经过该路径。

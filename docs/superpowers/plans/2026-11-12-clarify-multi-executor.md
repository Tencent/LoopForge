# 需求澄清开放多执行器 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让需求澄清可以跑在账号里已配置的任意智能体框架上（CodeBuddy、Codex、Claude Code 与七个 agent CLI），而不是被硬编码在 CodeBuddy。

**Architecture:** 澄清链路保留自己的会话目录、任务 hub 与提示词，但把「引擎相关的四件事」——命令行构造、二进制解析、凭据装配、输出翻译——改为按引擎分派，并直接复用 AI 对话已经验证过的 per-engine builder（`prepareAssistantClaudeInvocation` / `prepareAssistantCodexInvocation` / `prepareAssistantAgentCLIInvocation`）。引擎与模型用一个带前缀的模型 ID 表达（`codex:gpt-5.5`），临时选择存浏览器 localStorage，个人默认存 `user_configs` 新列，可用性由**需求责任人**的配置决定并通过新端点 `GET /api/clarify-engines` 提供给前端分组选择器。

**Tech Stack:** Go 1.26（module `agentflow`，gin，gorm）、原生 JS SPA（`web/app.js` + `web/app/*.js`，无构建步骤）、node 测试（Go wrapper + `webtest` jsdom）、SQLite/MySQL。

**Spec:** `docs/proposals/clarify-multi-executor.md`

## Global Constraints

- 不引入任何新的第三方依赖；Go 1.26.11，module `agentflow`。
- 全量测试：`make test`（= `go test -p=16 ./... -count=1 -timeout=4m`）；单测：`go test ./internal/httpapi -run '^TestXxx$' -count=1`；覆盖率门槛：`bash scripts/coverage-go.sh --min $(tr -d '[:space:]' < scripts/coverage-go.min)`（当前 84.8）。
- 前端 JS 测试通过 Go wrapper 调 node 运行，模式见 `web/issue_clarify_draft_test.go:9-16`（`appBundlePath(t)` 按 `index.html` 的 `<script src>` 顺序拼 bundle）；`webtest` 套件：`npm --prefix webtest ci && npm --prefix webtest test`。
- `web/app.js` 不是构建产物：新增 `web/app/<x>.js` 必须在 `web/index.html` 加 `<script src>`，且 `start.js` 必须最后加载（`web/app_bundle_test.go:69-91` 会检查）。优先把新函数放进已加载的文件（澄清选择器相关放 `web/app/sync-schedule.js`）。
- AGENTS.md：repo 方法只能挂在所属领域 Repo（个人默认归 `UsersRepo`，`internal/repo/users.go`）；handler 方法只能挂在自己的领域 struct（澄清 → `streamHandlers`，凭据/偏好 → `usersHandlers`）；一个文件不得为别的领域定义方法；纯包级 helper 可以单独成文件。
- AGENTS.md：代码不做迁移不做 seed。新增列靠 `autoMigrateIncremental` 自动补，同时**必须**附手写 `migrations/028_clarify_model_preference.sql`（现有序号最大为 `027_cli_model_catalog.sql`）。
- 用户可见文案一律中文，且每个新字符串要在 `web/i18n-en.js` 补英文键（`Object.assign(window.LF_I18N_EN,{…})` 追加行的形式）。
- **绝不静默回退**：用户当场明确选中的引擎缺凭据/缺 CLI 时报错，错误文本要说清缺什么；只有「个人默认失效」这一条允许回退 CodeBuddy，且必须在 UI 上标注。
- 提交信息用 Conventional Commits + 英文 subject（仓库现状：`feat(web): …`、`fix(sop): …`）。
- 前端视觉遵循根目录 `design.md`；澄清选择器沿用现有 `.clarify-model-field` / `.ai-model-toggle` / `.ai-model-menu` 规格，不新造控件样式。

## Review Focus

1. **责任人 ≠ 点击者，且责任人没配该引擎的 Key**：菜单里该引擎整组置灰并显示原因，不允许选中后跑起来才失败 → `TestClarifyEnginesUsesResponsibleUser`（Task 6）。
2. **一张单已经用 X 引擎澄清过，换人/换浏览器继续**：仍用 X；显式换成 Y 时不复用旧 session，改为重放历史，已答问题与结论不丢 → `TestExecuteClarifyPrefersIssueEngine`、`TestExecuteClarifyEngineSwitchStartsFreshSession`（Task 4）、`TestClarifySessionReusableRejectsEngineSwitch`（Task 3）。
3. **控制面没装所选 CLI**：错误文本含 CLI 名称与安装提示，且不出现「实际用 CodeBuddy 跑完」 → `TestExecuteClarifyReportsMissingCLI`（Task 4）。
4. **个人默认所指引擎的 Key 后来被清空**：回退 CodeBuddy，但端点返回 `defaultStale:true`、设置页与选择器显示「已失效」 → `TestClarifyEnginesStaleDefault`（Task 6）。
5. **非法或未知引擎前缀**（`foobar:x`、责任人没配却硬发 `codex:gpt-5.5`）：返回 400 且错误里带上原值，绝不改写成默认模型 → `TestClarifyResolveModelRejectsUnknownEngine`、`TestClarifyResolveModelRejectsEngineWithoutKey`（Task 1）、`TestUserCredentialsRejectsUnknownClarifyEngine`（Task 5）。

---

## File Structure

**后端（`internal/`）**

| 文件 | 责任 | 动作 |
| --- | --- | --- |
| `httpapi/clarify_engine.go` | 澄清的引擎层：`clarifyEngine` 值类型、模型 ID 解析与校验、per-engine 命令构造分派、可用性计算、`GET /api/clarify-engines` handler | 新建 |
| `httpapi/clarify_session.go` | 会话目录、续跑判断、命令 argv（CodeBuddy 方言） | 修改：`clarifyCommandOpts` 加 `Engine`；`canResumeClarifySession` 变引擎感知；新增 `clarifySessionReusable` |
| `httpapi/clarify_mcp.go` | 澄清提示词与 MCP 说明 | 修改：删除 `clarifyAllowedModel`（被 Task 1 的 `clarifyResolveModel` 取代） |
| `httpapi/stream.go` | `executeClarify` / `runClarifyTurn` / `rememberClarifySession` | 修改：引擎解析、二进制、凭据、翻译、`open` 事件带 `engine` |
| `httpapi/server.go` | 路由装配 | 修改：注册 `GET /api/clarify-engines`（紧邻 `server.go:401` 的 `/api/chat-clarify`） |
| `domain/issue.go` | `ClarificationAssessment` | 修改：新增 `ModelID` 字段 |
| `domain/user.go` | `User` | 修改：新增 `ClarifyModel` 字段 |
| `repo/models.go` | `userConfigRow` | 修改：新增 `clarify_model` 列 |
| `repo/users.go` | `UsersRepo` | 修改：`userWithConfig`/`userSelectColumns`/`toDomain`/`userHasConfig`/`writeUserConfig`/`UserCredentialUpdate`/`UpdateUserCredentials` 各加一处 |
| `httpapi/users.go` | `usersHandlers.user` | 修改：校验并透传 `clarifyModel` |
| `migrations/028_clarify_model_preference.sql` | 手动迁移脚本 | 新建 |

**前端（`web/`）**

| 文件 | 责任 | 动作 |
| --- | --- | --- |
| `app/assistant-models.js` | 共享模型目录与选择器 chrome | 修改：新增 `renderGrouped`（分组 + 置灰原因），导出到 `window.AgentFlowAssistantModels`；`renderOptions`/`renderList` 保持原样供其它调用方使用 |
| `app/sync-schedule.js` | 澄清选择器（`selectedClarifyModel`/`setClarifyModel`/`clarifyModelPickerHTML`/`bindClarifyModelPicker`） | 修改：接 `/api/clarify-engines`，改用 `renderGrouped`，选择器带 scope |
| `app/composer.js` | 新建需求向导 | 修改：`paintRequirementComposerChrome` 传 scope；`sendChatClarify` 发送带前缀的模型 ID |
| `app/issue-drawer.js` | 需求详情澄清页 | 修改：按 `issue.clarification.modelId` 预填、发送、显示责任人提示 |
| `app/workbench.js` | 个人配置 → 模型与执行 | 修改：组顶部新增「需求澄清 · 默认框架与模型」偏好行 + 保存 |
| `index.html` | 向导选择器 markup | 修改：`#reqClarifyModelPicker` 加 `data-clarify-scope="project"` |
| `i18n-en.js` | 英文翻译 | 修改：新增字符串 |

**测试**：`internal/httpapi/clarify_engine_test.go`、`clarify_engine_command_test.go`、`clarify_engine_availability_test.go`（新建）；`clarify_mcp_test.go`、`clarify_session_test.go`、`agent_tool_native_terminal_test.go`（改签名/删过时断言）；`internal/repo/users_clarify_pref_test.go`（新建）；`web/clarify_engine_picker_test.js` + `.go`、`web/clarify_engine_entries_test.js` + `.go`（新建）；`web/syntax_test.go`（加断言）；`webtest/tests/clarify-preference.test.mjs`（新建）。

**文档**：`README.md:103`、`README.zh-CN.md:103`、`CHANGELOG.md`（顶部新增 `## 0.3.7 — …`）、`docs/api/collaboration.md:146-169`、`docs/api/openapi.yaml`、`docs/api/README.md` 路由表、`design.md`、`docs/proposals/clarify-multi-executor.md`（状态与验证记录）。

---

### Task 1: 引擎感知的模型解析（`clarifyEngine` / `clarifyResolveModel`）

**Files:**
- Create: `internal/httpapi/clarify_engine.go`
- Test: `internal/httpapi/clarify_engine_test.go`

**Interfaces:**
- Consumes: `assistantModelEngine(id string) (engine, model string)`（`assistant_engines.go:38-45`）、`assistantEngineOptions(user domain.User, sponsored bool) []assistantEngineOption`（`:73-86`）、`assistantEngineCredential(user, engine) (apiKey, baseURL string)`（`:100-114`）、`assistantValidEngineModel(model string) bool`（`:157-167`）、`assistantKnownCatalog() assistantModelCatalog`（`assistant_models.go:141-158`）、`assistantDefaultModel = "default"`、`assistantEngineMissingKey(engine string) string`（`assistant_engines.go:238-240`）、`runtime.AgentCLINeedsBaseURL(kind, protocol string) bool`（`runtime/agent_cli.go:54-61`）、`assistantEngineProtocol(user, engine) string`。
- Produces:
```go
// clarifyEngine 是一轮澄清所用的 CLI 与其模型。
type clarifyEngine struct {
	ID      string // "codebuddy" | "codex" | "claude-code" | agent CLI kind
	Model   string // 去掉引擎前缀后的模型，允许 "default"
	ModelID string // 规范化后的对外 ID：codebuddy 不带前缀，其余为 "<engine>:<model>"
}

func clarifyEngineOf(modelID string) clarifyEngine          // 永不报错；无法识别时 ID 为 codebuddy
func clarifyEngineKnown(engine string) bool                 // engine 是否在 assistantEngineOptions 的全集里
func clarifyResolveModel(modelID string, user domain.User) (clarifyEngine, error)
var errClarifyEngineUnknown = errors.New("未知的智能体框架")
```
`clarifyResolveModel` 的规则（必须严格按此实现，不得静默改写）：
1. `strings.TrimSpace(modelID)` 为空 → `clarifyEngine{ID:"codebuddy", Model:assistantDefaultModel, ModelID:assistantDefaultModel}`，`nil` 错误（与今天 `clarifyAllowedModel("")` 一致）。
2. `engine, model := assistantModelEngine(modelID)`；`!clarifyEngineKnown(engine)` → `fmt.Errorf("%w：%s", errClarifyEngineUnknown, engine)`。
3. `engine == "codebuddy"`：`model` 为 `assistantDefaultModel` 或在 `assistantKnownCatalog().Models` 中 → 通过（`ModelID` 用不带前缀的原值）；否则 `fmt.Errorf("模型 %s 不在 CodeBuddy 目录中", modelID)`。
4. 其它引擎：`apiKey, baseURL := assistantEngineCredential(user, engine)`；`apiKey == ""` → `errors.New(assistantEngineMissingKey(engine))`；`runtime.AgentCLINeedsBaseURL(engine, assistantEngineProtocol(user, engine))` 且 `baseURL == ""` → `fmt.Errorf("%s 需要在个人配置中填写 Base URL", assistantEngineLabel(engine))`；`!assistantValidEngineModel(model)` → `fmt.Errorf("模型 %s 无效", modelID)`。通过时 `ModelID = engine + ":" + model`。

- [ ] **Step 1: 写失败测试** `internal/httpapi/clarify_engine_test.go`

```go
func TestClarifyResolveModelKeepsCodeBuddyDefaults(t *testing.T) // ""→{codebuddy,default,default}；
   // t.Setenv("AGENTFLOW_ASSISTANT_MODELS","glm-5.3-flash-ioa") 后 "glm-5.3-flash-ioa" 原样通过
func TestClarifyResolveModelRejectsUnknownCodeBuddyModel(t *testing.T) // "not-a-model" 返回 error，
   // err.Error() 必须包含 "not-a-model"（证明没有被改写成 default）
func TestClarifyResolveModelAcceptsConfiguredEngine(t *testing.T) // user{CodexAPIKey:"k"} +
   // "codex:gpt-5.5" → ID "codex"、Model "gpt-5.5"、ModelID "codex:gpt-5.5"、err nil
func TestClarifyResolveModelRejectsEngineWithoutKey(t *testing.T) // 无 codex key 时 err.Error() ==
   // assistantEngineMissingKey("codex")
func TestClarifyResolveModelRejectsUnknownEngine(t *testing.T) // "foobar:x" → errors.Is(err, errClarifyEngineUnknown)
func TestClarifyResolveModelRequiresBaseURL(t *testing.T) // user.AgentCLICredentials["opencode"]={APIKey:"k"}
   // 无 BaseURL → err.Error() 含 "Base URL"；补上 BaseURL 后通过
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/httpapi -run '^TestClarifyResolveModel' -count=1`
Expected: 编译失败（`undefined: clarifyResolveModel`）。

- [ ] **Step 3: 实现 `clarify_engine.go`**

按上面 Interfaces 的签名与四条规则实现；`clarifyEngineKnown` 用 `assistantEngineOptions(domain.User{}, false)` 的 ID 集合判断（该函数不依赖 user 就能列出全部引擎）。文件顶部注释说明本文件只放澄清的引擎层 helper 与 `streamHandlers` 方法（AGENTS.md 领域归属）。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/httpapi -run '^TestClarifyResolveModel' -count=1`
Expected: PASS（6 个测试）。

- [ ] **Step 5: 提交**

```bash
git add internal/httpapi/clarify_engine.go internal/httpapi/clarify_engine_test.go
git commit -m "feat(clarify): resolve the clarification engine from a prefixed model id"
```

---

### Task 2: 按引擎构造澄清命令

**Files:**
- Modify: `internal/httpapi/clarify_engine.go`
- Modify: `internal/httpapi/clarify_session.go:205-247`（`clarifyCommandOpts` / `prepareClarifyCommandWith`）
- Test: `internal/httpapi/clarify_engine_command_test.go`

**Interfaces:**
- Consumes: Task 1 的 `clarifyEngine`；`prepareClarifyCommandWith(clarifyCommandOpts) (runtime.CommandSpec, error)`；`assistantEngineInvocationOpts{Bin, Root, Config, Prompt, Model, Effort, SessionID, SystemPrompt string; Resume, ReviewOnly bool; User runtime.ProviderCredential}`（`assistant_engine_cli.go:38-45`）；`prepareAssistantClaudeInvocation(o) (assistantCLIInvocation, error)`（`:160-206`）；`prepareAssistantCodexInvocation(o) (assistantCLIInvocation, error)`（`assistant_engine_codex.go:18-63`）；`prepareAssistantAgentCLIInvocation(kind string, o) (assistantCLIInvocation, error)`（`assistant_engine_agentcli.go:16-77`）；`assistantCLIInvocation{command, credentials, sessionRef, translate, resumeMissing, finish}`（`assistant_engine_cli.go:26-36`）；`runtime.BindAgentCLI`/`BindCodex`/`BindClaudeCode`；`appendDisallowedTools(argv []string, extra string) []string`（`clarify_session.go:246`）。
- Produces:
```go
type clarifyInvocationOpts struct {
	Bin, Root, Config, Settings, Prompt, Model, SystemPrompt, SessionID, RunID string
	Resume, SubmitOnly bool
	User runtime.ProviderCredential // 该引擎对应的凭据（CodeBuddy 走调用方自己的装配）
}

// clarifyInvocation 是各引擎统一后的结果；凭据不在此处装配，由 executeClarify 统一给。
type clarifyInvocation struct {
	Command       runtime.CommandSpec
	Translate     func(string) []string // nil = CLI 本身就说 stream-json
	ResumeMissing bool
	Finish        func(ok bool) // 可为 nil；codex/agent CLI 用它落盘 thread/session
}

func prepareClarifyInvocation(engine clarifyEngine, o clarifyInvocationOpts) (clarifyInvocation, error)
func clarifySubmitOnlySupported(engine clarifyEngine) bool // codebuddy、claude-code 为 true，其余 false
```
分派规则：`codebuddy` → 现有 `prepareClarifyCommandWith`（`clarifyCommandOpts` 新增 `Engine string` 字段，CodeBuddy 分支行为逐字节不变），`Translate=nil`、`Finish=nil`；`claude-code` → `prepareAssistantClaudeInvocation`；`codex` → `prepareAssistantCodexInvocation`；`runtime.IsAgentCLI(engine.ID)` → `prepareAssistantAgentCLIInvocation(engine.ID, …)`；其余 → `fmt.Errorf("%w：%s", errClarifyEngineUnknown, engine.ID)`。非 CodeBuddy 分支把 `assistantCLIInvocation` 的 `command/translate/resumeMissing/finish` 映射进 `clarifyInvocation`，`credentials` 字段丢弃。`SubmitOnly` 且 `clarifySubmitOnlySupported` 为 false 时不改 argv（降级为整体只读），由重试提示词负责收敛。

- [ ] **Step 1: 写失败测试** `internal/httpapi/clarify_engine_command_test.go`

```go
func TestPrepareClarifyInvocationCodeBuddyKeepsArgv(t *testing.T)
   // engine=clarifyEngineOf("default")，Opts{Bin:"codebuddy",Prompt:"p",RunID:"r",SessionID:"af1",
   //   Config:<临时 mcp.json>,Settings:<临时 settings.json>}
   // 断言 argv 含 --output-format stream-json、--session-id af1、--strict-mcp-config，
   // --disallowedTools 值含 Bash/Write/Edit，Translate == nil，Finish == nil
func TestPrepareClarifyInvocationClaudeUsesClaudeDialect(t *testing.T)
   // Config 指向一个含 CodeBuddy 专属 "tools" 键的 mcp.json fixture，Root=t.TempDir()
   // 断言 argv 含 --verbose 与 --mcp-config；session 参数为 assistantClaudeSessionID("af1")；
   // Root 下生成的 claude mcp 文件里不含 "defer_loading"
func TestPrepareClarifyInvocationCodexUsesAppServer(t *testing.T)
   // 断言 Command.Provider=="codex"、argv 含 "app-server"、Command.Stdin != nil、Translate != nil、
   // CredentialBindings 含 runtime.BindCodex
func TestPrepareClarifyInvocationAgentCLIWritesReadOnlyMCP(t *testing.T)
   // engine=clarifyEngineOf("opencode:x")，User={APIKey:"k",BaseURL:"https://example.com/v1"}
   // 断言 <Root>/home/.config/opencode/opencode.json 存在且内容含 agentflow_session 的 URL、
   // CredentialBindings 含 runtime.BindAgentCLI、Translate != nil
func TestPrepareClarifyInvocationRejectsUnknownEngine(t *testing.T)
func TestClarifySubmitOnlySupport(t *testing.T) // codebuddy/claude-code true；codex/opencode false
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/httpapi -run '^TestPrepareClarifyInvocation|^TestClarifySubmitOnlySupport$' -count=1`
Expected: 编译失败（`undefined: prepareClarifyInvocation`）。

- [ ] **Step 3: 实现分派 + `clarifyCommandOpts.Engine`**

`clarifyCommandOpts` 加 `Engine string`（默认空串等价 `codebuddy`，保证现有调用点与测试不变）；`prepareClarifyCommandWith` 里把硬编码的 `domain.ExecutorDefinition{Kind:"codebuddy", …}`（`clarify_session.go:231`）改为 `Kind: engine`，其中 `engine` 取 `opts.Engine` 为空时的 `"codebuddy"`。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/httpapi -run '^TestPrepareClarifyInvocation|^TestClarifySubmitOnlySupport$' -count=1`
Expected: PASS。

- [ ] **Step 5: 跑受影响的既有测试**

Run: `go test ./internal/httpapi -run 'Clarify|NativeClarify' -count=1`
Expected: PASS（`clarify_mcp_test.go:29/66/80`、`agent_tool_session_test.go:179` 的 CodeBuddy argv 断言必须仍然成立）。

- [ ] **Step 6: 提交**

```bash
git add internal/httpapi/clarify_engine.go internal/httpapi/clarify_session.go internal/httpapi/clarify_engine_command_test.go
git commit -m "feat(clarify): build the clarification command per engine"
```

---

### Task 3: 引擎感知的续跑与会话归属

**Files:**
- Modify: `internal/domain/issue.go:148-166`（`ClarificationAssessment`）
- Modify: `internal/httpapi/clarify_session.go:34-49`
- Modify: `internal/httpapi/stream.go:700`（`rememberClarifySession`）
- Test: `internal/httpapi/clarify_session_test.go`（改 + 加）

**Interfaces:**
- Consumes: `clarifyCLIHome(userID int64) string`（`clarify_session.go:28`）、`readCodexThread(root, sessionID string) string`（`assistant_engine_cli.go:267-275`）、`readAgentCLISession(root, kind, sessionID string) string`（`assistant_engine_agentcli.go:121-129`）、`assistantClaudeSessionID(id string) string`（`assistant_engine_cli.go:136-143`）、`repo.Store.PatchIssue`。
- Produces:
```go
domain.ClarificationAssessment.ModelID string `json:"modelId,omitempty"` // 记住这张单澄清用的引擎+模型

func canResumeClarifySession(homeDir, engineID, sessionID string, sessionUserID, userID int64) bool
func clarifySessionReusable(issue domain.Issue, engine clarifyEngine, homeDir string, userID int64) bool
func (h *streamHandlers) rememberClarifySession(issueID, sessionID string, userID int64, modelID string)
```
`canResumeClarifySession` 的归属检查保持不变（`sessionUserID != 0 && sessionUserID != userID` → false）；此后按 `engineID` 分派：`codebuddy` 沿用 `~/.codebuddy/projects/*/<sessionID>.jsonl` glob；`codex` → `readCodexThread(homeDir, sessionID) != ""`；agent CLI → `readAgentCLISession(homeDir, engineID, sessionID) != ""`；`claude-code` → `true`（UUID 由 `assistantClaudeSessionID` 稳定推导，会话缺失由 `executeClarify` 现有的重建历史分支兜底）；其它 → `false`。
`clarifySessionReusable` = `issue.Clarification.ModelID` 为空 **或** `clarifyEngineOf(issue.Clarification.ModelID).ID == engine.ID`，且 `canResumeClarifySession(homeDir, engine.ID, issue.Clarification.SessionID, issue.Clarification.SessionUserID, userID)`。

- [ ] **Step 1: 改 + 写测试**

改 `clarify_session_test.go:116` `TestCanResumeClarifySessionUsesResponsibleUserHome`：调用处补 `engineID`（用 `"codebuddy"`），断言不变。新增：

```go
func TestCanResumeClarifySessionPerEngine(t *testing.T)
   // homeDir=t.TempDir()；codex：写 codexThreadsPath(homeDir) 里 {"<sid>":"thread-1"} 后 true，删掉后 false
   // opencode：写 agentCLISessionsPath(homeDir) 里 {"opencode:<sid>":"native-1"} 后 true，否则 false
   // claude-code：无文件也 true；未知引擎 "foobar"：false
func TestClarifySessionReusableRejectsEngineSwitch(t *testing.T)
   // issue.Clarification{ModelID:"codex:gpt-5.5", SessionID:"af1"}，engine=codebuddy，
   // 且 homeDir 下确实存在 codebuddy 的 af1 transcript → 仍必须 false
func TestRememberClarifySessionStoresModelID(t *testing.T)
   // 走 repo.OpenTest() + PutIssue，调 h.rememberClarifySession(id,"af1",7,"codex:gpt-5.5")，
   // 断言存回的 Clarification.ModelID == "codex:gpt-5.5" 且 SessionUserID == 7
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/httpapi -run '^TestCanResumeClarifySession|^TestClarifySessionReusable|^TestRememberClarifySession' -count=1`
Expected: 编译失败（参数个数/字段不存在）。

- [ ] **Step 3: 实现**

按 Interfaces 修改三处；`rememberClarifySession` 的 `PatchIssue` 里同时写 `Clarification.ModelID`（`modelID` 为空串时不覆盖已有值）。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/httpapi -run 'ClarifySession|RememberClarifySession' -count=1`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/domain/issue.go internal/httpapi/clarify_session.go internal/httpapi/stream.go internal/httpapi/clarify_session_test.go
git commit -m "feat(clarify): pin a clarification session to the engine that started it"
```

---

### Task 4: `executeClarify` 接线

**Files:**
- Modify: `internal/httpapi/stream.go:383-745`（`executeClarify`、`runClarifyTurn`）
- Modify: `internal/httpapi/clarify_mcp.go:16-27`（删除 `clarifyAllowedModel`）
- Modify: `internal/httpapi/clarify_engine.go`（新增 `clarifyChoice`）
- Test: `internal/httpapi/clarify_session_test.go`、`internal/httpapi/clarify_engine_test.go`、`internal/httpapi/agent_tool_native_terminal_test.go`、`internal/httpapi/clarify_mcp_test.go`

**Interfaces:**
- Consumes: Task 1-3 的全部；`resolveAssistantEngineBin(engine string) (string, error)` + `assistantCLIMissingError`（`assistant_engine_cli.go:61-109`）；`assistantProviderCredentials(user domain.User) runtime.RunCredentials`（`assistant_provider.go:58-65`）；`runtime.ExecuteLocalCommand`；`captureClarifySessionID`、`clarifyStreamEvents`、`clarifySessionOutputError`。
- Produces:
```go
type clarifyTurn struct {
	WorkDir, Bin, Prompt, RunID, SessionID, HomeDir string
	Engine      clarifyEngine
	Resume      bool
	Credentials runtime.RunCredentials
	Translate   func(string) []string
	Opts        clarifyTurnOpts
}

func (h *streamHandlers) runClarifyTurn(ctx context.Context, turn clarifyTurn, emit func(event string, payload any)) (parsed any, raw string, capturedSessionID string, err error)
func (h *streamHandlers) clarifyChoice(request chatClarifyRequest, issue domain.Issue, responsible domain.User) (clarifyEngine, error)
```
`clarifyChoice` 优先级（**必须按此顺序，且只有第 1 项允许报错中断**）：
1. `request.Model` 非空 → `clarifyResolveModel(request.Model, responsible)`，出错直接返回错误（不退回）。
2. `issue.Clarification.ModelID` 非空 → `clarifyResolveModel`；出错则继续往下（记录日志），因为这是历史数据。
3. `responsible.ClarifyModel` 非空 → 同上，出错继续往下（这就是「默认失效回退 CodeBuddy」）。
4. 兜底 `clarifyEngineOf("")`（CodeBuddy + `default`）。

`executeClarify` 的改动点：`cbPath, cbErr := resolveCodeBuddyBin()`（`stream.go:454`）→ `engine, err := h.clarifyChoice(request, issue, responsible)` + `bin, err := resolveAssistantEngineBin(engine.ID)`；`modelID := clarifyAllowedModel(request.Model)`（`:647`）→ 用 `engine.ModelID`；凭据（`:648-651`）→ `credentials := assistantProviderCredentials(responsible)`，再 `credentials.GitToken = strings.TrimSpace(h.repo.GitCredential(ctx, responsible.ID))`；`canResumeClarifySession(homeDir, sessionID, …)` → `clarifySessionReusable(issue, engine, homeDir, responsible.ID)`；`emit("open", …)` 的 payload 加 `"engine": engine.ID`，`"model"` 改为 `engine.ModelID`；两处 `h.rememberClarifySession(issue.ID, sessionID, responsible.ID)` 补 `engine.ModelID`；重试循环改为构造 `clarifyTurn` 并调用新签名，`inv.Translate` 非 nil 时先翻译再喂 `captureClarifySessionID`/`clarifyStreamEvents`，并在每轮结束后调用 `inv.Finish(err == nil)`（非 nil 时）。

- [ ] **Step 1: 改既有测试到新签名**

`clarify_session_test.go:166` `TestRunClarifyTurnDetectsMissingSessionWithZeroExitCode`、`:181` `TestExecuteClarifyRebuildsSessionFromHistory`、`agent_tool_native_terminal_test.go:126` 三处调用改成 `clarifyTurn{…}` 形式（断言文本一字不改）；删除 `clarify_mcp_test.go:16` `TestClarifyAllowedModelFallsBackToCatalog`（其语义已由 Task 1 的测试覆盖，且「静默回退目录默认值」正是本次要移除的行为）。

- [ ] **Step 2: 写新测试**

```go
func TestExecuteClarifyRejectsEngineWithoutCredential(t *testing.T)
   // 责任人无 codex key，request.Model="codex:gpt-5.5" → 返回错误 == assistantEngineMissingKey("codex")，
   // 且不设置任何 AGENTFLOW_*_BIN（证明没有起进程）
func TestExecuteClarifyReportsMissingCLI(t *testing.T)
   // 责任人配了 opencode key+baseUrl；t.Setenv("PATH", t.TempDir())、t.Setenv("AGENTFLOW_OPENCODE_BIN","")
   // → 错误为 assistantCLIMissingError（errors.Is），文本含 "OpenCode"
func TestExecuteClarifyPrefersIssueEngine(t *testing.T)
   // issue.Clarification.ModelID="codex:gpt-5.5"，request.Model="" → 捕获的 open 事件里 engine=="codex"
   //（用 setClarifyExecForTest 之外的方式：直接调 executeClarify 并收集 emit）
func TestExecuteClarifyEngineSwitchStartsFreshSession(t *testing.T)
   // issue 存了 codex 的 session；request.Model 指向 codebuddy；fake codebuddy bin（沿用
   // TestExecuteClarifyRebuildsSessionFromHistory 的写法）记录 argv → 断言出现 --session-id，不出现 --resume
func TestClarifyChoiceFallsBackWhenDefaultIsStale(t *testing.T)
   // responsible.ClarifyModel="codex:gpt-5.5" 但无 codex key，request.Model="" → 返回 codebuddy/default，err nil
```

- [ ] **Step 3: 运行确认失败**

Run: `go test ./internal/httpapi -run 'Clarify' -count=1`
Expected: 编译失败 / 断言失败。

- [ ] **Step 4: 实现 `clarifyChoice`、`clarifyTurn`、`executeClarify` 改动，删除 `clarifyAllowedModel`**

删除 `clarify_mcp.go:25-27` 的 `clarifyAllowedModel`（`clarifyDefaultModel` 常量保留，仍被测试引用）。

- [ ] **Step 5: 运行确认通过**

Run: `go test ./internal/httpapi -run 'Clarify|NativeClarify' -count=1`
Expected: PASS。

- [ ] **Step 6: 全量后端测试 + 覆盖率**

Run: `make test && bash scripts/coverage-go.sh --min $(tr -d '[:space:]' < scripts/coverage-go.min)`
Expected: 全绿，覆盖率不低于门槛。

- [ ] **Step 7: 提交**

```bash
git add internal/httpapi/
git commit -m "feat(clarify): run requirement clarification on the configured engine"
```

---

### Task 5: 个人默认（domain / repo / handler / 迁移）

**Files:**
- Modify: `internal/domain/user.go`（`User` 加字段）
- Modify: `internal/repo/models.go:35-50`（`userConfigRow`）、`internal/repo/users.go`（`userWithConfig`、`userSelectColumns`、`toDomain`、`userHasConfig`、`writeUserConfig`、`UserCredentialUpdate`、`UpdateUserCredentials`）
- Modify: `internal/httpapi/users.go:26-64`（`usersHandlers.user`）
- Create: `migrations/028_clarify_model_preference.sql`
- Modify: `docs/proposals/clarify-multi-executor.md`（把「个人默认存 users 新列」订正为 `user_configs.clarify_model`）
- Test: `internal/repo/users_clarify_pref_test.go`（新建）、`internal/httpapi/users_test.go`（若不存在则新建 `users_clarify_pref_http_test.go`）

**Interfaces:**
- Consumes: `repo.OpenTest()`、`UsersRepo.UpdateUserCredentials(id int64, update UserCredentialUpdate) (domain.User, error)`、`domain.PublicUser`、Task 1 的 `clarifyResolveModel`/`errClarifyEngineUnknown`。
- Produces:
```go
domain.User.ClarifyModel string `json:"clarifyModel,omitempty"`          // 带引擎前缀的模型 ID，空 = 用 CodeBuddy
repo.UserCredentialUpdate.ClarifyModel *string `json:"clarifyModel"`  // nil = 不改，"" = 清除
userConfigRow.ClarifyModel string `gorm:"column:clarify_model;size:191"`
```
校验分层（**必须这样切**，否则 repo 会反向依赖 httpapi）：repo 只做 `strings.TrimSpace` 与持久化；引擎合法性在 `usersHandlers.user` 里用 `clarifyResolveModel(body.ClarifyModel, currentUser)` 校验，失败返回 400 且错误文本含原值。

- [ ] **Step 1: 写失败测试**

```go
// internal/repo/users_clarify_pref_test.go
func TestUserConfigPersistsClarifyModel(t *testing.T)
   // OpenTest + PutUser；UpdateUserCredentials(id, {ClarifyModel: ptr("codex:gpt-5.5")}) →
   //   User(id).ClarifyModel == "codex:gpt-5.5"
   // 再用 {ClarifyModel: nil} 更新其它字段 → 值保持；用 ptr("") → 清空
   // 新用户（此前无 user_configs 行）也能写入：验证 userHasConfig 已包含新字段
// internal/httpapi
func TestUserCredentialsAcceptsClarifyModel(t *testing.T)
   // PUT /api/users/{id}/credentials {"clarifyModel":"codex:gpt-5.5"}（该用户已配 codex key）→ 200，
   //   响应 JSON 的 clarifyModel 原样返回
func TestUserCredentialsRejectsUnknownClarifyEngine(t *testing.T)
   // {"clarifyModel":"foobar:x"} → 400，错误文本含 "foobar"
func TestUserCredentialsRejectsUnconfiguredClarifyEngine(t *testing.T)
   // {"clarifyModel":"codex:gpt-5.5"} 但用户没有 codex key → 400，文本为 assistantEngineMissingKey("codex")
func TestPublicUserKeepsClarifyModel(t *testing.T)
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/repo -run '^TestUserConfigPersistsClarifyModel$' -count=1 && go test ./internal/httpapi -run 'ClarifyModel' -count=1`
Expected: 编译失败（字段不存在）。

- [ ] **Step 3: 实现字段、repo 读写、handler 校验、迁移脚本**

`migrations/028_clarify_model_preference.sql` 按 `migrations/025_agent_cli_credentials.sql` 的写法（同样的 MySQL 方言与注释风格）给 `user_configs` 加 `clarify_model`。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/repo -run 'ClarifyModel' -count=1 && go test ./internal/httpapi -run 'ClarifyModel|PublicUser' -count=1`
Expected: PASS。

- [ ] **Step 5: MySQL 路径也跑一遍**

Run: `make test-mysql`
Expected: PASS（新列在 MySQL 下同样可写可读）。

- [ ] **Step 6: 提交**

```bash
git add internal/domain/user.go internal/repo/ internal/httpapi/users.go internal/httpapi/users_clarify_pref_http_test.go migrations/028_clarify_model_preference.sql docs/proposals/clarify-multi-executor.md
git commit -m "feat(users): store a personal default engine for requirement clarification"
```

---

### Task 6: 引擎可用性端点 `GET /api/clarify-engines`

**Files:**
- Modify: `internal/httpapi/clarify_engine.go`
- Modify: `internal/httpapi/server.go`（紧邻 `:401`）
- Modify: `docs/api/openapi.yaml`、`docs/api/README.md`（路由表）
- Test: `internal/httpapi/clarify_engine_availability_test.go`

**Interfaces:**
- Consumes: `assistantEngineOptions(user, sponsored)`、`assistantEngineLabel`、`assistantEngineCredentialField`、`assistantEngineCredential`、`assistantEngineProtocol`、`assistantKnownCatalog()`、`assistantEngineModels(ctx, user) []assistantModel`（`assistant_engines.go:302-342`）、`resolveAssistantEngineBin`、`assistantCLIMissingError`、`runtime.AgentCLINeedsBaseURL`、`runtime.AgentCLIModelOptional`（导出名以实际代码为准，见 `runtime/agent_cli.go:67-76`）、`repo.ResponsibleUser(issue)`、平台共享 CodeBuddy Key 的 sponsored 判定（沿用 `assistant_models.go:222-223` 的算法）。
- Produces:
```go
type clarifyEngineOption struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Configured bool             `json:"configured"`
	Available  bool             `json:"available"`
	Reason     string           `json:"reason,omitempty"`
	Credential string           `json:"credential"`
	Models     []assistantModel `json:"models,omitempty"`
}

type clarifyEngineCatalog struct {
	ResponsibleUserID   int64                 `json:"responsibleUserId"`
	ResponsibleUserName string                `json:"responsibleUserName"`
	Default             string                `json:"default"`
	DefaultStale        bool                  `json:"defaultStale,omitempty"`
	Engines             []clarifyEngineOption `json:"engines"`
}

func (h *streamHandlers) clarifyEngines(c *gin.Context) // GET /api/clarify-engines?issueId=&projectId=
func clarifyEngineAvailability(ctx context.Context, user domain.User, sponsored bool) []clarifyEngineOption
func clarifyEngineUnavailableReason(engine string, user domain.User, sponsored bool) string // "" = 可用
```
`clarifyEngineUnavailableReason` 的判定顺序与文案（**逐条固定**）：
1. 无 Key（且非 sponsored 的 CodeBuddy）→ `assistantEngineMissingKey(engine)`。
2. `runtime.AgentCLINeedsBaseURL(engine, protocol)` 且 Base URL 空 → `fmt.Sprintf("%s 需要在个人配置中填写 Base URL", assistantEngineLabel(engine))`。
3. `resolveAssistantEngineBin(engine)` 失败 → 该错误的 `Error()`（已含「服务器未安装 X CLI」）。
4. 该引擎模型列表为空且不允许省略模型 → `"该框架暂无可用模型，请先在个人配置里添加"`。
Handler 语义：需要登录；`issueId` 优先（找不到 → 404），否则 `projectId`（用 `ResponsibleUser(domain.Issue{ProjectID: id})` 解析责任人，项目不存在 → 404）；两者都缺 → 400。`Default` = `clarifyResolveModel(user.ClarifyModel, user)` 成功时的 `ModelID`，失败时 `assistantDefaultModel` 且 `DefaultStale: true`。响应体**绝不含任何 Key 或 Base URL 值**。

- [ ] **Step 1: 写失败测试**

```go
func TestClarifyEnginesUsesResponsibleUser(t *testing.T)
   // 调用者 A 配了 codex key，issue 的责任人 B 没配 → codex 项 configured=false、available=false、
   // reason == assistantEngineMissingKey("codex")；responsibleUserId == B.ID、responsibleUserName == B 的名字
func TestClarifyEnginesGreysUnavailableCLI(t *testing.T)
   // opencode 有 key+baseUrl，但 PATH 清空且 AGENTFLOW_OPENCODE_BIN 未设 → available=false，reason 含 "OpenCode"
func TestClarifyEnginesRequiresBaseURL(t *testing.T)
   // opencode 有 key 无 baseUrl → reason 含 "Base URL"
func TestClarifyEnginesStaleDefault(t *testing.T)
   // user.ClarifyModel="codex:gpt-5.5" 但 codex key 已清空 → defaultStale=true、default=="default"
func TestClarifyEnginesNeverLeaksCredentials(t *testing.T)
   // 响应体不含任何 key/baseUrl 明文（用 httptest 全量 body 断言）
func TestClarifyEnginesRequiresScope(t *testing.T) // 无 issueId/projectId → 400；不存在的 issueId → 404
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/httpapi -run '^TestClarifyEngines' -count=1`
Expected: 编译失败 / 404。

- [ ] **Step 3: 实现 handler + 注册路由 + openapi + 路由表**

`server.go` 在 `g.POST("/api/chat-clarify", api.stream.chatClarify)`（`:401`）旁边加 `g.GET("/api/clarify-engines", api.stream.clarifyEngines)`；`docs/api/openapi.yaml` 增加 path 与两个 schema；`docs/api/README.md` 的 Issues 路由表加一行 `| GET | /api/clarify-engines |`。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/httpapi -run '^TestClarifyEngines' -count=1 && go test ./internal/httpapi -run '^TestRouteInventoryExport$' -count=1`
Expected: PASS（路由清单测试会捕获未登记的 openapi 条目）。

- [ ] **Step 5: 提交**

```bash
git add internal/httpapi/clarify_engine.go internal/httpapi/clarify_engine_availability_test.go internal/httpapi/server.go docs/api/openapi.yaml docs/api/README.md
git commit -m "feat(clarify): expose per-engine availability for the responsible user"
```

---

### Task 7: 前端分组选择器

**Files:**
- Modify: `web/app/assistant-models.js`（新增 `renderGrouped` 并导出）
- Modify: `web/app/sync-schedule.js:100-151`
- Modify: `web/i18n-en.js`
- Modify: `web/syntax_test.go`
- Create: `web/clarify_engine_picker_test.js`、`web/clarify_engine_picker_test.go`

**Interfaces:**
- Consumes: `GET /api/clarify-engines`（Task 6 的 `clarifyEngineCatalog` JSON）、`window.AgentFlowAssistantModels.{DEFAULT,catalog,engineOf,load,paint,paintItem,renderOptions}`、现有 `selectedClarifyModel()`/`setClarifyModel(id)`/`clarifyModelPickerHTML(disabled)`/`bindClarifyModelPicker(host)`。
- Produces:
```js
// assistant-models.js —— groups: [{id,name,reason,disabled,models:[{id,name,family,credits}]}]
function renderGrouped(menu, groups, selectedId, onPick)   // 禁用组的每一项 disabled + title=reason，
                                                           // 组标题显示 name（disabled 时追加 reason）
window.AgentFlowAssistantModels.renderGrouped

// sync-schedule.js
async function clarifyEngineCatalog(scope)   // scope: {issueId} | {projectId}；按 scope 键缓存一次
function clarifyPickerGroups(catalog)        // clarifyEngineCatalog → renderGrouped 的 groups
function clarifyEffectiveModel(catalog, issue)
    // issue?.clarification?.modelId || selectedClarifyModel() || catalog?.default || 'default'
function clarifyResponsibleHint(catalog)     // 责任人不是当前用户时返回提示 HTML，否则 ''
function bindClarifyModelPicker(host, scope) // scope 可省略；省略时退化为今天的行为
```

- [ ] **Step 1: 写失败测试** `web/clarify_engine_picker_test.js`（vm 模式照抄 `web/assistant_models_test.js:45-122` 的 harness）

```js
// renderGrouped：三组（codebuddy 可用 / codex 可用 / claude-code disabled+reason）
//   断言：组标题数量 == 3；disabled 组内每个 button 都 disabled 且 title 含 reason；
//         选中项 class 含 is-active；点击可用项调用 onPick(id) 并关闭菜单；点击禁用项不调用 onPick
// clarifyPickerGroups：喂 Task 6 形状的 fixture，断言 available=false → disabled=true 且 reason 原样带出
// clarifyEffectiveModel：issue.clarification.modelId 优先 > localStorage > catalog.default > 'default'
// clarifyResponsibleHint：responsibleUserId != state.currentUser.id 时含责任人名，相同时返回 ''
```
Go wrapper `web/clarify_engine_picker_test.go` 照 `web/assistant_models_test.go:57-66` 的两参数形式（`appBundlePath(t)`, `"app/assistant-models.js"`）；需要 `sync-schedule.js` 时再加第三个参数或走 bundle。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./web -run '^TestClarifyEnginePicker$' -count=1`
Expected: FAIL（`renderGrouped is not a function`）。

- [ ] **Step 3: 实现 `renderGrouped` + 选择器改造 + i18n**

`bindClarifyModelPicker(host, scope)` 打开菜单时：有 scope → `await clarifyEngineCatalog(scope)` 再 `renderGrouped`；无 scope → 保持 `renderList`（不破坏 SOP/Bot/定时任务等其它调用方）。`web/i18n-en.js` 追加新中文串（至少：`将用责任人 {name} 的凭据`、`该框架暂无可用模型，请先在个人配置里添加`、`已失效，当前用 CodeBuddy`、`需求澄清 · 默认框架与模型`）。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./web -run '^TestClarifyEnginePicker$' -count=1 && go test ./web -run 'Syntax|AppBundle' -count=1`
Expected: PASS。

- [ ] **Step 5: 浏览器无关的前端全量**

Run: `npm --prefix webtest ci && npm --prefix webtest test`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add web/app/assistant-models.js web/app/sync-schedule.js web/i18n-en.js web/clarify_engine_picker_test.js web/clarify_engine_picker_test.go web/syntax_test.go
git commit -m "feat(web): group the clarification picker by engine and show why one is unavailable"
```

---

### Task 8: 两处澄清入口 + 设置页默认

**Files:**
- Modify: `web/index.html:928-936`
- Modify: `web/app/composer.js:671-678,835-870`
- Modify: `web/app/issue-drawer.js:222,400-427,560-596`
- Modify: `web/app/workbench.js:31-34,571-593`
- Modify: `web/syntax_test.go`
- Create: `web/clarify_engine_entries_test.js`、`web/clarify_engine_entries_test.go`、`webtest/tests/clarify-preference.test.mjs`

**Interfaces:**
- Consumes: Task 7 的 `clarifyEngineCatalog`/`clarifyEffectiveModel`/`clarifyResponsibleHint`/`bindClarifyModelPicker(host, scope)`；`saveCredential(key, values, options)`（`workbench.js:691-698`）与 `PUT /api/users/{id}/credentials`；`renderCredentialPanel()`（`workbench.js:608-612`）。
- Produces:
```js
function clarifyScopeForComposer()          // {projectId: currentProject()?.id}
function clarifyScopeForIssue(issue)        // {issueId: issue.id}
function clarifyPreferenceRowHTML(user, catalog)  // 「模型与执行」组顶部的偏好行
async function saveClarifyPreference(modelId)     // PUT {clarifyModel: modelId}，成功后 renderCredentialPanel()
```

- [ ] **Step 1: 写失败测试**

```js
// web/clarify_engine_entries_test.js（fetch 桩照抄 web/clarification_composer_test.js:16-98）
//   sendChatClarify：body.model == clarifyEffectiveModel(catalog, null)；URL 仍是 /api/chat-clarify
//   sendIssueClarification：body.model == issue.clarification.modelId（当它存在时）
//   paintRequirementComposerChrome：给 #reqClarifyModelPicker 绑定的是 {projectId} scope
//   issue-drawer 渲染：责任人不等于当前用户时，澄清页出现 clarifyResponsibleHint 的文本
// webtest/tests/clarify-preference.test.mjs（jsdom，loadApp 后打开个人配置）
//   「模型与执行」组顶部出现偏好行；选一个模型后 PUT /api/users/{id}/credentials 的 body 是
//   {"clarifyModel":"codex:gpt-5.5"}；catalog.defaultStale=true 时行内出现「已失效，当前用 CodeBuddy」
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./web -run '^TestClarifyEngineEntries$' -count=1 && npm --prefix webtest test`
Expected: FAIL。

- [ ] **Step 3: 实现**

`web/index.html` 的 `#reqClarifyModelPicker` 加 `data-clarify-scope="project"`；`composer.js` 与 `issue-drawer.js` 按 scope 绑定并改用 `clarifyEffectiveModel`；`workbench.js` 在 `credentialGroupHTML` 里当 `group.id === 'model'` 时在凭据行之前插入 `clarifyPreferenceRowHTML`，保存走既有 credentials PUT。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./web -count=1 && npm --prefix webtest test`
Expected: PASS（含 `syntax_test.go` 新增断言：bundle 含 `clarifyScopeForIssue`、`saveClarifyPreference`、`data-clarify-scope`、`/api/clarify-engines`）。

- [ ] **Step 5: 提交**

```bash
git add web/
git commit -m "feat(web): pick the clarification engine at both entries and in personal settings"
```

---

### Task 9: 文档、CHANGELOG 与全量校验

**Files:**
- Modify: `README.md:103`、`README.zh-CN.md:103`、`CHANGELOG.md`（顶部新增 `## 0.3.7 — …`，末尾一行写「Existing installations may apply `migrations/028_clarify_model_preference.sql`; the platform also adds these tables and columns on startup.」）、`docs/api/collaboration.md:146-169`（补 `model` 字段与 `GET /api/clarify-engines`）、`design.md`（澄清选择器规格，参考 `:370` 运行方式选择器的写法）、`docs/proposals/clarify-multi-executor.md`（状态改为已实现 + 填验证记录）

- [ ] **Step 1: 改 README 两处**

把「需求澄清和通知 AI 汇总目前使用 CodeBuddy」改成：需求澄清可在已配置的框架中选择（默认 CodeBuddy，可在个人配置里改默认）；通知 AI 汇总仍使用 CodeBuddy。英文版同步。

- [ ] **Step 2: 写 CHANGELOG 新章节 + 更新 API 文档与 design.md**

CHANGELOG 用现有体例（`## 0.3.7 — <一句话摘要>` + 要点列表 + 迁移说明行）。`docs/api/collaboration.md` 的 `POST /api/chat-clarify` 请求示例补 `model`，并新增 `GET /api/clarify-engines` 小节（说明按需求责任人判定可用性）。

- [ ] **Step 3: 全量校验**

Run: `make test && bash scripts/coverage-go.sh --min $(tr -d '[:space:]' < scripts/coverage-go.min) && npm --prefix webtest ci && npm --prefix webtest run coverage && python3 scripts/check-public.py && bash scripts/scan-secrets.sh && go vet ./...`
Expected: 全部通过。

- [ ] **Step 4: 提交**

```bash
git add README.md README.zh-CN.md CHANGELOG.md docs/ 
git commit -m "docs: describe engine selection for requirement clarification"
```

---

### Task 10: 真机验证（需要凭据，人工执行）

设计文档的验收标准要求 Codex、Claude Code 与一个 agent CLI 真机跑通；这一步需要真实 Key 与已安装的 CLI，无法在单元测试里替代。

- [ ] **Step 1: 准备环境**

一台控制面机器上装好 `codex`、`claude`（或允许首次 npm 安装）、`opencode`（或 `cursor-agent`），并在个人配置里填好三者的 Key（opencode 需 Base URL）。

- [ ] **Step 2: 按引擎跑一轮完整澄清**

对每个引擎：新建需求 → 选择器里选该引擎的模型 → 开始澄清 → 回答一轮 → 继续澄清（验证续跑）→ 确认结论。逐条核对设计文档「验收标准」：argv 用的是对应二进制（看进程或日志）、思考流正常滚动、结论通过 `submit_clarification` 落地、第二轮走续跑而不是重放。

- [ ] **Step 3: 验证跨引擎与责任人语义**

同一张单从 Codex 显式切到 CodeBuddy：确认历史与已答问题保留、旧 session 未被复用。再用一个「责任人没配该引擎 Key」的账号打开同一张单：确认菜单里该引擎置灰并显示原因。

- [ ] **Step 4: 记录结果**

把每个引擎的验证结论（通过 / 未验证 / 失败原因）写进 `docs/proposals/clarify-multi-executor.md` 末尾的「验证记录」小节，未真机验证的引擎在 CHANGELOG 里如实标注。

- [ ] **Step 5: 提交**

```bash
git add docs/proposals/clarify-multi-executor.md CHANGELOG.md
git commit -m "docs: record the clarification engine verification matrix"
```

---

## Self-Review 记录

- **Spec 覆盖**：目标 1（全引擎）→ T1/T2/T4；目标 2（两层选择）→ T5/T7/T8；目标 3（两处入口）→ T8；目标 4（分组 + 置灰原因）→ T6/T7；目标 5（去掉静默改写）→ T1/T4；目标 6（CodeBuddy 行为不变）→ T2 Step 5、T4 Step 1 保留既有断言。非目标（无 effort 控件、不动通知汇总与定时任务、不按项目、不派 runner、不新增引擎）在计划中没有对应任务，符合预期。反目标 1/2/3 分别由 Review Focus 5、1、2 的测试钉住。约束里的迁移脚本在 T5，AGENTS.md 领域归属在 T5/T6 的 Files 里落实。
- **对 spec 的一处订正**：spec 写「个人默认存 users 新列（gorm AutoMigrate 补列）」，实际个人偏好存在 `user_configs` 侧表（先例 `CodeBuddyInternetEnvironment`），T5 Step 3 要求同步订正 spec 文本。
- **对 spec 的一处收窄**：spec 的置灰原因枚举包含「dsh 缺少 MCP 扩展依赖」。该依赖（`@deepseek-ai/dsh-mcp-client`）没有可靠的静态探测点，T6 的 `clarifyEngineUnavailableReason` 只做 Key / Base URL / CLI 二进制 / 模型目录四项检查；dsh 缺扩展时在运行期报错，错误文本必须点名该 npm 包（T2 的 agent CLI 分支沿用 `runtime` 现有错误）。
- **类型一致性**：`clarifyEngine`（T1）→ T2/T3/T4 一致使用；`clarifyInvocationOpts`/`clarifyInvocation`（T2）→ T4 消费；`clarifyTurn`（T4）替代旧的 13 参数 `runClarifyTurn`；`clarifyEngineCatalog`/`clarifyEngineOption`（T6）的 JSON 字段名与 T7 的 `clarifyPickerGroups` 消费字段逐字对应；`ClarifyModel`（T5，Go）↔ `clarifyModel`（JSON/前端）↔ `clarify_model`（列名）。
- **比例**：计划以签名、断言与命令为主，未粘贴实现体。

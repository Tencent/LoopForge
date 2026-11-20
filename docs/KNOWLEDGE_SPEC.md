# LoopForge 知识库接入与 MCP 能力工程规范（Spec）

> **文档定位**：本 Spec 面向后续接手研发的 AI Agent 与工程师，作为架构评审、增量开发与跨仓对齐的唯一工程基线。
> **事实约束**：所有表名、路由、结构体、环境变量、错误码与前端挂载状态均核实自 `LoopForge-knowledge-mcp/` 与 `LoopForge/` 当前源码；`WeKnora/` 仅作只读参考，严禁修改。

---

## 1. 背景与跨仓基线状态

当前工作区涉及两个可写研发仓库，后续 Agent 动手前必须先确认目标仓库：

1. **`LoopForge-knowledge-mcp/`（完整参考实现仓）**：
   - 已实现双引擎上游（`lightcloud` MCP + `weknora` REST）、多连接命名空间（`<connID>::<rawID>`）、无状态只读 MCP 代理（`/runners/knowledge/mcp`）、细粒度 `KnowledgeRef` 绑定（SOP Agent 节点 & Chatbot 会话）、L0 目录注入（`<bound_knowledge_bases>`）、5 态证据引用闭环（`[[knowledge:<id>]]`）、知识沉淀审核队列，以及完整的原生控制台（`web/app/knowledge.js`、`web/app/knowledge-refs.js`）与 Chatbot UI。
2. **`LoopForge/`（主平台仓）**：
   - 当前仅包含 `lightcloud` 单连接后端基线、`migrations/002_knowledge_base.sql`、角色级 `AgentDefinition.KnowledgeBase` 开关（`web/app/flows.js`、`web/app/agents.js`）及预留的 CSS/i18n 令牌；**尚未挂载** `#knowledgeView`、`knowledge.js`、`knowledge-refs.js`、WeKnora 引擎、`KnowledgeRef` 与 Chatbot 知识选择器。

---

## 2. 核心设计目标与硬性边界（Guardrails）

### 2.1 设计目标
- **多引擎统一抽象**：通过 `internal/knowledge.Upstream` 接口统一屏蔽 `lightcloud`（Streamable HTTP MCP）与 `weknora`（`/api/v1/*` REST）差异。
- **零凭据下发**：上游 Token / `X-API-Key` 永不离开服务端（不返回前端、不注入 Agent 环境变量/MCP Header），Agent 运行时仅持有 48 小时有效期的项目作用域代理令牌 `afkb1.`。
- **只读与写回分离**：Agent 运行时 MCP 仅暴露只读检索/读取工具；任何新知识写回必须经过「`knowledge.md` 产物 -> `knowledge_submissions` 人工审核 -> 管理员显式开启 `writeEnabled` 后发布」。
- **作用域最小权限**：每次运行时检索强制取「项目授权范围（`SourcesForProject`） ∩ 节点/会话 `KnowledgeRef.Sources` 白名单」的交集。

### 2.2 架构红线（后续 Agent 严禁违反）
1. **严禁向 Agent 暴露写工具**：`POST /runners/knowledge/mcp` 的 `tools/list` 与 `tools/call` 必须严格校验白名单（LightCloud 4 工具 / WeKnora 4 工具），`kb_write_document` 绝不可对 Agent 放行。
2. **禁止在 `Store` 新增领域方法**：所有知识库持久化归属 `internal/repo/knowledge.go`（`KnowledgeRepo`），领域 HTTP 处理归属 `internal/httpapi/knowledge.go`、`knowledge_proxy.go`、`knowledge_reference.go`。
3. **禁止代码内自动迁移或 Seed**：`repo.Open()` 仅保留 GORM `AutoMigrate` 增量 DDL；任何数据清洗、删列或预置脚本必须以手工 SQL 形式放入 `migrations/`（序号递增，当前主仓最新为 `038`，新脚本从 `039_` 起）。
4. **KISS / YAGNI**：单一实现禁止抽多余 Interface 或无意义 Wrapper；优先复用 `go.mod` 与 `chatbot/package.json` 现有依赖。
5. **前端规范**：新增中文 UI 文案必须同步补齐 `web/i18n-en.js`；复用 `web/app/harness-ui.css` 中的 `--hui-*` 与 `.kb-*` 令牌；若在 `web/` 根目录新增静态文件，必须同步更新 `web/embed.go`。

---

## 3. 核心数据契约（Data Contracts）

### 3.1 数据库表结构（`migrations/002_knowledge_base.sql`）
- **`knowledge_settings`**：`id VARCHAR(64) PK`, `payload JSON`, `token TEXT`, `updated_at DATETIME(3)`
  - `id = "global"`：存储 `KnowledgeSettings` JSON（`payload` 不含明文 Token），主连接 Token 存于 `token` 列。
  - `id = "conn_token_<connID>"`：多连接模式下各连接的独立 Token 隔离存储行。
  - `id = "weknora_admin_password"`：内置 WeKnora 自动生成的管理员密码。
- **`knowledge_submissions`**：`id VARCHAR(64) PK`, `project_id`, `run_id`, `artifact_id`, `status` (`pending`|`published`|`rejected`), `payload JSON`, `created_at`, `updated_at`（带 4 个单列索引）。

### 3.2 多连接命名空间编码
- 当 `len(settings.Connections) > 0` 且 `connID != "default"` 时，来源 ID 与文档 ID 统一编码为 `<connID>::<rawID>`（`EncodeScopedSourceID` / `DecodeScopedSourceID`）。
- 仅当处于纯 legacy 单连接模式（`len(settings.Connections) == 0`）时允许裸 ID。

### 3.3 细粒度绑定与引用契约（`internal/domain/knowledge_ref.go`）
```go
type KnowledgeRef struct {
    ServerID string   `json:"mcpServerId"`
    Mode     string   `json:"mode"`              // "all" | "allowlist"
    Sources  []string `json:"sources,omitempty"` // mode=="all" 时必须为空；"allowlist" 时至少 1 项
}
```
- **校验不变量**：
  - `ValidateWorkflowKnowledge`：仅 `node.Type == "agent"` 可配置 `KnowledgeRefs`；`mcpServerId != "agentflow-knowledge"` 时暂不允许按 `sources` 白名单过滤。
- **5 态 `KnowledgeRead` 与引用闭环**：
  - 状态枚举：`命中`、`无匹配`、`部分完成`、`不可用`、`未知结构`。
  - `MarkKnowledgeReadCited`：节点执行结束后扫描输出与产物中的 `[[knowledge:<id>]]` 标记（支持证据级 `Evidence.ID` 与调用级 `Read.ID`），仅当 `IsKnowledgeCitableStatus(status)` 为真时回填 `Cited = true`。

### 3.4 无状态代理令牌与 URL 重写契约
- **令牌格式**：`afkb1.<base64url(ProxyClaims)>.<base64url(HMAC-SHA256)>`，默认 TTL 48 小时（`internal/knowledge/token.go`）。
- **签名密钥优先级**：`AGENTFLOW_SESSION_SECRET` -> `SESSION_SECRET` -> 上游连接密钥派生。
- **Runner URL 重写**：`internal/runtime/platform_mcp.go` 读取 `AGENTFLOW_PLATFORM_MCP_URL`，将相对路径 `/runners/knowledge/mcp` 重写为绝对 URL；若云端/沙箱运行未配置基准地址，则剥离该 MCP 并输出告警。

---

## 4. 接口现状与待补齐缺口（Gap Matrix）

后续 Agent 在制定迭代计划时，需重点关注以下**已存在但未完全打通**的接口与功能缺口：

| 能力项 | 后端接口（`-knowledge-mcp`） | 前端 UI 挂载（`-knowledge-mcp`） | 主仓（`LoopForge/`）现状 |
|---|---|---|---|
| 配置管理、连通探测、WeKnora 一键接入、连接删除 | 已实现（`/api/knowledge/{settings,check,weknora/provision,connections/:id}`） | 已挂载（`web/app/knowledge.js` `settings` Tab） | 仅 `settings` / `check` 后端，无 UI |
| 来源列表、WeKnora 文档分页浏览、原文预览、试检索 | 已实现（`/api/knowledge/{sources,sources/:id/documents,document,search}`） | 已挂载（`sources` & `search` Tab） | 仅 `sources`/`document`/`search` 后端，无 UI |
| 知识沉淀候选扫描、审核队列、编辑/驳回/发布 | 已实现（`/api/knowledge/{candidates,submissions,submissions/:id/...}`） | 已挂载（`distill` Tab） | 仅后端，无 UI |
| **WeKnora 知识库与文档 CRUD / 上传 / 重解析** | **已实现**（`GET /api/knowledge/models`、`POST/PUT/DELETE /api/knowledge/sources(/:id)`、`POST .../upload\|url\|manual`、`PUT/DELETE/reparse /api/knowledge/documents/:docId`） | **未挂载**（当前控制台仅提供「管理此知识库 ↗」外链跳往 WeKnora UI `18081`） | 未实现 |
| **删除未发布沉淀条目** | **已实现**（`DELETE /api/knowledge/submissions/:id`） | **未挂载**（`knowledge.js` 无删除按钮） | 仅后端，无 UI |
| **SOP 节点 & Chatbot `KnowledgeRef` 绑定与引用展示** | 已实现（`/api/knowledge/reference-{options,check}`、`PUT /api/assistant/sessions/:id/knowledge`） | 已挂载（`knowledge-refs.js`、`flows.js`、`runs.js`、`chatbot/src/`） | 未实现 |

> **典型后续开发任务建议**：
> 1. 若目标是将 `-knowledge-mcp` 能力合入 `LoopForge/` 主仓：按「DB/Domain -> Knowledge Service & Proxy -> Engine/Runtime -> Native Console -> Chatbot」分阶段迁移，每阶段独立提交并通过定向测试。
> 2. 若目标是在 `-knowledge-mcp` 内消除对外部 WeKnora UI（18081）的跳转依赖：直接在 `web/app/knowledge.js` 对接上述已就绪的后端 CRUD / 上传 / 重解析接口，无需改动后端表结构。

---

## 5. 错误码映射契约（`internal/apperr` -> HTTP）

所有 `internal/knowledge` 业务错误必须通过 `apperr.New(kind, msg)` 返回，由 `internal/httpapi/gin_helpers.go` 统一映射：
- `apperr.Invalid` -> `400 Bad Request`（如 URL 非 `http(s)`、未启用任何有效连接、`.md` 路径不合法、`KnowledgeRef` 校验失败）
- `apperr.Forbidden` -> `403 Forbidden`（如来源不在项目授权范围、未开启 `writeEnabled` 尝试发布）
- `apperr.NotFound` -> `404 Not Found`（如审核单不存在）
- `apperr.Conflict` -> `409 Conflict`（如已发布条目禁止再编辑/删除、原连接已移除）
- `apperr.Unavailable` -> `503 Service Unavailable`（如知识中心未配置或未启用）
- `apperr.Upstream` -> `502 Bad Gateway`（如上游 LightCloud/WeKnora 超时或返回错误、容器环境未挂载 docker CLI）

---

## 6. 审阅与验收门禁（Acceptance Criteria）

后续 Agent 提交代码前，必须在目标仓库目录（`LoopForge-knowledge-mcp/` 或 `LoopForge/`）执行并通过以下定向验证（禁止无上限全量刷屏）：

1. **后端定向测试**：
   ```bash
   go test -count=1 ./internal/knowledge ./internal/httpapi -run TestKnowledge | tail -n 60
   go test -count=1 ./internal/engine -run "TestResolve.*Knowledge|TestKnowledge|TestMarkKnowledge" | tail -n 60
   ```
2. **前端与嵌入式资源检查**：
   ```bash
   go test -count=1 ./web | tail -n 60
   bash scripts/build-chatbot.sh | tail -n 60
   ```
3. **容器化集成验证（涉及部署或 WeKnora 联调时）**：
   ```bash
   ./start.sh dev --with-weknora
   docker compose -f compose.yaml -f compose.weknora.yaml logs --tail 80 platform weknora-app
   ```
4. **安全与规范核对**：
   - `GET /api/knowledge/settings` 响应中 `token` 与 `connections[].token` 必须为空，仅返回 `tokenConfigured`。
   - `/runners/knowledge/mcp` 的 `tools/list` 不得包含任何写操作工具。
   - 新增前端中文文案已在 `web/i18n-en.js` 同步英文映射。

---

## 7. 扩展其他外部知识库服务规范（腾讯文档、飞书、自建 RAG 等）

后续如需支持除 `lightcloud` 与 `weknora` 之外的第三方知识库（如**腾讯文档、飞书知识库/云文档、Dify、RAGFlow** 等），按以下两条标准路径选型与实施：

### 7.1 路径一：通用 MCP 服务零代码接入（目标服务已提供 MCP）
- **适用场景**：外部服务已提供标准 HTTP / SSE / Stdio MCP Server，且无需平台侧统一托管 Token 或按库白名单过滤。
- **接入方式**：
  1. 在控制台「Agent 能力 -> 能力资产 -> 自定义 MCP」（`POST /api/mcp-servers`）注册该服务；
  2. 在 Chatbot `.knowledge-picker` 或 SOP Agent 节点「关联知识库」（`#nodeKnowledgeRefs`）中勾选绑定（由 `GET /api/knowledge/reference-options` 自动聚合展示）。
- **源码边界**：`internal/domain/knowledge_ref.go` 当前限制非 `agentflow-knowledge` 的外部 MCP 仅支持 `mode: "all"`，不支持平台侧按 `sources` 白名单二次过滤。

### 7.2 路径二：原生 `Upstream` Provider 接入（推荐用于腾讯文档、飞书等企业 SaaS / 私有 API）
当外部服务仅提供 REST/OpenAPI（无标准 MCP），且需要纳入「知识中心」多连接聚合（`<connID>::<rawID>`）、`afkb1.` 只读代理、项目级授权白名单及 `knowledge.md` 审核写回时，按以下 5 步最小 Diff 扩展：

1. **注册 Provider 枚举**（`internal/domain/knowledge.go`、`internal/knowledge/service.go`）：
   - 新增常量（如 `KnowledgeProviderFeishu = "feishu"`、`KnowledgeProviderTencentDocs = "tencent_docs"`）；
   - 更新 `KnowledgeConnection.EffectiveProvider()`、`KnowledgeSettings.EffectiveProvider()` 与 `normalizeProvider()`。
2. **实现 `Upstream` 客户端**（新增 `internal/knowledge/<provider>.go`，对照 `internal/knowledge/weknora.go`）：
   - 实现 `Tools`、`ListSources`（映射为知识空间/文件夹列表 `[]Source`）、`Search`（映射为 `SearchResult`）、`Fetch`（读取文档正文/分块）、`WriteDocument`（仅用于 `Service.Publish` 审核后写回；若上游仅只读可返回 `apperr.New(apperr.Invalid, ...)`）。
3. **接入工厂与配置校验**（`internal/knowledge/client.go`、`internal/knowledge/service.go`）：
   - 在 `NewUpstream(settings)` 增加 Provider 分发分支；
   - 在 `UpdateSettings` 补充对应服务地址/参数校验规则。
4. **适配只读 MCP 代理**（`internal/httpapi/knowledge_proxy.go`）：
   - 在 `tools/list` 与 `tools/call` 中为新 Provider 暴露只读工具定义，并强制经过 `SourceAllowed`（项目范围 ∩ `KnowledgeRef.Sources`）校验；**严禁暴露任何写操作工具给 Agent**。
5. **挂载前端配置入口**（`web/app/knowledge.js`、`web/i18n-en.js`）：
   - 在 `#kbCfgProvider` 下拉选项增加新引擎，按需适配 `sources` Tab 文档列表预览，并同步补齐 `web/i18n-en.js`。

### 7.3 针对腾讯文档 / 飞书等 OAuth 授权类服务的额外约束
- **Token 自动刷新**：当前 `knowledge_settings` 仅存静态 `token` 列。若接入飞书（`tenant_access_token`）或腾讯文档 OAuth2 需要 `app_id` / `app_secret` / `refresh_token`，应将密钥隔离保存在 `knowledge_settings` 独立行（参照 `conn_token_<connID>`），并在 `KnowledgeSettings.Public()` 中彻底脱敏。
- **用户级权限隔离（可选演进）**：若客户要求按当前登录用户身份检索飞书/腾讯文档（而非平台共享应用权限），应利用 `ProxyClaims.UserID` 在 `knowledge_proxy.go` 执行阶段查用户授权凭据（`UsersRepo`）换取上游访问令牌，**严禁将用户 OAuth Token 写入 `afkb1.` 令牌或下发给 Agent 运行时容器**。


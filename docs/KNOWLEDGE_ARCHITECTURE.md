# LoopForge 知识库与 MCP 架构说明（源码盘点版）

> 本文档严格以当前仓库真实源码（Go 后端、SQL Migrations、原生控制台与 Chatbot 前端、Docker Compose 配置）为唯一事实来源编写。
> 同步标注了 `LoopForge-knowledge-mcp/`（完整实现仓）与 `LoopForge/`（主平台仓）之间的现状差异；未扫描 `WeKnora/` 参考仓。

---

## 1. 仓库定位与跨仓现状差异

| 维度 | `LoopForge-knowledge-mcp/`（完整实现仓） | `LoopForge/`（主平台仓） |
|---|---|---|
| 上游引擎支持 | `lightcloud`（MCP）+ `weknora`（REST `/api/v1/*` + 内置/外部一键接入） | 仅 `lightcloud` 单连接基线 |
| 多连接命名空间 | 支持 `connections[]` 多实例聚合与 `<connID>::<rawID>` 作用域编码 | 仅单连接 `url` + `token` |
| 细粒度绑定（`KnowledgeRef`） | 支持在 Chatbot 会话与 SOP Agent 节点上按服务/来源白名单绑定 | 未实现 |
| 原生控制台 UI | 已挂载「知识中心」(`#knowledgeView`)、SOP 节点 `#nodeKnowledgeRefs`、运行遥测引用展示 | 仅角色编辑器有 `knowledgeBase` 开关，未挂载 `#knowledgeView` |
| Chatbot 对话 UI | 已挂载 `.knowledge-picker` 与 `.knowledge-reads` 引用折叠面板 | 未挂载 |

---

## 2. 真实模块拓扑与核心组件职责

```text
+-------------------------------------------------------------------------+
|                           前端交互层 (web/ & chatbot/)                   |
|  - web/app/knowledge.js        : 知识中心 4 Tab（文档/沉淀/配置/调试）   |
|  - web/app/knowledge-refs.js   : 通用知识库范围选择器 (editKnowledgeRefs)|
|  - web/app/flows.js            : 角色 knowledgeBase 开关 & 节点绑定按钮  |
|  - web/app/runs.js             : 运行遥测 knowledge.call 与引用标记渲染  |
|  - chatbot/src/{App,useChat}   : 会话知识库选择与消息 knowledgeReads 展示|
+------------------------------------+------------------------------------+
                                     | HTTP /api/knowledge/* & /api/assistant/*
                                     v
+-------------------------------------------------------------------------+
|                        HTTP 接入与代理层 (internal/httpapi/)              |
|  - server.go                   : 路由装配与认证中间件                    |
|  - knowledge.go                : 配置、探测、文档浏览、沉淀审核 CRUD     |
|  - knowledge_reference.go      : KnowledgeRef 可用选项与连通性校验       |
|  - knowledge_proxy.go          : /runners/knowledge/mcp 只读 MCP 代理    |
+------------------------------------+------------------------------------+
                                     |
          +--------------------------+--------------------------+
          v                                                     v
+--------------------------------------+   +-------------------------------+
|    领域与业务层 (internal/knowledge/) |   |   引擎与运行时注入层           |
|  - service.go        : 核心业务逻辑   |   |  - internal/engine/assets.go  |
|  - client.go         : LightCloud MCP |   |  - internal/engine/node_execute.go |
|  - weknora.go        : WeKnora REST   |   |  - internal/runtime/platform_mcp.go |
|  - weknora_provision : 容器编排/多连接|   |  - internal/domain/knowledge*.go |
|  - token.go          : afkb1 代理令牌 |   +-------------------------------+
|  - rules.go          : 规范与路径归一化|
+--------------------------------------+
          |
          v
+-------------------------------------------------------------------------+
|                        持久化层 (internal/repo/ & migrations/)           |
|  - migrations/002_knowledge_base.sql : knowledge_settings / submissions |
|  - internal/repo/knowledge.go        : 凭据隔离存储、多连接 Token 行      |
+-------------------------------------------------------------------------+
```

### 关键源码路径清单

| 层级 | 源码路径 | 核心职责 |
|---|---|---|
| **Domain** | `internal/domain/knowledge.go` | 定义常量（`KnowledgeMCPServerID = "agentflow-knowledge"`、`KnowledgeUsageRuleID = "agentflow-knowledge-usage"`、`KnowledgeArtifactBasename = "knowledge.md"`）、引擎枚举（`lightcloud` / `weknora`）、`KnowledgeSettings`、`KnowledgeConnection`、`KnowledgeSubmission`、`KnowledgeCandidate`。 |
| **Domain** | `internal/domain/knowledge_ref.go` | 定义 `KnowledgeRef`（`mcpServerId` / `mode` [`all`\|`allowlist`] / `sources`）、`ValidateKnowledgeRefs`、`ValidateWorkflowKnowledge`、`SOPKnowledgeUsageRuleID`、Prompt 策略常量（`KnowledgeLookupInstruction`、`SOPKnowledgeLookupInstruction`）、5 态 `KnowledgeRead` 与 `MarkKnowledgeReadCited`（识别 `[[knowledge:<id>]]`）。 |
| **Knowledge** | `internal/knowledge/service.go` | `Service` 核心：配置校验、连通性检查、跨连接 `Sources` 聚合、受限检索与文档读取、WeKnora 文档/知识库管理、`Usage` 角色统计、知识沉淀队列（`Submit`/`Edit`/`Delete`/`Reject`/`Publish`）。 |
| **Knowledge** | `internal/knowledge/client.go` | LightCloud 上游 Streamable HTTP MCP 客户端（`mcpclient.Client` 按 `url\x00token` 缓存），封装 `kb_search`、`kb_fetch`、`kb_list_sources`、`kb_get_source`、`kb_write_document`。 |
| **Knowledge** | `internal/knowledge/weknora.go` | WeKnora REST 客户端（`/api/v1/*`，`X-API-Key` 认证），实现 `Upstream` 接口并将 REST 映射为 4 个只读 MCP 工具：`list_knowledge_bases`、`search_knowledge`、`list_documents`、`read_document`。 |
| **Knowledge** | `internal/knowledge/weknora_provision.go` | `ProvisionWeKnora`（`managed` 本地拉起 Docker 容器或 `external` 接入已有实例）、`BootstrapWeKnoraAPIKey`、`DeleteConnection`、`EncodeScopedSourceID` / `DecodeScopedSourceID`（`<connID>::<rawID>`）。 |
| **Knowledge** | `internal/knowledge/token.go` | 无状态代理令牌 `afkb1.<payload>.<sig>`（HMAC-SHA256，48 小时 TTL），签名密钥优先取 `AGENTFLOW_SESSION_SECRET` / `SESSION_SECRET`，未配置时退化为上游连接密钥派生。 |
| **Knowledge** | `internal/knowledge/rules.go` | `ProxyPath = "/runners/knowledge/mcp"`、`UsageRule()`、`MCPServerDefinition()`、`ParseDistilled()`（解析 `knowledge.md` front matter）、`NormalizeDocPath()`。 |
| **Engine** | `internal/engine/assets.go` | `resolveAssets`（角色级 `agent.KnowledgeBase`）与 `resolveNodeAssets`（SOP 节点级 `node.KnowledgeRefs`），注入只读 MCP 定义与规则，调用 `BuildKnowledgeCatalog` 注入 L0 `<bound_knowledge_bases>` 目录。 |
| **Engine** | `internal/engine/node_execute.go` | 执行完成后调用 `markKnowledgeReadsCited`，扫描节点输出与产物中的 `[[knowledge:<id>]]` 并回填 `Cited` 标记。 |
| **Runtime** | `internal/runtime/platform_mcp.go` | `PrepareMCPServers` 将相对路径 `/runners/knowledge/mcp` 结合 `AGENTFLOW_PLATFORM_MCP_URL` 重写为绝对 URL；若云端/沙箱运行无可用基准地址则剥离并给出告警。 |
| **HTTP API** | `internal/httpapi/knowledge.go` | `/api/knowledge/*` 管理与检索端点，基于 `apperr.Kind` 映射标准 HTTP 状态码。 |
| **HTTP API** | `internal/httpapi/knowledge_proxy.go` | `/runners/knowledge/mcp` JSON-RPC 2.0 MCP 代理端点（`initialize`、`tools/list`、`tools/call`），仅放行只读工具并强制校验项目/来源白名单。 |
| **HTTP API** | `internal/httpapi/knowledge_reference.go` | `/api/knowledge/reference-options` 与 `/api/knowledge/reference-check`，供 Chatbot 与 SOP 节点选择器使用。 |
| **Repo** | `internal/repo/knowledge.go` | `KnowledgeRepo`：读写 `knowledge_settings` 与 `knowledge_submissions`，按独立行隔离保存多连接 Token 与内置 WeKnora 管理员密码。 |

---

## 3. 端到端核心数据流

### 3.1 上游接入配置与多连接命名空间

```text
管理员前端 (knowledge.js)
  ├─ 方式 A: PUT /api/knowledge/settings (LightCloud 或单连接配置)
  └─ 方式 B: POST /api/knowledge/weknora/provision (mode: "managed" | "external")
       ├─ managed  -> ensureManagedWeKnoraContainers() -> BootstrapWeKnoraAPIKey()
       └─ external -> 直接使用 API Key 或账号密码换取 full_access API Key
            │
            v
     knowledge_settings 表
       ├─ id = "global"              : payload (含 connections 元数据，不含 Token), token (主连接)
       ├─ id = "conn_token_<connID>" : 各额外连接的独立 Token（不随 JSON 返回前端）
       └─ id = "weknora_admin_password": 自动生成的内置 WeKnora 管理员密码
```

- **凭据隔离**：`KnowledgeSettings.Public()` 在返回任何 HTTP 响应前清空 `Token` 与 `Connections[i].Token`，仅保留 `tokenConfigured: true/false`。
- **多连接路由**：当存在多个连接时，所有来源 ID 与文档 ID 均编码为 `<connID>::<rawID>`；`DecodeScopedSourceID` 据此将请求精确路由到对应连接实例。

### 3.2 运行时注入与只读 MCP 代理链路

1. **资产解析与令牌签发**（`internal/engine/assets.go`）：
   - **角色级开关**：当 `AgentDefinition.KnowledgeBase == true` 且知识库就绪时，注入 `id = "agentflow-knowledge"` 的 HTTP MCP 定义（URL 为 `<baseURL>/runners/knowledge/mcp`，Header 为 `Authorization: Bearer afkb1.<claims>.<sig>`）以及内置规则 `agentflow-knowledge-usage`。
   - **SOP 节点级覆盖**：当节点配置了 `KnowledgeRefs` 时，若包含 `agentflow-knowledge`，则覆盖角色级默认绑定；对非内置知识 MCP 服务，注入 ID 记为 `agentflow-knowledge-<mcpServerId>`。同时调用 `BuildKnowledgeCatalog` 生成 `<bound_knowledge_bases>` L0 目录附加到 `agentflow-node-knowledge-usage` 规则中。
2. **远程 Runner URL 重写**（`internal/runtime/platform_mcp.go`）：
   - `RebasePlatformMCPServers` / `PrepareMCPServers` 检查 `AGENTFLOW_PLATFORM_MCP_URL`，将 `/runners/knowledge/mcp` 补全为可被远程容器/CLI Runner 访问的完整 URL。
3. **代理执行与安全边界**（`internal/httpapi/knowledge_proxy.go`）：
   - 请求到达 `POST /runners/knowledge/mcp`（属于 `/runners` 机器面，免浏览器登录态）。
   - 验证 `afkb1.` HMAC-SHA256 令牌有效性与 48h 过期时间，提取 `ProxyClaims`（`ProjectID`、`AgentID`、`RunID`、`NodeID`、`Attempt`、`Ref`）。
   - `tools/list` 仅返回只读工具集合（LightCloud 为 `kb_search` / `kb_fetch` / `kb_list_sources` / `kb_get_source`；WeKnora 或混合模式为 `list_knowledge_bases` / `search_knowledge` / `list_documents` / `read_document`）。**`kb_write_document` 永不暴露给 Agent**。
   - `tools/call` 强制与项目授权范围（`Settings.SourcesForProject(projectID)`）及 `Ref.Sources` 白名单取交集后转发上游。

### 3.3 知识沉淀与审核写回闭环

```text
Agent 运行结束产出 workspace/knowledge.md
  │
  ▼ GET /api/knowledge/candidates (扫描 Run 产物中的 knowledge.md)
人工在「知识中心 -> 知识沉淀」点击「发起审核」
  │
  ▼ POST /api/knowledge/submissions (ParseDistilled 解析 title/source/path)
knowledge_submissions (status = "pending")
  │
  ├─ 驳回: POST /api/knowledge/submissions/:id/reject -> status = "rejected"
  └─ 发布: POST /api/knowledge/submissions/:id/publish
       ├─ 校验 settings.WriteEnabled == true 且来源在项目授权范围内
       ├─ LightCloud 校验 NormalizeDocPath (必须为 .md 相对路径)
       └─ 调用上游 WriteDocument -> status = "published", 回填 commit 与 publishedAt
```

---

## 4. 数据库表结构与迁移现状

### 4.1 迁移规范与现状

- 基线建表脚本位于 `migrations/002_knowledge_base.sql`（`LoopForge/` 与 `LoopForge-knowledge-mcp/` 均有该文件）。
- 遵循仓库约定，生产代码不做自动数据迁移/seed；`internal/repo/repo.go` 仅通过 GORM `AutoMigrate` 补齐缺失表/列/索引，运维可通过手动执行 `migrations/` SQL 预建。
- 多连接 Token 与 `KnowledgeRef` 未新增额外物理表：前者复用 `knowledge_settings` 表的独立主键行，后者序列化在 `workflows.payload`（节点 `knowledgeRefs`）与 `assistant_sessions`（会话 `knowledgeRefs`）中。

### 4.2 真实表结构（`migrations/002_knowledge_base.sql`）

1. **`knowledge_settings`**
   - `id` `VARCHAR(64) NOT NULL PRIMARY KEY`（主配置固定为 `"global"`；多连接 Token 为 `"conn_token_<connID>"`；内置 WeKnora 密码为 `"weknora_admin_password"`）
   - `payload` `JSON NULL`（存储 `KnowledgeSettings` 结构，不含敏感凭据）
   - `token` `TEXT NULL`（主连接或对应行的明文凭据，独立成列）
   - `updated_at` `DATETIME(3) NULL`

2. **`knowledge_submissions`**
   - `id` `VARCHAR(64) NOT NULL PRIMARY KEY`
   - `project_id` `VARCHAR(64) NULL`（索引 `idx_knowledge_submissions_project`）
   - `run_id` `VARCHAR(64) NULL`（索引 `idx_knowledge_submissions_run`）
   - `artifact_id` `VARCHAR(64) NULL`（索引 `idx_knowledge_submissions_artifact`）
   - `status` `VARCHAR(32) NULL`（`pending` | `published` | `rejected`，索引 `idx_knowledge_submissions_status`）
   - `payload` `JSON NULL`（存储 `KnowledgeSubmission` 完整元数据、正文、审核意见与错误信息）
   - `created_at` `DATETIME(3) NULL`
   - `updated_at` `DATETIME(3) NULL`

---

## 5. 对外 HTTP 与 MCP 接口清单

### 5.1 平台 HTTP API（`internal/httpapi/server.go`）

| 方法与路径 | 说明 | 权限 / 来源文件 |
|---|---|---|
| `GET /api/knowledge/settings` | 获取脱敏配置（含 `ready`、`canManage`、`weKnoraUiUrl`） | 登录用户 / `knowledge.go` |
| `PUT /api/knowledge/settings` | 更新知识库连接、写回开关与项目来源白名单 | 管理员 / `knowledge.go` |
| `POST /api/knowledge/check` | 使用已保存或表单临时参数探测上游连通性 | 管理员 / `knowledge.go` |
| `POST /api/knowledge/weknora/provision` | 一键接入内置（`managed`）或外部（`external`）WeKnora | 管理员 / `knowledge.go` |
| `DELETE /api/knowledge/connections/:id` | 移除指定连接及其独立 Token 行 | 管理员 / `knowledge.go` |
| `POST /api/knowledge/reference-check` | 校验 `KnowledgeRef[]` 格式与对应服务可用性 | 登录用户 / `knowledge_reference.go` |
| `GET /api/knowledge/reference-options` | 获取可选知识 MCP 服务（含 `sources=true` 展开来源列表） | 登录用户 / `knowledge_reference.go` |
| `GET /api/knowledge/models` | 获取 WeKnora 可用 Embedding / QA 模型列表 | 登录用户 / `knowledge.go` |
| `GET /api/knowledge/sources` | 聚合所有活跃连接的知识库来源（标注 `allowed`） | 登录用户 / `knowledge.go` |
| `POST/PUT/DELETE /api/knowledge/sources(/:id)` | WeKnora 知识库创建、更新、删除（仅后端，控制台未挂载） | 管理员 / `knowledge.go` |
| `GET /api/knowledge/sources/:id/documents` | 分页/关键词查询 WeKnora 知识库下文档列表 | 登录用户 / `knowledge.go` |
| `POST /api/knowledge/sources/:id/{upload,url,manual}` | WeKnora 文档上传/URL 抓取/手工录入（仅后端，控制台未挂载） | 管理员 / `knowledge.go` |
| `PUT/DELETE /api/knowledge/documents/:docId`、`POST .../reparse` | WeKnora 文档更新、删除、重解析（仅后端，控制台未挂载） | 管理员 / `knowledge.go` |
| `POST /api/knowledge/search` | 受项目作用域限制的试检索（`排障调试` Tab） | 登录用户 / `knowledge.go` |
| `GET /api/knowledge/document` | 按 `source` + `path`（及可选行号范围）读取原文分块/行 | 登录用户 / `knowledge.go` |
| `GET /api/knowledge/usage` | 查询启用知识库的角色数、列表与近期审计活动 | 登录用户 / `knowledge.go` |
| `GET /api/knowledge/candidates` | 扫描 `knowledge.md` 产物候选列表 | 登录用户 / `knowledge.go` |
| `GET/POST /api/knowledge/submissions` | 查询或创建知识沉淀审核单 | 登录用户 / `knowledge.go` |
| `GET/PUT/DELETE /api/knowledge/submissions/:id` | 查看、编辑、删除未发布的沉淀审核单 | 管理员 / `knowledge.go` |
| `POST /api/knowledge/submissions/:id/{publish,reject}` | 审核通过写回上游，或驳回并记录审核意见 | 管理员 / `knowledge.go` |
| `PUT /api/assistant/sessions/:id/knowledge` | 保存 Chatbot 会话绑定的 `knowledgeRefs` | 会话所属用户 / `assistant.go` |

### 5.2 运行时 MCP 代理接口（`POST /runners/knowledge/mcp`）

由 `internal/httpapi/knowledge_proxy.go` 处理 JSON-RPC 2.0 请求，支持方法 `initialize`、`notifications/initialized`、`tools/list`、`tools/call`：

- **LightCloud 模式只读工具**（`internal/knowledge/client.go`）：
  - `kb_list_sources`：列出当前项目已授权的知识库来源。
  - `kb_search`：在已授权来源中执行检索并返回引用片段。
  - `kb_fetch`：按 `source` + `path` + 行号范围读取原文。
  - `kb_get_source`：获取单个来源的元信息。
- **WeKnora / 多连接混合模式只读工具**（`internal/knowledge/weknora.go`）：
  - `list_knowledge_bases`：列出已授权知识库 ID、名称、简介与文档数。
  - `search_knowledge`：混合检索（参数 `query`、`knowledge_base_ids`、`limit`）。
  - `list_documents`：列出指定知识库内的文档（参数 `knowledge_base_id`、`keyword`、`page`、`page_size`）。
  - `read_document`：按分块顺序读取文档正文（参数 `knowledge_id`、`source`、`offset`、`limit`、`query`）。

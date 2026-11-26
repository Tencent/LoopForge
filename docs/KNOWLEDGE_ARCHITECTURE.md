# LoopForge 知识库连接器与 MCP 架构说明

> 本文档严格以当前仓库真实源码（Go 后端、SQL Migrations、原生控制台与 Chatbot 前端）为唯一事实来源编写。

---

## 1. 架构概览

LoopForge 采用**用户级知识库连接器（Knowledge Connector）**架构，统一支持 6 类内置连接器类型：
- **四大原生只读 REST 连接器**（`domain.IsNativeRESTConnectorType`，统一暴露 4 个只读工具：`list_knowledge_bases`、`search_knowledge`、`list_documents`、`read_document`）：
  - `obsidian`：对接 Obsidian Local REST API
  - `notion`：对接 Notion API（`2026-03-11`，Block 转 Markdown）
  - `siyuan`：对接思源笔记本地内核（`:6806`）
  - `feishu`：对接飞书企业自建应用（Wiki 知识空间与云文档，支持 `tenant_access_token` 自动缓存与续期）
- **`weknora` 连接器**：对接外部或本地部署的 WeKnora 实例（`/api/v1/*`，`X-API-Key` 认证，同样映射为上述 4 个标准只读工具）
- **`mcp` 通用连接器**：对接任意遵循 Streamable HTTP MCP 协议的知识库服务（支持 `none` / `bearer` / `headers` / `oauth` 鉴权）

---

## 2. 模块拓扑与核心源码路径

| 层级 | 源码路径 | 核心职责 |
|---|---|---|
| **Domain** | `internal/domain/knowledge.go`、`internal/domain/knowledge_ref.go` | 定义连接器类型枚举（`obsidian`/`notion`/`siyuan`/`feishu`/`weknora`/`mcp`）、`KnowledgeConnector`、`KnowledgeRef`（`connectorId` / `mode` / `sources`）、5 态 `KnowledgeRead` 与引用标记 `[[knowledge:<id>]]` 解析。 |
| **Knowledge 底座** | `internal/knowledge/service.go`、`service_oauth.go`、`crypto.go` | 用户级连接器 CRUD、连通性探测（`CheckConnector`）、AES-GCM 凭据加密、OAuth 2.0（PKCE / DCR / State 校验 / 自动刷新）与用量统计。 |
| **原生与 MCP 连接器** | `internal/knowledge/connector_{common,obsidian,notion,siyuan,feishu,weknora,mcp}.go` | 统一 SSRF 防护拨号器、4 工具标准契约适配、MCP Streamable HTTP 客户端池（按 `OwnerUserID + ConnectorID` 隔离）、写工具识别与限流重试。 |
| **执行器与证据回填** | `internal/knowledge/executor.go`、`tool_adapter.go`、`evidence.go` | `ExecuteReferenceToolWithHistory`、`BuildCatalogXML`（`<bound_knowledge_bases>` L0 目录）、四级证据回填链 `enrichReadEvidences`。 |
| **Engine & Runtime** | `internal/engine/assets.go`、`node_execute.go`、`internal/runtime/platform_mcp.go` | 角色级 `KnowledgeBase` 开关与 SOP 节点级 `KnowledgeRefs` 解析，签发 `afkb1.` 短期只读代理令牌，节点完成后执行 `MarkKnowledgeReadCited` 与 `UsageCounter.MarkCited` 落库。 |
| **HTTP API** | `internal/httpapi/knowledge.go`、`knowledge_oauth.go`、`knowledge_proxy.go`、`agent_tool_mcp.go` | `/api/knowledge/*` 连接器与用量接口、OAuth 授权与回调（含 `knowledge_oauth_state` Cookie 防 CSRF）、`/runners/knowledge/mcp` 严格只读 MCP 代理、对话助手写工具人工审批门控。 |
| **Repo & Migrations** | `internal/repo/knowledge.go`、`knowledge_usage.go`、`migrations/047~049_*.sql` | `knowledge_connectors`、`knowledge_connector_usage_counts`、`knowledge_document_usage_counts`、`knowledge_usage_events` 持久化。 |
| **Web & Chatbot** | `web/app/knowledge.js`、`web/app/knowledge-refs.js`、`chatbot/src/KnowledgeSources.jsx` | 原生控制台「我的知识库」管理页、知识范围选择器弹窗、对话引用角标 `[N]` 与折叠来源面板。 |

---

## 3. 核心安全边界

1. **凭据零下发与加密存储**：连接器 `secret_ciphertext` 与 `oauth_token_ciphertext` 经 AES-GCM 加密入库，HTTP 响应永远只返回布尔标记（`secretConfigured` / `oauthConnected`）；变更 `endpoint` 主机/协议/端口时强制清空已存旧凭据。
2. **多租户严格隔离**：所有连接器、MCP 客户端缓存、工具缓存、并发信号量与用量统计均以 `owner_user_id` 隔离；绑定了个人知识库的会话对其他项目成员及非成员均不共享个人连接器上下文。
3. **SSRF 与重定向防护**：所有连接器与 `mcpclient` 均通过统一 SSRF 防护拨号器校验解析后的目标 IP（拦截链路本地/云元数据 `169.254.0.0/16`、阿里云元数据/CGNAT `100.64.0.0/10` 等），并限制跨源重定向以防凭据泄漏。
4. **SOP 代理严格只读**：`POST /runners/knowledge/mcp` 仅暴露只读工具并校验活跃的 `RunID + NodeID + Attempt`；在 AI 工作台对话中，MCP 写工具默认拦截并触发人工审批。

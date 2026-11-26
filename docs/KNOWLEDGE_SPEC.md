# LoopForge 知识库连接器与 MCP 工程规范（Spec）

> **文档定位**：本规范定义 LoopForge 个人与团队知识库连接器（Knowledge Connectors）、运行时只读 MCP 代理及证据引用闭环的工程契约。

---

## 1. 核心设计目标与架构红线

1. **统一连接器模型**：所有知识源均抽象为用户级 `KnowledgeConnector`（归属 `owner_user_id`），支持 `obsidian`、`notion`、`siyuan`、`feishu`、`weknora`、`mcp` 六类内置连接器。
2. **零凭据下发**：上游 API Key、Bearer Token 与 OAuth 令牌加密存储于服务端，绝不下发至前端或 Agent 运行时容器；SOP Agent 运行时仅持有绑定到 `RunID + NodeID + Attempt` 的短期 HMAC 令牌 `afkb1.`。
3. **只读代理与写工具审批**：
   - SOP 运行时代理端点 `POST /runners/knowledge/mcp` 严格只读，任何可能修改外部状态的写工具均返回 `未开放只读工具` 错误。
   - AI 工作台对话（`AgentToolAssistant`）调用 MCP 写工具时，必须经过 `approveKnowledgeWrite` 原生人工审批；非对话模式下遇到写工具一律 Fail-Closed 拒绝。
4. **领域隔离**：持久化逻辑归属 `internal/repo/knowledge.go` 与 `internal/repo/knowledge_usage.go`，禁止在 `repo.Store` 上挂载知识库领域方法；HTTP 路由归属 `internal/httpapi/knowledge*.go`。

---

## 2. 数据契约

### 2.1 数据库表（`migrations/047~049`）
- **`knowledge_connectors`**：存储用户连接器定义、加密凭据（`secret_ciphertext`）、OAuth 状态（`oauth_token_ciphertext`）、来源范围（`source_scope_json`）与允许工具列表（`allowed_tools_json`）。
- **`knowledge_connector_usage_counts` / `knowledge_document_usage_counts`**：按 `(owner_user_id, connector_id[, document_id])` 聚合检索调用次数、命中次数与最终被回答/产物引用的次数（`cited_count`）。
- **`knowledge_usage_events`**：记录近期检索与读取审计明细（含场景 `chat`/`sop`、工具名、查询词、命中文档与引用状态）。

### 2.2 引用绑定契约（`internal/domain/knowledge_ref.go`）
```go
type KnowledgeRef struct {
    ConnectorID string   `json:"connectorId"`
    Mode        string   `json:"mode"`              // "all" | "allowlist"
    Sources     []string `json:"sources,omitempty"` // mode=="all" 时为空；"allowlist" 时至少 1 项
}
```

---

## 3. 错误码映射契约（`internal/apperr` -> HTTP）

- `apperr.Invalid` -> `400 Bad Request`
- `apperr.Forbidden` -> `403 Forbidden`
- `apperr.NotFound` -> `404 Not Found`
- `apperr.Conflict` -> `409 Conflict`
- `apperr.Unavailable` -> `503 Service Unavailable`
- `apperr.Upstream` -> `502 Bad Gateway`

---

## 4. 验收测试命令

```bash
go vet ./internal/mcpclient ./internal/knowledge ./internal/httpapi ./internal/engine ./internal/repo ./web
go test -count=1 ./internal/mcpclient ./internal/knowledge ./internal/httpapi ./internal/engine ./internal/repo ./docs/api ./internal/mcpserver ./web
npm --prefix chatbot test -- --run
npm --prefix webtest test
```

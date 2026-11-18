# LoopForge HTTP API

本文档以 `internal/httpapi/server.go`、`dispatch.go`、`schedule.go` 的当前实现为准。接口按领域拆分：

- [协作与需求](collaboration.md)：项目、仓库、Issue、评论、澄清和任务
- [能力注册表](registry.md)：Agent、规则、Skill、MCP、执行器、环境与沙箱配置
- [编排与运行](orchestration.md)：SOP、运行、审批、日志、制品、分析和定时任务
- [共享 AI 定时任务与订阅](shared-schedules.md)：发布版本、业务参数、独立执行与手动升级
- [外部同步](sync.md)：工蜂/TAPD 同步

## 基础约定

- 基础 URL 示例：`http://127.0.0.1:8090`。
- 除下载和 SSE 外，请求与响应均为 UTF-8 JSON。写请求使用 `Content-Type: application/json`。
- JSON 请求体上限为 2 MiB。
- 时间使用 RFC 3339，例如 `2026-09-02T12:00:00Z`。
- 服务可能对普通响应启用 gzip；客户端可发送 `Accept-Encoding: gzip`。SSE、OTLP 和显式请求 `text/event-stream` 的响应不压缩。
- 列表接口除运行日志/遥测外均为完整数组，没有通用分页。
- 浏览器跨域仅允许与 API 相同的 host；异源请求返回 `403`。预检 `OPTIONS` 返回 `204`。

### 认证

生产环境设置 `AGENTFLOW_AUTH_REQUIRED=true` 后：

- Studio 使用 `/login.html` 密码登录后签发的 HttpOnly Cookie；`POST /api/auth/password` 需要同源 Origin 和 JSON `{login,password}`。
- 登录用户在「接入方式 → 我的 Token」申请个人 Token；REST API 与 MCP 使用
  `Authorization: Bearer afp_<token>`。
- Token 明文只展示一次，支持有效期、最近使用时间和即时撤销。
- `GET/POST /api/auth/tokens`、`DELETE /api/auth/tokens/{id}` 只接受登录 Cookie，
  不允许 Bearer Token 自行创建更多 Token。
- 缺失、过期或已撤销的凭据返回 `401`，并带
  `WWW-Authenticate: Bearer realm="AgentFlow"`。
- `-basic-user` / `-basic-pass` 是兼容旧部署的外围 Basic Auth。
- 文档示例统一使用环境变量，避免把凭据写入历史：

```bash
export AGENTFLOW_URL=http://127.0.0.1:8090
export AGENTFLOW_TOKEN='afp_xxx'
curl -H "Authorization: Bearer $AGENTFLOW_TOKEN" "$AGENTFLOW_URL/api/health"
```

### 通用响应和状态码

错误通常为：

```json
{"error":"错误说明"}
```

- `200 OK`：读取、更新、动作成功；部分写接口返回 `{"ok":true}`。
- `201 Created`：创建运行、派发任务或克隆 SOP 成功。
- `204 No Content`：删除 SOP 或 CORS 预检成功。
- `400 Bad Request`：JSON 无效、字段缺失、引用无效或业务参数错误。
- `401 Unauthorized`：Basic Auth 失败。
- `403 Forbidden`：异源请求被拒绝或当前实例禁用执行。
- `404 Not Found`：资源或子路径不存在。
- `405 Method Not Allowed`：路径存在但方法不支持。
- `409 Conflict`：版本冲突、状态不允许、重复派发、运行中资源阻止删除或调度器关闭。
- `500 Internal Server Error`：流式输出不受支持或澄清执行器失败。
- `504 Gateway Timeout`：AI 澄清超过 10 分钟。

注意：底层存储/引擎的大多数错误当前映射为 `400`，不是 `500`。部分 `PUT` 注册接口要求 `id`，缺失时当前实现可能返回空的 `200`；调用方仍应将 `id` 视为必填。

## API 索引

### 通用

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/health` | 健康检查，返回 `{"status":"ok","role":"api"}` |
| `GET` | `/api/capabilities` | 列出公共 HTTP/MCP 操作清单 |
| `GET` | `/api/bootstrap` | 一次获取 Studio 初始化目录和存储信息 |
| `POST`, `DELETE` | `/mcp` | 无状态 MCP Streamable HTTP 入口 |

### 协作与需求

| 方法 | 路径 |
|---|---|
| `GET`, `POST`, `PUT` | `/api/projects` |
| `GET` | `/api/projects/{id}` |
| `GET`, `POST`, `PUT`, `DELETE` | `/api/repositories` |
| `GET` | `/api/repositories/{id}` |
| `GET`, `POST`, `PUT` | `/api/issues` |
| `GET` | `/api/issues/{issueId}` |
| `POST` | `/api/issues/{issueId}/comments` |
| `GET` | `/api/issues/{issueId}/runs` |
| `POST` | `/api/issues/{issueId}/chat-clarify` |
| `POST` | `/api/issues/{issueId}/reopen` |
| `POST` | `/api/issues/{issueId}/dispatch` |
| `POST` | `/api/chat-clarify` |
| `POST` | `/api/chat-clarify/cancel` |
| `GET` | `/api/tasks` |
| `GET` | `/api/tasks/{id}` |

### 能力注册表

| 方法 | 路径 |
|---|---|
| `GET`, `PUT` | `/api/agents` |
| `GET` | `/api/agents/{id}` |
| `DELETE` | `/api/agents/{id}` |
| `GET` | `/api/agents/{id}/revisions` |
| `GET` | `/api/agents/{id}/revisions/{revisionId}` |
| `POST` | `/api/agents/{id}/revisions/{revisionId}/restore` |
| `GET`, `PUT`, `DELETE` | `/api/rules` |
| `GET`, `PUT`, `DELETE` | `/api/skills` |
| `GET`, `PUT` | `/api/mcp-servers` |
| `POST` | `/api/mcp-servers/import` |
| `GET` | `/api/executors` |
| `GET`, `PUT`, `DELETE` | `/api/dockerfiles` |
| `GET`, `PUT`, `DELETE` | `/api/environments` |
| `GET`, `PUT`, `DELETE` | `/api/sandboxes` |

### 编排、运行与观测

| 方法 | 路径 |
|---|---|
| `GET`, `PUT` | `/api/workflows` |
| `GET`, `DELETE` | `/api/workflows/{id}` |
| `GET` | `/api/workflows/{id}/revisions` |
| `GET` | `/api/workflows/{id}/revisions/{revisionId}` |
| `POST` | `/api/workflows/{id}/revisions/{revisionId}/restore` |
| `POST` | `/api/workflows/{id}/publish` |
| `POST` | `/api/workflows/{id}/clone` |
| `GET` | `/api/workflow-metrics` |
| `GET` | `/api/analytics` |
| `GET` | `/api/observability/spec` |
| `GET`, `POST` | `/api/runs` |
| `GET`, `DELETE` | `/api/runs/{runId}` |
| `GET` | `/api/runs/{runId}/stream` |
| `GET` | `/api/runs/{runId}/logs` |
| `GET` | `/api/runs/{runId}/logs.txt` |
| `GET` | `/api/runs/{runId}/telemetry` |
| `GET` | `/api/runs/{runId}/artifacts/{artifactId}` |
| `POST` | `/api/runs/{runId}/approve` |
| `POST` | `/api/runs/{runId}/pass` |
| `POST` | `/api/runs/{runId}/reject` |
| `POST` | `/api/runs/{runId}/retry` |
| `GET`, `POST` | `/api/runs/{runId}/workspace/restore` |
| `POST` | `/api/runs/{runId}/decline-retry` |
| `POST` | `/api/runs/{runId}/cancel` |
| `GET`, `POST`, `PUT` | `/api/schedule-jobs` |
| `DELETE` | `/api/schedule-jobs/{id}` |
| `POST` | `/api/schedule-jobs/{id}/run` |
| `POST` | `/api/schedule-jobs/{id}/enabled` |
| `GET` | `/api/schedule-job-executions` |

### AI 对话（助手）

`/api/assistant/*` 是 Studio 与嵌入式聊天的接口，也是本文件里体量最大的一个域。登录 Cookie 与 `Authorization: Bearer afp_<token>` 均可使用，异源请求返回 `403`；除附件与诊断下载外，请求与响应都是 UTF-8 JSON。

| 方法 | 路径 |
|---|---|
| `POST` | `/api/assistant/chat` |
| `POST` | `/api/assistant/generations` |
| `GET` | `/api/assistant/sessions/{id}/generation` |
| `GET` | `/api/assistant/sessions/{id}/generations/{generationId}/diagnostics` |
| `POST` | `/api/assistant/sessions/{id}/generation/cancel` |
| `POST` | `/api/assistant/sessions/{id}/generation/steer` |
| `POST` | `/api/assistant/sessions/{id}/proposals` |
| `GET` | `/api/assistant/preferences` |
| `PUT` | `/api/assistant/preferences` |
| `GET` | `/api/assistant/models` |
| `POST` | `/api/assistant/cli-install` |
| `GET` | `/api/assistant/cli-install` |
| `POST` | `/api/assistant/attachments` |
| `GET` | `/api/assistant/attachments/{id}` |
| `GET` | `/api/assistant/references` |
| `GET` | `/api/assistant/sessions` |
| `POST` | `/api/assistant/sessions/{id}/read` |
| `GET` | `/api/assistant/sessions/{id}` |
| `PUT` | `/api/assistant/sessions/{id}` |
| `DELETE` | `/api/assistant/sessions/{id}` |
| `POST` | `/api/assistant/sessions/{id}/fork` |
| `POST` | `/api/assistant/sessions/{id}/archive` |
| `POST` | `/api/assistant/sessions/{id}/edit` |
| `PUT` | `/api/assistant/sessions/{id}/issue` |
| `PUT` | `/api/assistant/sessions/{id}/mode` |
| `PUT` | `/api/assistant/sessions/{id}/sandbox` |
| `GET` | `/api/assistant/sessions/{id}/native-approvals` |
| `POST` | `/api/assistant/sessions/{id}/native-approvals/{approvalId}` |
| `GET` | `/api/assistant/tasks` |
| `PUT` | `/api/assistant/tasks` |
| `DELETE` | `/api/assistant/tasks/{id}` |
| `POST` | `/api/assistant/tasks/{id}/run` |

### 共享 AI 定时任务

| 方法 | 路径 |
|---|---|
| `GET` | `/api/shared-schedule-tasks` |
| `GET` | `/api/shared-schedule-tasks/{id}/versions` |

订阅、发布版本与业务参数的语义见[共享 AI 定时任务与订阅](shared-schedules.md)。

### 同步

| 方法 | 路径 |
|---|---|
| `GET`, `POST`, `PUT` | `/api/sync/config` |
| `POST` | `/api/sync/now` |
| `GET` | `/api/sync/logs` |
| `GET` | `/api/sync/status` |
| `POST` | `/api/sync/create` |

## 公共能力发现与 MCP

### `GET /api/capabilities`

返回 `200`：

```json
{
  "operations":[
    {"name":"projects_list","description":"List projects with secrets redacted","method":"GET","path":"/api/projects","readOnly":true}
  ],
  "mcp":{"streamableHttp":"/mcp","stdioCommand":"agentflow-mcp"}
}
```

`operations` 是同时供 HTTP 和 MCP 使用的能力清单。它明确排除了 signals、OTLP、lease 和沙箱维护。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" \
  "$AGENTFLOW_URL/api/capabilities"
```

### `POST /mcp`

无状态 JSON-RPC 2.0 MCP 入口，请求体最大 4 MiB，支持协议版本 `2024-11-05`、`2025-03-26`、`2025-06-18`，其他版本回落到 `2025-06-18`。支持：

- `initialize`、`ping`
- `tools/list`、`tools/call`
- `resources/list`、`resources/templates/list`、`resources/read`
- `notifications/*`（返回 HTTP `202`，无 JSON-RPC 响应）

`tools/list` 由 `/api/capabilities` 的 operation manifest 生成。`tools/call` 参数格式为：

```json
{"name":"issues_dispatch","arguments":{"path":{"id":"issue-1"},"query":{},"body":{"rerun":true}}}
```

底层 HTTP 2xx/非 2xx 分别映射为 tool result 的 `isError:false/true`；JSON 响应还放在 `structuredContent.data`，原 HTTP 状态在 `structuredContent.status`。制品和日志还可通过 `agentflow://runs/{runId}/artifacts/{artifactId}`、`agentflow://runs/{runId}/logs` 资源模板读取。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X POST \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' \
  "$AGENTFLOW_URL/mcp"
```

JSON-RPC 有响应时 HTTP 为 `200`，JSON 解析失败为 `400`；`GET /mcp` 和其他不支持的方法为 `405`。`DELETE /mcp` 返回 `204`，由于服务无状态，仅用于协议兼容，不会销毁会话。MCP 与公共 API 使用相同的部署级 Basic Auth。

## AI 对话：诊断导出与对话内安装 CLI

这两个接口是 0.4.0 新增的错误自助处理路径，都不经 `/api/capabilities` 暴露，因此也不在 `openapi.yaml` 契约里。

### `GET /api/assistant/sessions/{id}/generations/{generationId}/diagnostics`

返回某一轮生成的 `version: 2` 诊断档案，对应界面上的「查看详情 / 复制诊断 / 下载诊断」：`environment`（平台与实例版本、每次调用的 CLI 版本与二进制来源）、`attempts[]`（进程状态、信号、stdout/stderr 行数、`firstStderr` 与 `firstError`）、`tools[]`（CLI 协议与平台 MCP 网关的工具调用，失败的调用带脱敏片段、`statusCode`、`requestId`）、`termination`（`USER_CANCELLED`、`USER_STEER`、`WORKSPACE_LEASE_LOST`、`CHECKPOINT_FAILED` 等）、`timeline`、`assessment`（`evidence` / `unknowns` / `nextSteps` 三段确定性结论）、`coverage` 与 `dropped`（各类截断计数）。

- 响应带 `Cache-Control: no-store`。诊断按 `generationId` 独立保存，重试后仍可导出历史失败轮次。
- `?download=1` 返回附件 `loopforge-diagnostics-<generationId>.json`，带 `Content-Disposition: attachment` 与 `X-Content-Type-Options: nosniff`。
- 未登录 `401`；不是项目成员或没有该会话的访问权 `403`；该轮没有诊断记录 `404`。公开项目的只读会话链接只返回固定摘要，不包含详细诊断。
- 档案不含提示词、思考正文、工具参数、成功工具的输出、环境变量与完整 stdout；密钥、令牌、URL 凭据与查询参数、ANSI 控制序列与本机绝对路径均已脱敏。

```bash
curl -H "Authorization: Bearer $AGENTFLOW_TOKEN" \
  "$AGENTFLOW_URL/api/assistant/sessions/chat-1/generations/gen-1/diagnostics?download=1" \
  -o diagnostics.json
```

### `POST /api/assistant/cli-install` / `GET /api/assistant/cli-install`

对话因为「服务器未安装某个 CLI」失败时，用这两个接口把它装到平台侧。请求只能选择一个引擎 id，不能传包名或 URL：`{"engine":"opencode"}`，`claude` 等价于 `claude-code`。

- 可安装：`codex`、`codebuddy`、`claude-code`、`opencode`、`cursor`、`agy`、`kimi`、`pi`、`dsh`。`zcode` 不在其中：官方 ZCode 没有 npm 包，只能由执行机原生安装后由 runner 探测。
- 下载在后台进行，关闭页面不会中断，单次上限 10 分钟。
- `POST` 成功返回 `{"engine":"opencode","installed":true}`；引擎不可安装 `400`；未登录 `401`；下载或安装失败 `502`，body 为 `{"error":"..."}`。
- `GET ?engine=opencode` 供页面重载后查询，返回 `{"engine":"opencode","status":"missing|installing|installed|failed"}`，`failed` 时附带 `error`，同样会返回 `400` / `401`。

```bash
curl -H "Authorization: Bearer $AGENTFLOW_TOKEN" -H 'Content-Type: application/json' \
  -d '{"engine":"opencode"}' "$AGENTFLOW_URL/api/assistant/cli-install"
curl -H "Authorization: Bearer $AGENTFLOW_TOKEN" \
  "$AGENTFLOW_URL/api/assistant/cli-install?engine=opencode"
```

## 明确不属于公共 API 的接口

以下实现细节不应被外部客户端当作稳定能力：

- `POST /api/runs/{runId}/signals`：Worker 向控制面上报 envelope 的内部入口。
- `POST /api/otel/v1/traces`：仅 loopback 可用的内部 OTLP 接收入口；即使启用了 Basic Auth，本机调用也可绕过 Basic Auth。
- run 中的 `ownerInstanceId`、`leaseUntil`、`executionGeneration`、`workspaceHost`、`workspaceRoot` 以及节点/运行的 `sandboxId`：实例 lease 和执行定位元数据，仅作不透明诊断字段。
- 沙箱回收、清理和生命周期操作：当前没有公共 HTTP 接口。`/api/sandboxes` 只管理沙箱配置模板，不执行回收。

因此，本套文档不提供 Worker signals、OTLP、lease 管理或沙箱回收的调用示例。

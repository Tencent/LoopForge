# 工蜂与 TAPD 同步 API

认证和通用状态码见 [README](README.md)。同步服务使用注册表中的 MCP Server 和项目/仓库配置访问外部系统。

## 同步配置

`SyncConfig` 字段：

```json
{
  "playbookSeedRev":1,
  "gongfengProjectId":"10001",
  "gongfengMcpUrl":"https://...",
  "tapdWorkspaceId":"123",
  "tapdIterationId":"456",
  "tapdMcpUrl":"https://...",
  "autoSyncEnabled":true,
  "autoSyncIntervalSec":300,
  "lastSyncAt":"2026-09-02T12:00:00Z",
  "lastSyncStatus":"success",
  "lastSyncError":""
}
```

### `GET /api/sync/config`

返回当前配置，`200`。

### `POST|PUT /api/sync/config`

两种方法语义相同。保存配置并重新配置同步服务、启动/刷新自动同步。`autoSyncIntervalSec<=0` 时设为 `300`；客户端提交的 `lastSyncAt`、`lastSyncStatus`、`lastSyncError` 会被忽略并保留服务端当前值。

成功 `200` 返回保存后的 `SyncConfig`；JSON/存储错误 `400`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X PUT \
  -H 'Content-Type: application/json' \
  -d '{"gongfengProjectId":"10001","tapdWorkspaceId":"123","tapdIterationId":"456","autoSyncEnabled":true,"autoSyncIntervalSec":300}' \
  "$AGENTFLOW_URL/api/sync/config"
```

## 手动拉取

### `POST /api/sync/now`

无请求体。重新加载 MCP 认证配置，在后台启动一次全量 pull，并立即返回：

```json
{"status":"started","syncing":true}
```

正常状态码 `200`。它是异步长任务启动接口：`200` 只表示 goroutine 已启动，不表示同步成功，也不返回任务 ID。用 `/api/sync/status` 轮询 `syncing` 和配置中的最近结果；也可查看 `/api/sync/logs`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X POST \
  "$AGENTFLOW_URL/api/sync/now"

curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" \
  "$AGENTFLOW_URL/api/sync/status"
```

当前实现不会因另一次同步已在运行而改变 HTTP 响应；同步服务内部负责并发互斥。

## 状态与日志

### `GET /api/sync/status`

返回 `200`：

```json
{
  "syncing":false,
  "config":{"lastSyncStatus":"success","lastSyncAt":"..."},
  "logs":[]
}
```

`logs` 为最近同步记录，格式同下。

### `GET /api/sync/logs`

返回最近最多 100 条 `SyncLog[]`，按新到旧排列，不提供游标分页：

```json
[
  {
    "id":"sync-...",
    "direction":"pull",
    "source":"gongfeng",
    "action":"sync_issues",
    "status":"success",
    "count":12,
    "message":"...",
    "startedAt":"...",
    "finishedAt":"..."
  }
]
```

正常状态码 `200`。服务内部最多保留 200 条，但 API 只暴露最近 100 条。

## 将已有 Issue 推送到外部系统

### `POST /api/sync/create`

请求：

```json
{"issueId":"issue-1","target":"gongfeng"}
```

- `issueId`、`target` 必填。
- `target` 只能为 `gongfeng|tapd`。
- 推送工蜂时，Issue 未绑定仓库会尝试继承项目默认仓库；最终仍需有效的工蜂仓库配置。
- 调用前会重新加载 MCP 认证配置。

成功 `200` 返回带 `source` 和 `externalRef` 的已保存 Issue；字段错误/外部创建失败通常 `400`；Issue 不存在 `404`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X POST \
  -H 'Content-Type: application/json' \
  -d '{"issueId":"issue-1","target":"tapd"}' \
  "$AGENTFLOW_URL/api/sync/create"
```

新建本地 Issue 时也可以在 `POST /api/issues` 中直接传 `syncTarget`，详见 [协作与需求 API](collaboration.md)。

## 与定时任务的关系

自动同步配置控制周期性 pull；`/api/schedule-jobs` 则用于按工蜂/TAPD 来源拉取候选项并派发指定 SOP，两者不是同一任务模型。定时任务接口见 [编排与运行 API](orchestration.md)。

同步接口没有 SSE、文件下载或分页端点。外部 MCP 认证、内部 Worker signals、OTLP、实例 lease 和沙箱回收均不属于本页公共 API。

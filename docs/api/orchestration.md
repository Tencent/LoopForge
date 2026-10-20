# 编排、运行与调度 API

认证和通用状态码见 [README](README.md)。

## SOP（Workflow）

`WorkflowDefinition` 主要字段：

```json
{
  "id":"wf-dev",
  "name":"研发 SOP",
  "version":1,
  "revision":0,
  "status":"draft",
  "owner":"platform",
  "tags":["研发"],
  "description":"...",
  "sandboxProfileId":"sandbox-go",
  "nodes":[{"id":"implement","name":"实现","type":"agent","agentId":"agent-dev","maxAttempts":2,"maxReworks":1,"position":{"x":0,"y":0}}],
  "edges":[{"id":"e1","source":"implement","target":"review","when":"success"}]
}
```

节点类型为 `agent|script|approval`。脚本节点通过 `scriptLanguage`（`shell|python`）和 `script` 配置；Agent 节点只引用角色定义，任务书写在角色的 `taskPrompt` 上。节点可配置制品契约、失败/重试策略和画布位置。角色的 CLI 仅可选择 CodeBuddy、Codex 或 Claude Code。旧的 `command` 节点在保存时会迁移为 shell 脚本节点。

### `GET /api/workflows`

返回全部 `WorkflowDefinition[]`，`200`，不分页。

返回值包含两类 SOP：项目里存储的 SOP，以及 `generic_templates` 表中的**通用模板**（payload 带 `generic: true`，`projectId` 为空）。通用模板与项目无关，所有项目都能看到；普通用户只读，复制到项目后才能编辑。运行时按和自定义 SOP 相同的流程解析节点与角色。通用模板通过 `requirements` 声明运行依赖（例如 `gongfeng-git-token`、`gongfeng-mcp`、`taihu-pat`），Studio 会在复制前做依赖预检。需要人工把关时可在复制后自行添加审批节点。

### `PUT /api/workflows`

按 `id` 保存。`id` 和 `sandboxProfileId` 必填；`version=0` 时改为 `1`。成功 `200` 返回保存后的 SOP；revision 冲突返回 `409`，其他校验/存储错误 `400`。`id` 命中通用模板时返回 `409`（通用模板只读）。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X PUT \
  -H 'Content-Type: application/json' \
  -d '{"id":"wf-dev","name":"研发 SOP","version":1,"status":"draft","sandboxProfileId":"sandbox-go","nodes":[],"edges":[]}' \
  "$AGENTFLOW_URL/api/workflows"
```

### `GET /api/workflows/{id}`

成功 `200` 返回完整 SOP（含通用模板）；不存在 `404`。

### `DELETE /api/workflows/{id}`

成功 `204`，无响应体。有 `running|waiting` run，或仍被未关闭 Issue 绑定时返回 `409`；不存在 `404`；其他删除错误 `400`。通用模板返回 `409`（只读）。

### revision

- `GET /api/workflows/{id}/revisions`：`200 DefinitionRevision[]`。
- `GET /api/workflows/{id}/revisions/{revisionId}`：`200 DefinitionRevision`；revision 不存在或不属于该 SOP 时 `404`。
- `POST /api/workflows/{id}/revisions/{revisionId}/restore`：恢复并 `200` 返回 SOP；失败通常 `400`。

以上接口在 SOP 本身不存在时先返回 `404`。

### `POST /api/workflows/{id}/publish`

无请求体。设置 `status=published`，`version` 加一，成功 `200` 返回 SOP；不存在 `404`；保存失败 `400`。

### `POST /api/workflows/{id}/clone`

请求：

```json
{"id":"wf-dev-copy","name":"研发 SOP 副本","owner":"u-1","projectId":"alpha"}
```

`id`、`name` 必填。副本固定为 `draft`、`version=1`、`revision=0`，成功 `201`；字段/存储错误 `400`，源 SOP 不存在 `404`。

源 SOP 是通用模板时，复制会同时把模板引用的角色创建到 `projectId` 指定的项目（已存在的同名角色直接复用），并把节点上的 `agentId` 重写到本项目角色，因此复制出的 SOP 可直接运行；此时 `projectId` 必填，缺失返回 `400`。模板声明的 `requirements` 会保留在副本上，Studio 继续据此提示依赖（例如发起 MR 节点需要工蜂 Git Token / 工蜂 MCP + 太湖个人令牌）。

## 运行

run 状态通常为 `running|waiting|succeeded|failed|cancelled`。响应中的 `nodes` 是 node id 到 `NodeRun` 的映射；还包含 usage、toolCalls、artifact 元数据、时间和 revision。

`ownerInstanceId`、`leaseUntil`、`executionGeneration`、`workspaceHost`、`workspaceRoot`、`sandboxId` 是内部 lease/执行定位信息。客户端可忽略这些字段，不应据此实现实例接管或沙箱生命周期操作。

### `GET /api/runs`

返回全部 run 摘要数组，`200`，不分页。摘要省略 artifacts 内容、遥测、日志、事件和 workflow snapshot，截断 input/错误，并省略已完成节点的大输出。

可传 `projectId` 限定项目，`responsible=me` 只返回当前登录用户负责执行或关联需求责任人为当前用户的任务；`responsible=me` 未登录时返回 `401`。两项筛选可以组合。

### `POST /api/task-groups`

创建自由协作任务，`201` 返回 Run。必填 `projectId`、`input`；不传 `workflowId` 时无需 SOP 模板，Main Bot 可直接协作，角色在创建后按需邀请。可传 `sandboxProfileId` 选择本项目或共享运行环境（省略时使用 `local`）。传 `workflowId` 时仅继承可访问模板的角色与环境，不自动执行 SOP，不能同时覆盖 `sandboxProfileId`。

`repositoryIds` 可指定本项目的一个或多个仓库，按配置主分支创建任务工作分支；不属于本项目或不存在的仓库会拒绝整个请求。显式 `[]` 表示不使用代码仓库；省略或 `null` 保持旧接口选择项目全部仓库的行为。

```json
{"projectId":"platform","input":"协作优化前后端接口","sandboxProfileId":"project-sandbox","repositoryIds":["frontend","backend"]}
```

### `POST /api/runs`

直接启动 SOP：

```json
{"workflowId":"wf-dev","input":"实现审计日志","projectId":"platform"}
```

`projectId` 可选，但给出时必须存在。成功 `201` 返回新 run；执行被禁用 `403`；工作流/参数/引擎错误通常 `400`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X POST \
  -H 'Content-Type: application/json' \
  -d '{"workflowId":"wf-dev","input":"实现审计日志","projectId":"platform"}' \
  "$AGENTFLOW_URL/api/runs"
```

该接口只启动长任务，不等待完成。保存为 Issue 的工作优先使用 Issue dispatch，以建立 task/issue 关联。

### `GET /api/runs/{runId}`

成功 `200` 返回 run 详情；不存在 `404`。详情：

- artifact 只返回元数据，不返回 `content`；
- telemetry/logs 不内嵌，应使用独立接口；
- workflow snapshot 会移除节点 script（以及兼容字段 command）；
- events 最多返回最近 80 条；部分输出文本会截断。

### `DELETE /api/runs/{runId}`

成功 `200 {"status":"deleted"}`；不存在或不能删除通常 `400`。当前处理器本身不额外检查运行状态，约束由存储层决定。

## SSE 实时运行

### `GET /api/runs/{runId}/stream?after={seq}&logsAfter={seq}`

成功响应 `200 text/event-stream`。`after` 是已消费的遥测 sequence，`logsAfter` 是日志 sequence；无效/缺省按 `0`。每个更新事件：

```text
id: 12
event: run
data: {"run":{...},"telemetryDelta":[...],"logDelta":[...]}
```

- `id` 为 run revision。
- `run` 是实时详情，但不含 events、遥测/日志正文，input 和节点文本会截断。
- 每次 `telemetryDelta`、`logDelta` 最多保留最新 80 条。
- 空闲时每 15 秒发送注释心跳 `: heartbeat`。
- run 不存在或订阅失败返回 `404`；服务器不支持 flush 返回 `500`。
- 客户端断开即取消订阅；服务端关闭更新通道时流结束。当前不解析 `Last-Event-ID`，重连必须显式传 sequence。

```bash
curl -N -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" \
  "$AGENTFLOW_URL/api/runs/run-1/stream?after=0&logsAfter=0"
```

## 日志、遥测与下载

### 游标分页

`GET /api/runs/{runId}/logs` 和 `/telemetry` 共用 sequence 游标：

- `limit`：默认 100，最大 200；小于等于 0 也按 100。
- `after=N`：正向读取 `sequence>N`。
- `before=N`：向前读取 `sequence<N`；未传 `after`/`before` 时返回最新一页。
- 同时传入时 `after` 优先。
- 返回始终按 sequence 升序。
- `nextAfter` 是本页最后一条 sequence，适合增量刷新；`nextBefore` 是第一条，适合向前翻页；`hasMore` 表示当前方向还有数据；`sequence` 是 run 当前总序号。

### `GET /api/runs/{runId}/logs`

参数：`nodeId`、`after`、`before`、`limit`。成功 `200`：

```json
{"runId":"run-1","sequence":321,"logs":[{"id":"log-1","sequence":201,"timestamp":"...","level":"info","stream":"stdout","body":"...","source":{},"context":{}}],"nextAfter":201,"nextBefore":201,"hasMore":false}
```

响应中 log body 最长 2000 字节；不存在 `404`，查询错误通常 `400`。

### `GET /api/runs/{runId}/telemetry`

参数：`nodeId`、`adapter`、`after`、`before`、`limit`。`adapter` 可为 `all`/空、`hooks`、`otel`、`live` 或具体 adapter 名。成功 `200`：

```json
{"runId":"run-1","sequence":120,"events":[{"id":"ev-1","sequence":101,"category":"tool","name":"tool.call","message":"...","source":{"adapter":"live","protocol":"..."}}],"nextAfter":101,"nextBefore":101,"hasMore":false}
```

公共响应会裁剪 message，移除 attributes、metrics、tool/modelOperation 详情，并精简 source/context。不存在 `404`，查询错误通常 `400`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" \
  "$AGENTFLOW_URL/api/runs/run-1/logs?nodeId=implement&before=500&limit=100"
```

### `GET /api/runs/{runId}/logs.txt?nodeId={nodeId}`

下载 worker 日志，`200 text/plain; charset=utf-8`，响应头：

```text
Content-Disposition: attachment; filename="run-1-worker-logs.txt"
```

省略 `nodeId` 下载所有节点。最多导出 20,000 条，格式为头部注释以及时间、节点、stream、正文。run 不存在 `404`；导出失败通常 `400`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -OJ \
  "$AGENTFLOW_URL/api/runs/run-1/logs.txt"
```

### `GET /api/runs/{runId}/artifacts/{artifactId}`

返回制品原始文本内容，`200`。`Content-Type` 为记录的 media type 加 `charset=utf-8`，`Content-Disposition: inline`，并以 SHA-256 设置 `ETag`。`text/markdown`、`application/json` 分别使用 `.md`、`.json` 文件名，其他类型使用 `.txt`。制品不存在返回 `404`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -OJ \
  "$AGENTFLOW_URL/api/runs/run-1/artifacts/artifact-1"
```

## 人工动作

以下接口成功均返回 `200 {"ok":true}`；run/node/状态错误通常 `400`。与 Issue 关联且执行被关闭/交付锁定的 run，在 approve、pass、reject、retry 时返回 `409`。

### `POST /api/runs/{runId}/approve`

```json
{"nodeId":"approval"}
```

批准等待中的节点。

### `POST /api/runs/{runId}/pass`

```json
{"nodeId":"phpunit-full"}
```

将已失败的**脚本**节点视为通过并继续下游，不重新执行该节点。用于 `onFailure=fail` 停下来之后，或打回返工到达最大轮次后，避免硬脚本一直卡住。Agent / AI 节点不能手动通过，应走 `/retry` 并附带补充上下文。

### `POST /api/runs/{runId}/reject`

```json
{"nodeId":"approval","targetId":"implement","reason":"缺少回归测试"}
```

拒绝并按 `targetId`/工作流策略回退。

### `POST /api/runs/{runId}/retry`

```json
{"nodeId":"implement","reason":"修复失败测试"}
```

重试节点。

### `POST /api/runs/{runId}/decline-retry`

```json
{"nodeId":"implement"}
```

拒绝引擎建议的重试。

### `POST /api/runs/{runId}/cancel`

无需请求字段；可使用空 JSON `{}`。取消 run。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X POST \
  -H 'Content-Type: application/json' -d '{"nodeId":"approval"}' \
  "$AGENTFLOW_URL/api/runs/run-1/approve"
```

## Run 评测

只评测终态 run（`succeeded|failed|cancelled`）。使用当前登录用户的 CodeBuddy Key 和助手模型目录，不自动触发。

### `POST /api/runs/{runId}/eval`

请求 `{ "model": "default" }`，`model` 可选。成功 `202` 返回评测记录（`status=running`）。进行中的 run 返回 `409`；未登录 `401`；未配置个人 CodeBuddy Key `400`。

### `GET /api/runs/{runId}/eval`

最新一次评测。尚无记录时 `200` 且 `status` 为空。

### `GET /api/runs/{runId}/evals`

最近最多 10 次评测。

### `GET /api/runs/{runId}/eval/stream`

SSE：`thought` / `done` / `error`。

### `POST /api/runs/{runId}/eval/apply`

请求 `{ "evalId":"eval-1", "recommendationIds":["rec-1"] }`。把用户确认的建议合并进**当前** SOP 或角色，不改这次 run 的冻结 snapshot。通用只读模板会先克隆到项目再改。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X POST \
  -H 'Content-Type: application/json' -d '{"model":"default"}' \
  "$AGENTFLOW_URL/api/runs/run-1/eval"
```

## 指标与分析

### `GET /api/workflow-metrics`

返回全部 `WorkflowMetrics[]`，`200`。每项含运行数、各状态计数、闭环/成功/返工率、平均耗时、token/tool/artifact 汇总、最近运行和节点指标。

### `GET /api/analytics`

查询参数：

- `projectId`、`workflowId`：可选过滤；
- `userId`、`issueSource`：按责任人或需求来源（`local`、`gongfeng`、`tapd`）过滤；
- `scope=overview`：返回用量页所需的轻量聚合，跳过交付扫描；
- `grain`：趋势粒度，可选 `day`、`week`、`month`、`all`（按月聚合全部历史）；
- `from`、`to`：可选 RFC 3339 时间；无效值返回 `400`。

成功 `200` 返回 `AnalyticsSnapshot`：`filter`、`summary`、`trend`、按 workflow/agent/executor/model/node 的 breakdown、`failures`、`coverage`、`deliveries` 和 `delivery`。`demo` 提供平均 Token、工具成功率、平均人工干预和自动完成率；`runTraces` 提供 `TAPD/Issue → workflow_task_id → run_id → trace_id` 的下钻索引，原始事件继续通过 Run telemetry 分页接口读取。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" \
  "$AGENTFLOW_URL/api/analytics?projectId=platform&from=2026-09-01T00%3A00%3A00Z"
```

### `GET /api/observability/spec`

返回运行时支持的公共观测规范说明对象，`200`。这是能力描述，不是数据摄取端点；内部 `/api/otel/v1/traces` 不属于公共 API。

## 定时任务

`ScheduleJob` 分两类，由 `skipDispatch` 区分，不能写在同一条任务里。

触发任务（`skipDispatch=false`）字段包括 `id`、`name`、`enabled`、`projectId`、`workflowId` / `useIssueWorkflow`、`intervalSec` 或 `cron`、最近执行结果和时间戳。到点扫描本项目未运行需求，做基线门禁再派发 SOP；仓库集成分支冲突会把任务标成 `waiting` 并带上 `blockers`，暂停自动派发；对齐 Agent 会自动解决冲突并合入基线。

AI 定时任务（`skipDispatch=true`）字段包括 `id`、`name`、`enabled`、`projectId`、`assistantPrompt` / `assistantModel` / `ownerUserId`、`intervalSec` 或 `cron`。到点用项目 Owner 的 CodeBuddy 按提示词原文执行并自动写入，不扫描需求、不派发 SOP。

AI 定时任务固定使用自动执行模式，不受 Studio 对话审批模式影响，平台写入无需逐次人工确认。

AI 定时任务单次执行最长 180 分钟。本实例已有定时任务运行时，到点的 cron 会被跳过，不排队、不在当前任务结束后补跑；等待下一个 cron 时间。手动运行仍返回 `409`。

### `GET /api/schedule-jobs`

返回全部 `ScheduleJob[]`，`200`，不分页。

### `POST|PUT /api/schedule-jobs`

新建或按 `id` 更新，两种方法语义相同。`name` 必填。调度为 `intervalSec`（常用间隔）或 5 段 `cron`（东八区）。`skipDispatch` 为 true 时是 AI 定时任务，必须有 `assistantPrompt`，保存时清空 SOP 字段；否则是触发任务，必须有 `workflowId`，保存时清空提示词。可选 `useIssueWorkflow`。`assistantModel` 取自助手 CodeBuddy 目录。保存时把项目 Owner 记为 `ownerUserId`。更新时若请求未带 `blockers`，保留触发任务已有的基线暂停状态。

`id` 为空时生成；`projectId` 默认 `platform`；`intervalSec<30` 时重置为 `300`。更新若不提供 `lastRunAt` 会保留已有最近运行字段。成功 `200` 返回 job；校验/存储错误 `400`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X POST \
  -H 'Content-Type: application/json' \
  -d '{"name":"自动修线上单","enabled":true,"projectId":"platform","workflowId":"wf-dev","useIssueWorkflow":true,"intervalSec":3600}' \
  "$AGENTFLOW_URL/api/schedule-jobs"
```

### `DELETE /api/schedule-jobs/{id}`

成功 `200 {"ok":true}`；不存在/删除失败通常 `400`。

### `POST /api/schedule-jobs/{id}/run`

在服务端启动一次 job，`202` 返回 `lastStatus=running` 的 `ScheduleJob`。这次 HTTP 调用不等待扫描、AI 提示词或派发完成；关闭浏览器或断开 MCP 客户端都不会取消。用 `GET /api/schedule-jobs` 轮询，直到 `lastStatus` 变为 `success` / `failed` / `waiting`。被派发的 SOP 仍作为独立长任务运行。

- 当前实例 `AGENTFLOW_SCHEDULER_ENABLED=false` 时返回 `409`。
- 已有定时任务正在本实例运行时返回 `409`。
- job 不存在时通常 `400`。
- 启动后的失败写在 job 的 `lastStatus` / `lastError`，不通过这次 POST 的 HTTP 状态码返回。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X POST \
  "$AGENTFLOW_URL/api/schedule-jobs/job-1/run"
```

定时派发会冻结集成分支上下文，并避免同一需求重复执行。需求澄清未达标时本轮跳过该 Issue，不把整次任务标失败。Worker signals、实例 lease 和沙箱回收属于内部机制，不是调度公共 API。

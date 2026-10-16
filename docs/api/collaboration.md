# 协作与需求 API

认证、通用状态码和基础 URL 见 [README](README.md)。本页数组接口没有分页。

## 项目

### `GET /api/projects`

返回 `Project[]`，`200`。项目含 `id`、`key`、`name`、`description`、`color`、`workflowId`、`defaultRepositoryId`、`members`、`clarification`、TAPD 配置及时间戳。响应永不返回 `gitToken`、`codeBuddyApiKey` 明文，只以 `gitTokenConfigured`、`codeBuddyApiKeyConfigured` 表示是否已配置。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" "$AGENTFLOW_URL/api/projects"
```

### `GET /api/projects/{id}`

返回一个脱敏后的 `Project`；不存在返回 `404 {"error":"project not found"}`。

### `POST|PUT /api/projects`

两种方法语义相同，按 `id` 新建或整体更新。请求是 `Project`，附加可选清除开关：

```json
{
  "id":"platform",
  "name":"平台研发",
  "key":"PLATFORM",
  "defaultRepositoryId":"repo-platform",
  "workflowId":"wf-dev",
  "gitToken":"new-token",
  "gitTokenClear":false,
  "codeBuddyApiKey":"",
  "codeBuddyApiKeyClear":false,
  "codeBuddyInternetEnvironmentClear":false,
  "tapdClear":false,
  "clarification":{"enabled":true,"threshold":80,"dimensions":[]}
}
```

`id`、`name` 必填；`key` 为空时取大写 `id`。更新时敏感值留空会保留旧值，只有对应 `*Clear=true` 才清空。`defaultRepositoryId` 必须属于本项目。成功 `200` 返回脱敏后的项目；参数错误 `400`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X PUT \
  -H 'Content-Type: application/json' \
  -d '{"id":"platform","name":"平台研发","clarification":{"enabled":false,"threshold":0}}' \
  "$AGENTFLOW_URL/api/projects"
```

## 仓库

`Repository` 主要字段为 `id`、`projectId`、`provider`、`name`、`gongfengProjectId`、`webUrl`、`cloneUrl`、`defaultBranch`、`localPath`、流水线配置和 `integrateBranch`。

### `GET /api/repositories`

返回脱敏的 `Repository[]`，`200`；`gitToken` 被清空，以 `gitTokenConfigured` 表示状态。

### `GET /api/repositories/{id}`

返回一个脱敏后的 `Repository`；不存在返回 `404 {"error":"repository not found"}`。

### `POST|PUT /api/repositories`

新建或按 `id` 更新。`projectId`、`name`、`gongfengProjectId` 必填；`provider` 默认且只能是 `gongfeng`；`defaultBranch` 默认 `main`；`id` 为空时服务生成。更新时空 token 保留旧值，`gitTokenClear=true` 清除。成功 `200` 返回脱敏后的仓库；校验失败 `400`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X POST \
  -H 'Content-Type: application/json' \
  -d '{"projectId":"platform","name":"agentflow","gongfengProjectId":"10001","cloneUrl":"https://example/repo.git","defaultBranch":"main"}' \
  "$AGENTFLOW_URL/api/repositories"
```

### `DELETE /api/repositories?id={id}`

`id` 必填。成功删除仓库，并清除引用它的项目默认仓库设置，返回 `200 {"status":"deleted"}`；缺少 `id` 或存储错误为 `400`。

## Issue

`Issue` 常用字段：

- `id`、`projectId`、`number`、`title`、`description`
- `status`：`backlog|todo|in_progress|in_review|done|blocked|cancelled`
- `priority`、`kind`（`requirement|issue`）、`assigneeType`、`assigneeId`
- `workflowId`、`repositoryId`、`baseBranch`、`latestTaskId`
- `source`（`local|gongfeng|tapd`）、`externalRef`
- `clarification`：`score`、`passed`、`answers`、`dimensions`、`missing`、`status`（`in_progress|ready|needs_user`）、`assessedAt`。导入 TAPD 需求时立刻按标题+描述打分；`needs_user` 表示未达项目准入线，手动派发 SOP 会被 409。
- 交付字段 `deliveryState`、`deliveryMrUrl`、`deliveryMrState`、`deliveryMrs`、`deliveryUpdatedAt`。`deliveryMrs` 每个仓库一项（`repositoryId`、`repository`、`project`、`iid`、`url`、`state`、`targetBranch`），由 SOP 成功时按仓库工作分支向工蜂查询 MR 得出，查不到才回退到 MR 类产物里同项目的链接；`deliveryMrUrl` 为第一项，`deliveryMrState` 为聚合状态（全部合入默认分支才是 `merged`）。

### `GET /api/issues`

先根据关联 run 刷新 task/issue 状态，再返回匹配的需求。

可选 query：`projectId`、`status`（可逗号分隔，OR）、`source`（可逗号分隔，OR）、`q`（标题支持忽略大小写、空格、标点及多关键词顺序的模糊匹配，同时支持 id / 编号 / 外部 ID）、`limit`、`offset`。

- 不传 `limit`：返回 `Issue[]`（Studio 看板沿用此形态）。
- 传入 `limit`：返回 `{items,total,offset,limit,hasMore}`。`limit` 默认按 50 理解、最大 200；`offset` 默认 0。非法 `limit`/`offset` 返回 `400`。

成功 `200`。

### `POST|PUT /api/issues`

两种方法语义相同。请求：

```json
{
  "id":"issue-local-1",
  "projectId":"platform",
  "title":"增加审计日志",
  "description":"记录配置变更",
  "kind":"requirement",
  "repositoryId":"repo-platform",
  "workflowId":"wf-dev",
  "syncTarget":"gongfeng"
}
```

`projectId`、`title` 必填且项目必须存在。`status` 默认 `backlog`，`priority` 默认 `medium`；未指定仓库时继承项目默认仓库，指定仓库必须属于项目，`baseBranch` 默认仓库默认分支。

新 Issue 必须绑定外部 tracker：若请求未带有效 `source/externalRef`，`syncTarget` 必须为 `gongfeng` 或 `tapd`，服务会同步创建后保存；工蜂还要求仓库。更新时多个空字段会继承原值。成功 `200` 返回 Issue；校验/同步错误通常为 `400`。

### `GET /api/issues/{issueId}`

成功 `200`：

```json
{"issue":{"id":"issue-1","title":"..."},"comments":[{"id":"comment-1","body":"..."}]}
```

Issue 不存在返回 `404`。

### `POST /api/issues/{issueId}/comments`

请求 `IssueComment`；服务覆盖 `id` 和 `issueId`，`authorType` 默认 `member`。`body` 必须非空。成功 `200 {"ok":true}`；Issue 不存在 `404`；存储错误 `400`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X POST \
  -H 'Content-Type: application/json' \
  -d '{"authorId":"u-1","body":"验收范围已确认"}' \
  "$AGENTFLOW_URL/api/issues/issue-1/comments"
```

### `GET /api/issues/{issueId}/runs`

返回该 Issue 的 run 摘要数组，按时间倒序，`200`；Issue 不存在 `404`。摘要省略 artifacts 内容、遥测、日志、事件和 workflow snapshot，并截断部分文本。

### `POST /api/issues/{issueId}/clarify`

按项目配置的澄清维度计算分数：

```json
{"answers":{"scope":"仅修改 API 层并补充测试","acceptance":"所有接口测试通过"}}
```

成功 `200` 返回 `ClarificationAssessment` 并把 Issue `kind` 设为 `requirement`；项目不存在/存储错误 `400`，Issue 不存在 `404`。分数按维度字数加权，未达标时 `passed=false` 且 `status=needs_user`。

## AI 多轮澄清

### `POST /api/chat-clarify`

对尚未保存的需求进行澄清。`projectId` 必填且必须存在；请求字段：

```json
{
  "projectId":"platform",
  "repositoryId":"repo-platform",
  "title":"增加审计日志",
  "description":"记录配置变更",
  "message":"需要覆盖项目配置",
  "history":[{"role":"user","content":"..."},{"role":"assistant","content":"..."}]
}
```

### `POST /api/issues/{issueId}/chat-clarify`

请求结构相同，使用已保存 Issue 作为上下文；Issue 不存在 `404`。

已保存 Issue 的澄清在**服务端后台任务**中执行：浏览器断开、关闭网页或点抽屉外都不会取消。前台仍可用 SSE 看思考过程；`GET` 同一路径（`Accept: text/event-stream`）可重新接入正在跑的任务。`POST /api/issues/{issueId}/chat-clarify/cancel` 只取消这一张单。手动导入 TAPD 或新建需求评分未达标时会自动排队澄清；外部同步进来的需求不评分、不自动澄清。

最长 10 分钟。响应模式：

1. 普通 JSON：不设置 SSE `Accept`。成功 `200` 返回（客户端提前断开时任务仍继续，响应可能被截断）：

   ```json
   {"result":"原始模型结果","parsed":{"summary":"...","questions":[],"ready":true},"repository":{"id":"repo-platform","analyzed":true},"issue":{}}
   ```

2. SSE：发送 `Accept: text/event-stream`。响应 `200 text/event-stream`，事件依次可能为：
   - `open`：`{"repository":{...}}`
   - `thought`：`{"kind":"thinking","text":"..."}` 或 `{"kind":"tool","tool":"Read","detail":"file.go"}`
   - `done`：包含 `result`、`parsed`、`repository`，以及已持久化的 `issue`
   - `error`：`{"error":"..."}`。注意响应头已是 `200`，流内错误不会改成 HTTP 5xx。

普通模式下字段/项目错误 `400`，执行失败 `500`，超时 `504`。仓库拉取失败会降级，并在 `repository.reason` 中说明，不一定使请求失败。

```bash
curl -N -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" \
  -H 'Accept: text/event-stream' -H 'Content-Type: application/json' \
  -d '{"projectId":"platform","title":"增加审计日志","description":"记录配置变更"}' \
  "$AGENTFLOW_URL/api/chat-clarify"
```

### `POST /api/chat-clarify/cancel`

无请求体。取消当前实例上所有正在执行的澄清请求，返回 `200 {"cancelled":true}`。它不是按用户或澄清 ID 定向取消。

## 派发、重开和任务

### `POST /api/issues/{issueId}/dispatch`

启动绑定的 SOP。请求体可省略；可选：

```json
{"rerun":false}
```

成功 `201` 返回 `Task`，含 `id`、`issueId`、`workflowId`、`runId`、`status`、`dispatchMode`、`triggerOrigin` 和冻结的 `repository`。

- 首次手动派发创建确定性的 primary task。
- 已执行过时必须显式 `rerun:true`，此时创建新 task；尚未执行却传 `rerun:true` 返回 `400`。
- 工作流按 Issue、来源/类型默认值、项目默认值依次解析；仍为空返回 `400`。
- 已运行、手动导入/新建需求澄清未通过、交付已锁定或外部工蜂 Issue 已关闭等状态返回 `409`。外部同步进来的需求不走澄清闸门，可以直接派发。
- Issue 不存在 `404`；执行被实例禁用可返回 `403`；其他引擎错误通常 `400`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X POST \
  -H 'Content-Type: application/json' -d '{"rerun":true}' \
  "$AGENTFLOW_URL/api/issues/issue-1/dispatch"
```

这是长任务的“启动”接口，只等待 run 创建，不等待 SOP 完成。后续使用 `/api/runs/{runId}/stream` 或轮询 run。

### `POST /api/issues/{issueId}/reopen`

显式重开已交付 Issue，成功 `200` 返回更新后的 Issue。无法重开时 `409`；Issue 不存在 `404`。

### `GET /api/tasks`

刷新并返回全部 `Task[]`，`200`，无分页。Task 状态由 run 映射为 `running|completed|failed|cancelled`；关联 Issue 的阶段也会同步更新。

### `GET /api/tasks/{id}`

刷新状态后返回一个 `Task`；不存在返回 `404 {"error":"task not found"}`。

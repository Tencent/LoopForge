# 外部工作流用量接入

外部系统自行执行工作流，在成功、失败或取消后上报一次完整汇总。数据独立存放在 `insight_sources`、`insight_executions`，不创建平台 Run，不触发执行、恢复或交付。用量分析合并展示 Token、成本、执行次数、成功率、项目、责任人、模型和趋势。

## 1. 创建数据源

登录后选择项目，进入「用量分析 → 外部数据源」。项目负责人、maintainer 或平台管理员可以创建、查看及撤销数据源。创建时配置名称、有效期（默认 90 天，最多 365 天）和可选责任人映射。

- 外部账号必须显式映射到项目成员或负责人；不按同名用户推断。
- Token 仅创建时返回，数据库只存 SHA-256 摘要及显示前缀。
- 一个 Token 绑定一个项目和来源，只能用于上报接口，不能读取或管理平台资源。
- 撤销阻止后续写入，但保留既有统计。过期或轮换时创建新数据源；旧执行的重试仍属于原数据源，不能换新来源重报同一历史执行。
- 未配置责任人时省略 `externalUserId`，统计归入“未分配”；未知外部账号会被拒绝。

管理接口（使用已登录会话或个人 API Token）：

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/api/insights/v1/sources` | 创建，返回 `{source, token}` |
| GET | `/api/insights/v1/sources?projectId=...` | 列表，不返回凭据 |
| DELETE | `/api/insights/v1/sources/{id}` | 撤销，保留历史 |

创建请求示例：

```json
{"projectId":"your-project","name":"团队评审流水线","expiresInDays":90,"userMappings":{"zhangsan":123}}
```

其中 `123` 必须替换为本项目成员的平台用户 ID。只需项目统计时可省略 `userMappings`。

## 2. 保存执行汇总

将以下内容保存为 `usage.json`，用真实执行 ID、时间及模型计量替换示例：

```json
{
  "id": "build-123:completed",
  "type": "workflow_execution",
  "externalExecutionId": "build-123",
  "workflow": {"id": "code-review", "name": "代码评审"},
  "externalUserId": "zhangsan",
  "url": "https://ci.example.com/build/123",
  "startedAt": "2026-10-09T02:00:00Z",
  "finishedAt": "2026-10-09T02:03:00Z",
  "executionDurationMs": 150000,
  "status": "succeeded",
  "usage": [{
    "provider": "example-provider",
    "model": "example-model",
    "inputTokens": 12000,
    "outputTokens": 3000,
    "cacheReadTokens": 2000,
    "cacheWriteTokens": 0,
    "reasoningTokens": 1000,
    "costUsd": null,
    "costStatus": "unknown"
  }]
}
```

计量契约：

- 一条记录是一轮**完整执行汇总**，不是增量或会话快照。仅接受 `succeeded/failed/cancelled`；工作流重跑须使用新的执行 ID。
- `usage` 包含 1–64 项，每个 provider/model 一项；相同模型的多次调用先求和。模型维度的执行状态/成功率不能由工作流结果推断。
- 输入包含缓存读写 Token，输出包含推理 Token。总 Token = input + output，不再次累加缓存/推理子项。适配器需先转换供应商原始口径。
- 每项 Token 为非负整数且不超过 10^12；缓存读写之和不得超过输入，推理不得超过输出。
- 原始上报成本单位固定 USD。`reported/estimated` 要求非负金额（0 是已知零成本），`unknown` 要求 `costUsd: null`。部分模型未知时保留已知金额并标记部分未识别。
- `executionDurationMs` 可选，是活跃节点耗时之和，不包含人工审批和排队；省略时不参与总览执行耗时分位数/平均值。起止时间计算墙钟耗时。
- 时间使用 RFC3339，服务端归一到 UTC 毫秒精度；按 `finishedAt` 归入日/周/月趋势和时间筛选。允许历史补报，实时读取不依赖导入时间；结束时间不能晚于当前时间 5 分钟。
- `url` 可选，只接受不含凭据的 HTTP(S) 地址，用于回到原系统查看执行。
- 不接受客户端填写 `projectId/sourceId/responsibleUserId`；这些归属来自凭据及管理员配置。未知字段拒绝，单请求最多 1 MiB。

## 3. 通过 Skill 集成工作流

可移植 Skill 位于 `skills/report-workflow-usage/`，包含 `SKILL.md`、独立 Python 上报脚本、协议和工作流挂载说明。外部 Agent 工作流可以直接引用该 Skill；AgentFlow 中可在「技能管理」上传文件夹/ZIP，再绑定收尾节点使用的角色。

宿主将最终汇总文件路径注入 `AGENTFLOW_USAGE_FILE`，地址与密钥分别注入 `AGENTFLOW_URL`、`AGENTFLOW_INSIGHT_TOKEN`，收尾节点使用 `$report-workflow-usage`。Skill 支持不联网的 `--check`，缺少真实输入/输出 Token 时会拒绝上报。

Skill 不会自动获得执行器统计或继承进程环境；宿主必须提供最终文件及环境变量。若需计入最后一个 Agent 自身的完整消耗，应由宿主在该 Agent 退出后调用 Skill 内脚本。平台原生 Run 不重复上报。

详细接入方式见 [工作流集成](../skills/report-workflow-usage/references/workflow-integration.md)。生成上传包：

```sh
python3 -m zipfile -c /tmp/report-workflow-usage.zip skills/report-workflow-usage
```

## 4. 直接使用脚本上报

在 CI 密钥配置中设置 `AGENTFLOW_INSIGHT_TOKEN`，再运行：

```sh
export AGENTFLOW_URL='https://your-agentflow.example.com'
python3 scripts/report-workflow-usage.py usage.json
```

仓库入口委托给 Skill 内的同一份脚本，避免两份实现漂移。脚本读取 `AGENTFLOW_INSIGHT_TOKEN`，对网络错误、429、5xx 有界重试，拒绝重定向；不会删除 JSON 文件。凭据只放在 Authorization 头。生产使用 HTTPS，网关须允许该路径通过到服务本身，最终由数据源凭据鉴权。

也可直接调用 `POST /api/insights/v1/ingest`，携带 `Authorization: Bearer <数据源Token>`：

- `201 {"status":"accepted",...}`：事务已持久化，立即可分析。
- `200 {"status":"duplicate",...}`：同一来源、相同记录 ID、相同内容的安全重试。
- `409`：记录 ID 内容改变，或同一来源下执行 ID 被另一记录占用；不会覆盖旧事实。
- `400/413`：字段或体积错误，修正后再提交。
- `401/403`：无效、过期、撤销凭据或来源项目已删除。

来源内 `(sourceId, id)` 与 `(sourceId, externalExecutionId)` 都唯一，数据库约束及事务保证多实例幂等。第一版不支持覆盖更正、实时快照、调用明细或跨来源自动去重；同一次执行应只指定一个权威来源，避免工作流与模型网关重复上报。

## 5. 看板与统计边界

「执行来源」可选全部、平台内、外部工作流；API 对应 `/api/analytics?origin=all|internal|external`。项目、责任人、时间和业务域权限范围同时适用。外部工作流按来源隔离名称/ID，返回的 workflow key 为 `external:{sourceId}:{workflowId}`。

外部数据缺少平台需求关联，因此不计需求数、MR 交付数、需求来源及需求分类。选择需求来源筛选会排除外部记录。人工干预、自动完成率、一次通过率只使用平台执行事件；外部汇总不推断这些质量指标。外部追溯行链接原系统，不跳到不存在的平台 Run。

新增领域写入归 `InsightsRepo` / `insightsHandlers`，分析读取归 `AnalyticsRepo`。SQL 基线为 `migrations/003_external_workflow_usage.sql`，由运维手动执行；生产增量 DDL 可补建缺失表/索引，不自动迁移数据或 seed。

统计展示使用人民币：Hy 保留上报金额，其他模型按公开标准价和输入/输出/缓存分项重新测算；原始上报事实不改写。详细口径见 [Token 成本统计](token-pricing.md)。

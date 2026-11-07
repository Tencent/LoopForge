# 执行汇总契约

`POST {AGENTFLOW_URL}/api/insights/v1/ingest`，以环境变量中的数据源 Token 设置 Bearer Authorization。使用随包脚本发送，不手工展开 Token。

## 文件结构

以下是格式示例，不是真实用量。执行 ID、时间及计量值须由宿主提供：

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

- `id/externalExecutionId/workflow.id/workflow.name`：必填，每项最多 256 UTF-8 字节；一次重跑使用新的执行 ID，上报重试复用原 ID。
- `type` 固定 `workflow_execution`。`status` 仅支持 `succeeded/failed/cancelled`。
- 时间使用带时区 RFC3339，开始不早于 2000 年，结束不早于开始、不超过当前时间 5 分钟。按完成时间统计，接收时间由服务端生成。
- `externalUserId` 可选（最多 128 字节），必须已在数据源中映射到项目成员；无责任人就省略。
- `url` 可选（最多 2048 字节），为不含用户名/密码的 HTTP(S) 原系统执行链接。
- `executionDurationMs` 可选，非负整数，最大 315360000000，是活跃节点耗时之和；未知时省略，不用墙钟时长代替。
- `usage` 必须有 1–64 项，每个 provider/model 仅一项。来源和真实模型名称必填，各不超过 128 字节。同模型多次调用先汇总，避免累加供应商的累计快照。
- 输入/输出 Token 必须有真实计量，非负整数，每项不超过 10^12。输入**包含**缓存读写，输出**包含**推理；总计由服务器计算为输入加输出。供应商输入若不含缓存，应先加回缓存；若已经包含，不能再加。缓存读写之和不得超过输入，推理不得超过输出。
- 缓存/推理细分可省略，代表未提供，不影响真实输入/输出总量。金额统一 USD；`reported/estimated` 需真实上报或可追溯估算的非负金额，最高 10^9；`unknown` 使用 `costUsd: null`。费用不能通过缺失值假定为零。
- 禁止填写 `projectId/sourceId/responsibleUserId/totalTokens`，归属与合计由服务器确定；不发送提示词、源码或完整日志。

文件最多 1 MiB。服务端按来源分别约束记录 ID 和执行 ID，已接收记录不可覆盖：201 accepted、200 duplicate 都成功；409 表示已有事实冲突，修正需人工核对，本版无覆盖 API。HTTP 4xx 不自动重试（429 除外）。

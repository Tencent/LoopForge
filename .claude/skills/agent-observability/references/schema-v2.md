## Schema v2.3

`agent-observability` 输出 `ndjson`，每行一个事件对象。

> 以下字段表基于当前 `logs/metrics.ndjson` 的实际输出整理；其中“可选字段”只会在特定场景出现。

### 通用字段

- `event`: `start | user_prompt_submit | tool | usage | stop | error | stage_transition`
- `sid`: session id
- `ts`: 秒级时间戳（float）
- `turn_id`: 可选；由 `UserPromptSubmit` 生成，贯穿本轮事件

### Devflow 感知字段（可选，见 `core/devflow.py`）

只有当前项目正在跑 `.claude/runtime` 描述的 multi-agents-devflow 工作流（存在
`.claude/teams/multi-agents-devflow-{task_slug}/` team 目录）时才会出现，非
devflow 项目完全不受影响：

- `tool` / `usage` / `stop` 事件上会附带 `task_slug`（devflow 需求标识，用于把
  main + 最多 7 个常驻角色跨事件串成"同一次运行"；⚠️ `/resume-devflow` 中断恢复
  可能发生在新的 CodeBuddy 顶层 session 里，因此**聚合一次 devflow 运行要按
  `task_slug` 分组，不能按 `sid` 分组**）与 `stage`（`workflow-state.json` 的
  `current_stage`，如 `TASK-03`/`CODE-REVIEW`）。
- `tool` 事件在能从 subagent transcript 文件名识别出并行 sub-developer 轨道时
  （`sub-developer-PT-01` 这类命名）会额外带 `pt_id`（如 `"PT-01"`）。按事件自己的
  transcript_path 推断，不依赖任何 session 级共享状态——并行轨道是真并发，不能
  用一个可变指针记"当前是哪条轨道"。

### 事件字段

| event | 实际顶层字段 | 说明 |
|---|---|---|
| `start` | `event`, `sid`, `agent`, `ts` | 会话开始 |
| `user_prompt_submit` | `event`, `sid`, `agent`, `turn_id`, `prompt_len`, `ts` | 新 turn 边界；AgentLens 侧按 `1 Trace = 1 Turn` 生成 trace |
| `tool` | `event`, `sid`, `agent`, `tool`, `ms`, `transcript_path`, `turn_id`, `ts`, `skill`, `rule`, `cwd`, `call_id`, `message_id`, `tool_details`, `task_slug`?, `stage`?, `pt_id`? | 工具调用与耗时 |
| `usage` | `event`, `sid`, `agent`, `tool`, `tokens`, `model`, `transcript_path`, `source_offset`, `turn_id`, `message_id`, `ts`, `task_slug`?, `stage`? | transcript 增量 token / 模型 / 消息归属 |
| `stop` | `event`, `sid`, `agent`, `tokens`, `model`, `transcript_path`, `source_offset`, `turn_id`, `message_id`, `ts`, `task_slug`?, `stage`? | 会话尾部 flush |
| `error` | `event`, `sid`, `phase`, `error`, `x_traceback`, `ts` | hook 自身异常记录 |
| `stage_transition` | `event`, `sid`, `task_slug`, `stage`, `status`?, `executor`?, `retry_count`?, `review_result`?, `ts` | devflow `workflow-state.json` 某个 stage 的 status/retry_count/review_result 发生变化时触发（仅 devflow 项目） |

### 可选字段说明

- `tool.ms`: Pre/Post 未成功配对时可能为 `null`
- `tool.call_id`: 仅当工具调用存在 call id 时出现
- `tool.message_id`: 仅当成功和 transcript 中的 message 关联上时出现
- `tool.tool_details`: 仅当 transcript 中能还原出更细工具上下文时出现
- `usage.message_id`: 仅当 usage 对应的 transcript message 可识别时出现
- `usage.model`: transcript 中能识别模型名时出现
- `usage.cost_usd`: 只有模型价格命中 `config/pricing.json` 时才会出现
- `stop.cost_usd`: 只有当前 stop 事件对应 usage 能估算成本时才会出现
- `stop.cost_session_usd`: 只有 stop 汇总阶段能反算出整个 session 成本时才会出现

### 完整示例

换成更容易读的多行 JSON。下面仍然是当前 `metrics.ndjson` 里的实际数据，只是长文本字段做了截断。

`start`

```json
{
  "event": "start",
  "sid": "f6435d63-b4d5-44f2-b4d6-ae73d6c140fd",
  "agent": "main",
  "ts": 1784015331.87806
}
```

`user_prompt_submit`

```json
{
  "event": "user_prompt_submit",
  "sid": "f6435d63-b4d5-44f2-b4d6-ae73d6c140fd",
  "agent": "main",
  "turn_id": "turn-1784015342075",
  "prompt_len": 136,
  "ts": 1784015342.076438
}
```

`usage`

```json
{
  "event": "usage",
  "sid": "f6435d63-b4d5-44f2-b4d6-ae73d6c140fd",
  "agent": "main",
  "tool": "Read",
  "tokens": {
    "input": 31110,
    "output": 594,
    "cache_read": 3072,
    "total": 31704
  },
  "model": "hy3-ioa",
  "transcript_path": "/Users/rachel/.claude/projects/Users-rachel-skillhub-tokentrack-mr/f6435d63-b4d5-44f2-b4d6-ae73d6c140fd.jsonl",
  "source_offset": 17423,
  "turn_id": "turn-1784015342075",
  "message_id": "d187771c6eef4520a530564dd2a73e38",
  "ts": 1784015355.413464
}
```

`tool` 调用参数型

```json
{
  "event": "tool",
  "sid": "f6435d63-b4d5-44f2-b4d6-ae73d6c140fd",
  "agent": "main",
  "tool": "Read",
  "ms": null,
  "transcript_path": "/Users/rachel/.claude/projects/Users-rachel-skillhub-tokentrack-mr/f6435d63-b4d5-44f2-b4d6-ae73d6c140fd.jsonl",
  "turn_id": "turn-1784015342075",
  "ts": 1784015355.412617,
  "skill": [],
  "rule": [
    "global"
  ],
  "cwd": "/Users/rachel/skillhub-tokentrack-mr",
  "call_id": "chatcmpl-tool-9e50f7ec1d7e4a27",
  "message_id": "d187771c6eef4520a530564dd2a73e38",
  "tool_details": {
    "call_id": "chatcmpl-tool-9e50f7ec1d7e4a27",
    "arguments": "{\"file_path\": \"/Users/rachel/skillhub-tokentrack-mr/assets/devflow.defaults.yaml\"}",
    "arguments_display_text": "assets/devflow.defaults.yaml"
  }
}
```

`tool` 返回结果型

```json
{
  "event": "tool",
  "sid": "f6435d63-b4d5-44f2-b4d6-ae73d6c140fd",
  "agent": "main",
  "tool": "Bash",
  "ms": 759,
  "transcript_path": "/Users/rachel/.claude/projects/Users-rachel-skillhub-tokentrack-mr/f6435d63-b4d5-44f2-b4d6-ae73d6c140fd.jsonl",
  "turn_id": "turn-1784015342075",
  "ts": 1784015356.071573,
  "skill": [],
  "rule": [
    "global"
  ],
  "cwd": "/Users/rachel/skillhub-tokentrack-mr",
  "call_id": "chatcmpl-tool-898d45e0999b3e3c",
  "message_id": "93b617ceebaa49458169b89b4ecae17b",
  "tool_details": {
    "call_id": "chatcmpl-tool-898d45e0999b3e3c",
    "result_content": "Command: cd /Users/rachel/skillhub-tokentrack-mr && ls -la .claude/teams/ ...<truncated>",
    "raw_response": {
      "exitCode": 1,
      "signal": null,
      "interrupted": false,
      "sandboxDenied": false,
      "stderrBytesTruncated": 0,
      "stdoutBytesTruncated": 0,
      "tool_error_code": "8002",
      "is_error": true,
      "error": "Command: cd /Users/rachel/skillhub-tokentrack-mr && ls -la .claude/teams/ ...<truncated>"
    },
    "output_text": "Command: cd /Users/rachel/skillhub-tokentrack-mr && ls -la .claude/teams/ ...<truncated>",
    "original_message_id": "d187771c6eef4520a530564dd2a73e38",
    "next_message_id": "93b617ceebaa49458169b89b4ecae17b",
    "message_id_reassigned": true
  }
}
```

`stop`

```json
{
  "event": "stop",
  "sid": "f6435d63-b4d5-44f2-b4d6-ae73d6c140fd",
  "agent": "test-engineer",
  "tokens": {
    "input": 128290,
    "output": 665,
    "cache_read": 128192,
    "total": 128955
  },
  "model": "hy3-ioa",
  "transcript_path": "/Users/rachel/.claude/projects/Users-rachel-skillhub-tokentrack-mr/f6435d63-b4d5-44f2-b4d6-ae73d6c140fd.jsonl",
  "source_offset": 1157291,
  "turn_id": "turn-1784015342075",
  "message_id": "51e4fae9553d4d0995e7a599f3b2ef2d",
  "ts": 1784018405.467215
}
```

`error`

```json
{
  "event": "error",
  "sid": "6e8cd633-1a2f-4488-b1df-1eef168009c3",
  "phase": "post",
  "error": "AttributeError: module 'core.transcript_runtime' has no attribute 'transcript_path'",
  "x_traceback": "Traceback ...<truncated>",
  "ts": 1784183163.4314518
}
```

`stage_transition`（仅 devflow 项目；见 `core/devflow.py`）

```json
{
  "event": "stage_transition",
  "sid": "f6435d63-b4d5-44f2-b4d6-ae73d6c140fd",
  "task_slug": "fix-token-bypass_20260911_0900",
  "stage": "CODE-REVIEW",
  "status": "failed",
  "executor": "code-reviewer",
  "retry_count": 1,
  "review_result": "failed",
  "ts": 1784015412.223
}
```

### tokens

```json
{"input": 1200, "output": 180, "cache_read": 9000, "cache_creation": 0, "total": 1380}
```

- `total`: 统一按 `input + output` 计算
- `cache_read` / `cache_creation`: 保留给成本估算和缓存命中分析使用

### tool_details

`tool` 事件里的 `tool_details` 是一个可选嵌套对象，当前实现里可能包含：

- `call_id`
- `arguments`
- `arguments_display_text`
- `result_content`
- `raw_response`
- `output_text`
- `original_message_id`
- `next_message_id`
- `message_id_reassigned`

常见示例：

```json
{
  "call_id": "chatcmpl-tool-9e50f7ec1d7e4a27",
  "arguments": "{\"file_path\": \"/path/to/file\"}",
  "arguments_display_text": "path/to/file"
}
```

### AgentLens sidecar

`.state.json[sid]._agentlens` 只作为内部状态使用，核心字段：

- `enabled`: AgentLens 上报是否可用
- `last_error`: 最近一次降级原因
- `current_turn.carrier.traceparent`: 当前 turn 的 trace context
- `current_turn.subagent_spans`: 同一 turn 下的子 agent span carrier
- `current_turn.agent_spans`: 同一 turn 下按 agent 聚合的 span carrier 与累计统计
- `current_turn.step_spans`: `(agent, message_id)` 级 step span registry
- `turn_history`: 已结束 turn 的摘要

### Hook 接入

参考 `templates/settings-hook.json`，必须启用：

- `SessionStart`
- `UserPromptSubmit`
- `PreToolUse`
- `PostToolUse`
- `Stop`

### 兼容

- 旧日志若在 `tool` 事件里直接携带 `tokens` / `cost_usd`，按 legacy usage 处理。
- 不理解 `turn_id` 的下游消费者可安全忽略。

# Runtime Adapter — Team 模式派发模板

> Main Agent 作为 team lead，所有角色通过 team member 异步常驻，使用 send_message 通信。

## 运行模式

固定 `runtime_mode = "ide"`（team-async 模式）：
- `team_create` 创建团队
- `send_message` 唤醒/通信
- team member 异步常驻

## 派发原语

| 原语 | 实现 |
|------|------|
| `dispatch(role, prompt)` | `send_message(recipient=role, content=prompt, summary=...)` |
| `dispatch_parallel(jobs)` | 单条 assistant message 内 N 个 `Task(name="sub-developer-PT-XX", team_name="multi-agents-devflow-${TASK_SLUG}", subagent_name="developer", prompt=<sub-prompt>)` |
| `notify_main(event, payload)` | `send_message(recipient="main", content=payload, summary=event)` |

## 启动期

1. `team_create(team_name="multi-agents-devflow-${TASK_SLUG}")`
2. 按 `size_class` 一次性预 spawn 当前模式需要的角色（small → 仅 `solo-developer`；medium/large → 6 角色，不含 `solo-developer`；全部待命）
3. 后续阶段派发用 `send_message(recipient=<role>)` 唤醒
4. `SOLO_overflow` 升级时补 spawn 6 个 multi 角色

## 子 agent 回报

team member 完成后：
```
send_message(recipient="main", content=<JSON payload>, summary="TASK-XX_completed")
```

## 并行 sub-developer

developer 在单条 assistant message 内一次性并行派发所有 PT：
```
Task(name="sub-developer-PT-01", team_name="multi-agents-devflow-${TASK_SLUG}",
     subagent_name="developer", prompt=<sub-prompt for PT-01>)
Task(name="sub-developer-PT-02", team_name="multi-agents-devflow-${TASK_SLUG}",
     subagent_name="developer", prompt=<sub-prompt for PT-02>)
...
```

## 清理

1. 逐个 `send_message(type="shutdown_request", recipient=<member>)`
2. 等待全部 `shutdown_response(approve=true)`
3. `team_delete()`
4. 保留 `workflow-state.json` 与产物目录

## 兼容性自检（启动期一次）

| 检查项 | 要求 |
|--------|------|
| `Task` 工具可用 | ✅ 必须 |
| `team_create` 可用 | ✅ 必须 |
| `send_message` 可用 | ✅ 必须 |
| `subagent_name` 参数被支持 | ✅ 必须 |
| `workflow-state.json` 可读写于 `{artifacts_dir}/` | ✅ 必须 |
| `dispatch_parallel` 单条 message 多 Task 支持 | ✅ 必须 |

任一关键检查失败 → **显式报错并中止**，不得静默降级。

## 错误处理

| 异常 | 处理 |
|------|------|
| team member 未写 JSON 即结束 | Main 监听 inbox 超时 → 兜底读 JSON 判定 retry |
| team member 越权写 JSON 字段 | Leader 门禁审核打回 + decisions[] 记录 |
| Task 工具调用失败 | 重试 1 次 → 仍失败 → 写 last_error + 暂停 |
| `team_create` 失败 | 报错中止 |

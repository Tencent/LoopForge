---
description: 中止正在运行的 devflow 工作流
argument-hint: TASK_SLUG 或 TASK_ID
allowed-tools: ["read_file", "write_to_file", "replace_in_file", "send_message", "team_delete", "list_dir", "search_content", "execute_command"]
---

# /abort-devflow

## 用法
```
/abort-devflow {TASK_SLUG}
/abort-devflow {TASK_ID}
```

## 参数识别

- 优先尝试按 TASK_SLUG 匹配：`{artifacts_root}/{TASK_SLUG}/workflow-state.json`
- 若未匹配，按 TASK_ID 扫描 `{artifacts_root}/*/workflow-state.json`，匹配 `task_id` 字段
- 两者均未匹配 → 输出"未找到对应工作流"并终止

## Main Agent 动作（Team 模式）

1. 对所有存活 team member 逐个 `send_message(type="shutdown_request", recipient=<member>)`
2. 等待全部 `shutdown_response(approve=true)`（超时 30s 视为已退出）
3. `team_delete()`
4. 写 JSON：`summary.status = "aborted"` / `summary.aborted_at = <ISO8601>` / `summary.abort_reason = "用户手动中止"`
5. 保留 `workflow-state.json` 与已有产物目录供审计

## 输出

```
[abort] task_slug={task_slug} | stage={current_stage} | status=aborted | reason=用户手动中止
```

## 注意

- 中止后可用 `/resume-devflow` 重新续跑（从中止点恢复）
- 已产出的产物文件和 `workflow-state.json` 保留不删，供审计和恢复
- **严禁** 删除 artifacts 目录或 JSON 文件

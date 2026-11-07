---
description: 恢复中断的 devflow 工作流
argument-hint: TASK_SLUG（如 修复登录_20260513_1204）
allowed-tools: ["use_skill", "task", "team_create", "send_message", "read_file", "write_to_file", "search_content", "list_dir", "execute_command"]
---

# /resume-devflow

## 用法
```
/resume-devflow {TASK_SLUG}
/resume-devflow {TASK_ID}
```

## 参数识别

- 优先尝试按 TASK_SLUG 匹配：`{artifacts_root}/{TASK_SLUG}/workflow-state.json`
- 若未匹配，按 TASK_ID 扫描 `{artifacts_root}/*/workflow-state.json`，匹配 `task_id` 字段
- 两者均未匹配 → 输出"未找到对应工作流"并终止

## Main Agent 动作

1. **解析** TASK_SLUG 或 TASK_ID（必填，空则终止）
2. **定位状态文件**：按上述参数识别规则定位 `workflow-state.json`（不存在则终止）
3. **读取状态**：size_class / current_stage / next_target / last_event
4. **重建 Team**：`team_create(team_name="multi-agents-devflow-${TASK_SLUG}")` → 按 JSON 中 `size_class` 分流预 spawn（small → 仅 `solo-developer`；medium/large → 6 角色，不含 `solo-developer`），全部待命
5. **续跑派发**：按 Routing Table + current_stage 计算下一 target，`send_message(recipient=<target>)` 唤醒
6. **进入调度循环**：与 `/start-devflow` 一致

team member 续跑契约：先读 JSON → 产物已存在且通过自检 → 直接发完成事件；否则从断点继续。
**严禁**删除已有产物 / 重置 JSON / 重生成 TASK_SLUG。

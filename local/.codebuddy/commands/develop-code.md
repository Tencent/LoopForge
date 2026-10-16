---
description: 单独执行代码开发（spawn developer 执行 TASK-03）
argument-hint: [TASK_SLUG]
allowed-tools: ["task", "read_file", "write_to_file", "execute_command", "codebase_search", "search_content", "replace_in_file"]
---

# /develop-code

## 用法
```
/develop-code [TASK_SLUG]
```

单独执行代码开发阶段（TASK-03），不启动完整 devflow 工作流。适用于：
- 方案设计已完成，只想做代码实现
- 重新执行代码开发（如修复后重跑）

## 前置条件

- `workflow-state.json` 存在且 `TASK-02` 已完成
- `02-design/execution-plan.md` 存在
- `size_class ∈ {medium, large}`

## Main Agent 动作

### 第一步：环境检查

1. 读取 `workflow-state.json` → 校验前置条件
2. 未传 TASK_SLUG → 从 JSON 读取

### 第二步：spawn developer

```
Task(subagent_name="developer", mode="bypassPermissions",
     prompt="执行 TASK-03 代码实现。workflow_state_path={path}")
```

### 第三步：等待完成

developer 完成后产出：
- `{artifacts_dir}/03-code/change-report.md`

### 完成提示

输出：代码开发已完成，可执行 `/review-code` 进入代码审查阶段。

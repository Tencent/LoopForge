---
description: 单独执行代码审查（spawn code-reviewer 执行 CODE-REVIEW）
argument-hint: [TASK_SLUG]
allowed-tools: ["task", "read_file", "write_to_file", "execute_command", "codebase_search", "search_content"]
---

# /review-code

## 用法
```
/review-code [TASK_SLUG]
```

单独执行代码审查阶段（CODE-REVIEW），不启动完整 devflow 工作流。适用于：
- 代码开发已完成，只想做代码审查
- 对已有代码变更进行独立审查

## 前置条件

- `workflow-state.json` 存在且 `TASK-03` 已完成
- `03-code/change-report.md` 存在

## Main Agent 动作

### 第一步：环境检查

1. 读取 `workflow-state.json` → 校验前置条件
2. 未传 TASK_SLUG → 从 JSON 读取

### 第二步：spawn code-reviewer

```
Task(subagent_name="code-reviewer", mode="bypassPermissions",
     prompt="执行 CODE-REVIEW 代码审查。workflow_state_path={path}")
```

### 第三步：等待完成

code-reviewer 完成后产出：
- `{artifacts_dir}/03-code/review-report.md`
- 审查结论：PASSED / FAILED

### 完成提示

- PASSED → 输出：代码审查通过，可执行 `/run-tests` 进入测试阶段。
- FAILED → 输出：代码审查发现问题（P0: X, P1: Y），请修复后重新执行 `/review-code`。

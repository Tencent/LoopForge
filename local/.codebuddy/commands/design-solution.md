---
description: 单独执行方案设计（spawn architect 执行 TASK-02）
argument-hint: [TASK_SLUG]
allowed-tools: ["task", "read_file", "write_to_file", "execute_command", "codebase_search", "search_content"]
---

# /design-solution

## 用法
```
/design-solution [TASK_SLUG]
```

单独执行方案设计阶段（TASK-02），不启动完整 devflow 工作流。适用于：
- 需求分析已完成，只想做技术方案设计
- 重新生成 tech-design.md + execution-plan.md

## 前置条件

- `workflow-state.json` 存在且 `TASK-01` 已完成（或 `01-requirement/requirement-report.md` 存在）
- `size_class ∈ {medium, large}`（small 走 solo 路径）

## Main Agent 动作

### 第一步：环境检查

1. 读取 `workflow-state.json` → 校验前置条件
2. 未传 TASK_SLUG → 从 JSON 读取

### 第二步：spawn architect

```
Task(subagent_name="architect", mode="bypassPermissions",
     prompt="执行 TASK-02 技术方案设计。workflow_state_path={path}")
```

### 第三步：等待完成

architect 完成后产出：
- `{artifacts_dir}/02-design/tech-design.md`
- `{artifacts_dir}/02-design/execution-plan.md`

### 完成提示

输出：方案设计已完成，可执行 `/develop-code` 进入代码开发阶段。

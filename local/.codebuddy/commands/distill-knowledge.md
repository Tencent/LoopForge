---
description: 单独执行知识沉淀（spawn knowledge-engineer 执行 TASK-05）
argument-hint: [TASK_SLUG]
allowed-tools: ["task", "use_skill", "read_file", "write_to_file", "execute_command", "codebase_search", "search_content"]
---

# /distill-knowledge

## 用法
```
/distill-knowledge [TASK_SLUG]
```

单独执行知识沉淀阶段（TASK-05），不启动完整 devflow 工作流。适用于：
- 测试已完成，只想做知识沉淀
- 对已有流程产物补充知识提炼

## 前置条件

- `workflow-state.json` 存在且 TASK-02 ~ TASK-04 + CODE-REVIEW 已完成
- 各阶段产物文件存在

## Main Agent 动作

### 第一步：环境检查

1. 读取 `workflow-state.json` → 校验前置条件
2. 未传 TASK_SLUG → 从 JSON 读取

### 第二步：spawn knowledge-engineer

```
Task(subagent_name="knowledge-engineer", mode="bypassPermissions",
     prompt="执行 TASK-05 知识沉淀。workflow_state_path={path}")
```

### 第三步：等待完成

knowledge-engineer 完成后产出：
- `{ARTIFACTS_ROOT}/knowledge/{task_slug}.md`（全局知识文件）

### 完成提示

输出：知识沉淀已完成。全部阶段执行完毕，可用 `/status-devflow` 查看工作流状态。

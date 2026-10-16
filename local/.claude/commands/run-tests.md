---
description: "单独执行测试阶段（spawn test-engineer 根据变更补充并运行必要测试）"
argument-hint: "[TASK_SLUG]"
---

# /run-tests

## 用法
```
/run-tests [TASK_SLUG]
```

单独执行测试阶段（TASK-04），不启动完整 devflow 工作流。test-engineer 会根据实际变更和项目已有测试体系选择单元、集成/API 或 E2E 测试。

适用于：
- 代码审查已完成，只想补充和执行必要测试
- 重新执行测试生成与验证

## 前置条件

- `workflow-state.json` 存在且 `CODE-REVIEW` 已完成（PASSED）
- `03-code/change-report.md` 存在

## Main Agent 动作

### 第一步：环境检查

1. 读取 `workflow-state.json` → 校验前置条件
2. 未传 TASK_SLUG → 从 JSON 读取

### 第二步：spawn test-engineer

```
Agent(subagent_type="devflow-test-engineer",
     prompt="执行 TASK-04：根据实际变更和项目现有测试体系补充、执行并报告必要测试。workflow_state_path={path}")
```

### 第三步：等待完成

test-engineer 完成后产出：
- `{artifacts_dir}/04-e2e/test-report.md`
- `{artifacts_dir}/04-e2e/added-cases.md`

### 完成提示

输出：测试阶段已完成，可执行 `/distill-knowledge` 进入知识沉淀阶段。

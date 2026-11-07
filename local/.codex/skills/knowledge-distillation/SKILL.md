---
name: knowledge-distillation
description: 通用知识沉淀能力。用于从 devflow 或其他交付过程的需求、设计、实现、审查、测试产物中提炼可复用经验、技术决策、踩坑记录和流程改进。
---

# Knowledge Distillation

本 skill 提供知识沉淀方法论。它只定义如何收集、筛选、组织和输出知识点；
具体输入产物与输出路径由调用方指定。

## DevFlow Override

当本 skill 由 Codex devflow 的 TASK-05 `knowledge-engineer` 调用时：

- 必须读取 `workflow-state.json` 中的 `task_slug`、`artifacts_dir` 和各阶段
  `artifact_path`。
- 必须读取 TASK-01、TASK-02、TASK-03、CODE-REVIEW、TASK-04 的有效产物；
  若任一必需上游阶段未完成或产物缺失，TASK-05 应失败并写入 `last_error`。
- 输出路径固定为 `{ARTIFACTS_ROOT}/knowledge/{task_slug}.md`，其中
  `ARTIFACTS_ROOT` 来自 `.codex/assets/devflow.defaults.yaml` 的
  `artifacts.root_dir`。
- 只允许追加，不允许删除、覆盖或改写已有知识条目。
- 如果同一 `task_slug` 的知识文件已存在，应先做标题和主题去重；相似条目合并为
  新子条目，不重复创建同义章节。
- 完成后由 `knowledge-engineer` 更新 `workflow-state.json`：
  `stages.TASK-05.status = "completed"`、`artifact_path` 指向全局知识文件、
  `last_event = "TASK-05_completed"`、`next_target = "leader"`。

## When To Use

- 需求或项目交付完成后，提炼可复用经验。
- 技术攻关后，记录关键决策和踩坑经验。
- 代码改造后，沉淀可复用的改造模式。
- 流程复盘时，记录后续 devflow 可改进点。

## Step 1: Collect Inputs

优先从结构化产物中提取信息：

| 来源 | 关注要点 |
|------|----------|
| 需求分析 | 关键约束、澄清过的歧义、纳入/排除范围 |
| 技术方案 | 架构决策、取舍理由、风险控制 |
| 执行计划 | 任务拆分、文件边界、并行协作约束 |
| 代码实现 | 可复用改造模式、关键实现点、实际变更边界 |
| 代码审查 | P0/P1 问题、门禁经验、质量规则 |
| 测试报告 | 有效测试方法、无需测试的判定依据、环境限制 |

## Step 2: Filter And Distill

每条知识点必须满足：

| 标准 | 要求 | 反例 |
|------|------|------|
| 可复用 | 后续类似场景能直接参考 | 本次任务创建时间是某日某时 |
| 具体明确 | 包含模块、文件、函数、配置项或流程节点 | 某个地方要注意 |
| 结论导向 | 写清建议做法和原因 | 复述完整讨论过程 |
| 精简 | 每条 1-3 句 | 大段复制原始报告 |

宁可少写，也不要为凑数写低价值条目。

## Step 3: Categorize

按需使用以下类别；没有价值的类别可省略：

- 业务知识：业务规则、模块职责、数据流向。
- 技术决策：架构选型、方案取舍及理由。
- 改造模式：可复用的代码或文档改造方式。
- 踩坑记录：易错点、隐含约束、环境问题。
- 流程改进：devflow、评审、测试或协作流程的改进建议。

## Output Template

```markdown
---
task_id: {task_id}
stage: TASK-05
author: 知识沉淀师
date: {YYYY-MM-DD}
run_mode: {auto/manual}
---

## {YYYY-MM-DD} | {task_slug} | {title}

### 技术决策
- ...

### 改造模式
- ...

### 踩坑记录
- ...

### 流程改进
- ...
```

如果本次交付没有新的可复用知识，也要追加一条简短记录说明“不产生新知识点”
的依据，避免 TASK-05 看起来像被跳过。

## Write Rules

1. 追加不覆盖：不得删除或改写已有条目。
2. 幂等安全：同一任务重复沉淀时先检查已有章节，避免重复。
3. 可追溯：保留 YAML front matter 或追加章节中的日期、任务 slug。
4. 不越权：不修改需求、设计、代码、审查、测试等阶段产物。

## Quality Check

完成前检查：

- 每条知识点是否可复用、具体、结论导向、精简。
- 是否遗漏了关键决策理由或明确踩坑。
- 是否含足够上下文，方便不了解本次任务的人理解。
- 是否避免了大段复制原始产物。
- 是否只写入调用方指定的知识文件。

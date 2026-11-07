---
name: devflow-codex
description: 在 Codex 中运行 devflow。用户提到 devflow、start/resume/status/abort devflow、多 agent 工作流，或要求“按 Codex devflow 跑”时使用。
---

# DevFlow Codex 工作流

这是 Codex 原生工作流入口。Codex 运行时只读取 `.codex/`，不读取
`.codebuddy/`。

`.codex/` 与 `.codebuddy/` 是两套独立工作流资产；二者应保持流程契约一致，
但互不作为运行时依赖。

## 必读文件

启动或恢复前必须读取：

1. `.codex/assets/devflow.defaults.yaml`
2. `.codex/runtime/start-workflow.md`
3. `.codex/runtime/workflow-state-spec.md`
4. TASK-01 需求澄清时读取 `.codex/skills/superpowers/brainstorming/SKILL.md`
5. TASK-05 知识沉淀时读取 `.codex/skills/knowledge-distillation/SKILL.md`
6. 当前阶段对应的 `.codex/agents/<role>.md`
7. 当前阶段需要的 `.codex/rules/` 和 `.codex/checklists/`

## 在 Codex 中如何触发

Codex 不会自动把仓库里的命令文件注册进 `/` 菜单。请用自然语言或显式
skill 触发：

```text
$devflow-codex start 需求描述...
$devflow-codex status TASK_SLUG
$devflow-codex resume TASK_SLUG
$devflow-codex abort TASK_SLUG
```

也可以直接说：

```text
用 devflow 跑这个需求：...
```

## 编排规则

- 主 Codex 线程是唯一 orchestrator。
- `workflow-state.json` 是阶段交接和审计的唯一持久化状态。
- 不使用 CodeBuddy 的 `team_create`、`send_message`、`team_delete`。
- medium/large 的 TASK-01 必须使用 `superpowers:brainstorming` 做需求澄清，
  且必须等待用户明确回答或确认后才能进入 TASK-02。
- TASK-02 及之后的角色阶段必须通过 Codex `multi_agent_v1.spawn_agent`
  派发；只有工具不存在或返回 unavailable/not found 时，才允许主线程内联执行
  并在 `workflow-state.json.decisions[]` 记录降级原因。
- 阶段产物和状态 schema 必须保持 devflow 兼容。

## 启动协议

按 `.codex/runtime/start-workflow.md` 执行：

1. 初始化 `artifacts/{task_slug}/` 和 `workflow-state.json`。
2. Phase 0 做需求草图、影响扫描和规模判定。
3. small 进入 SOLO。
4. medium/large 先由主 Codex 线程调用 `superpowers:brainstorming` 完成
   TASK-01 需求分析与澄清，写入 `01-requirement/requirement-report.md`，
   经用户确认后再进入 TASK-02。
5. TASK-02 起使用 `multi_agent_v1.spawn_agent` 派发对应角色。
6. 如果用户要求先确认方案，停在 TASK-03 前。

## 阶段协议

每个阶段：

1. 先读取 `workflow-state.json`。
2. 读取 `.codex/agents/<role>.md`。
3. 写入阶段开始状态。
4. 产出对应 artifact。
5. 更新 `workflow-state.json`。
6. 返回主线程，由主线程按 routing table 决定下一阶段。

## 完成协议

结束时生成 `workflow-summary.md`，更新 summary 字段，保留全部产物，并向用户
汇总改动、验证、审查和知识沉淀结果。

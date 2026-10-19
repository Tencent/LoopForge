# Codex DevFlow 使用说明

`.codex/` 存放 Codex 原生 devflow 工作流资产。Codex 运行时只读取
本目录，不依赖 `.codebuddy/`。

## 如何启动

在 Codex 中用自然语言或 skill 名称触发：

```text
$devflow-codex start <需求描述>
```

等价示例：

```text
用 devflow 跑这个需求：...
按 Codex devflow 执行：...
$devflow-codex status <task_slug>
$devflow-codex resume <task_slug>
$devflow-codex abort <task_slug>
```

Codex 不会自动把仓库里的 slash command 注册到 `/` 菜单，所以请使用 skill 名称
或自然语言触发。

## 运行边界

Codex devflow 运行期只读取以下本地资产：

- `.codex/skills/devflow-codex/SKILL.md`
- `.codex/skills/superpowers/brainstorming/SKILL.md`
- `.codex/skills/knowledge-distillation/SKILL.md`
- `.codex/assets/devflow.defaults.yaml`
- `.codex/assets/workflow-state-template.json`
- `.codex/runtime/start-workflow.md`
- `.codex/runtime/workflow-state-spec.md`
- `.codex/agents/*.md`
- `.codex/rules/*.mdc`
- `.codex/checklists/*.md`

`.codebuddy/` 是 CodeBuddy 的运行资产副本。两边可以保持流程契约一致，但 Codex
执行过程中不要读取 `.codebuddy/` 作为运行时来源。

## Skill 发现入口

仓库内 skill 通过 `.agents/skills/` 暴露给 Codex：

```text
.agents/skills/devflow-codex -> ../../.codex/skills/devflow-codex
.agents/skills/superpowers-brainstorming -> ../../.codex/skills/superpowers/brainstorming
.agents/skills/knowledge-distillation -> ../../.codex/skills/knowledge-distillation
```

如果当前 Codex 会话没有动态发现新加的 skill，相关 agent 文档中会 fallback 到
本地 `.codex/skills/.../SKILL.md` 读取执行。

## 工作流状态

每次运行会创建：

```text
artifacts/{task_slug}/
  workflow-state.json
  workflow-summary.md
  01-requirement/
  02-design/
  03-code/
  04-e2e/
  05-knowledge/
  01-solo/
```

`workflow-state.json` 是唯一持久化交接状态。每个阶段开始前必须读取，完成后必须
写回。阶段完成时至少更新：

- 自己负责的 `stages.<stage>` 字段
- `current_stage`
- `last_event`
- `next_target`
- `updated_at`

`current_stage` 保留为刚完成的阶段；下一步路由通过 `last_event` 和
`next_target` 表示。

## 阶段路由

小需求走 solo 路径：

```text
PHASE-0 -> SOLO -> workflow_completed
```

中/大需求走多 agent 路径：

```text
PHASE-0
  -> TASK-01 main 需求澄清
  -> TASK-02 architect 技术方案
  -> TASK-03 developer 代码实现
  -> CODE-REVIEW code-reviewer 代码审查
  -> TASK-04 test-engineer E2E 验证
  -> TASK-05 knowledge-engineer 知识沉淀
  -> leader 最终汇总
  -> workflow_completed
```

TASK-02 及之后必须在工具可用时通过 `multi_agent_v1.spawn_agent` 派发。只有
spawn 工具不存在，或返回 unavailable/not found 类错误时，才允许主线程内联降级，
并且必须在 `workflow-state.json.decisions[]` 中记录原因。

## 必需 Skills

- `devflow-codex`：主工作流入口。
- `superpowers:brainstorming`：中/大需求的 TASK-01 需求澄清。
- `knowledge-distillation`：TASK-05 可复用知识沉淀。

TASK-01 使用 brainstorming skill 中的 DevFlow Override，输出到：

```text
artifacts/{task_slug}/01-requirement/requirement-report.md
```

中/大需求的 TASK-01 不能自动跳过。即使需求看起来已经足够明确，也必须向用户
发起一次澄清或确认，并等待明确回复后，才能写入 `TASK-01_completed` 和进入
TASK-02。

TASK-05 使用 knowledge-distillation skill 中的 DevFlow Override，输出到：

```text
artifacts/knowledge/{task_slug}.md
```

## 状态查看和恢复

状态查看只读，不修改文件：

```text
$devflow-codex status <task_slug>
```

恢复运行会读取 `artifacts/{task_slug}/workflow-state.json`，保留已有产物，并从
最近未完成或失败的阶段继续：

```text
$devflow-codex resume <task_slug>
```

恢复时不要删除已有产物，也不要重置 `task_slug`。

## 重要规则

- 不要调用 CodeBuddy 专属原语，例如 `team_create`、`send_message`、
  `team_delete`。
- 不要把 `.codebuddy/` 当作 Codex 运行时来源。
- 不要修改已应用且按项目约定不可变的数据库迁移文件。
- TASK-03 不要把正式产物写入 `04-e2e/` 或 `05-knowledge/`。
- 中/大需求不要跳过 TASK-01，也不要以“无需追加澄清”为由自动进入 TASK-02。
- 如果 knowledge-distillation 指令不可用，不要手写 TASK-05 知识沉淀冒充通过；
  应标记阶段失败并写入 `last_error`。

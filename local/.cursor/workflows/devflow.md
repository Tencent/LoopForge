# Cursor DevFlow

`.cursor/` 是 Cursor 独立工作流资产，不是 `.cursor/` 的符号链接或运行时代理。

## 资产边界

Cursor devflow 只读取：

- `.cursor/assets/devflow.defaults.yaml`
- `.cursor/runtime/start-workflow.md`
- `.cursor/runtime/workflow-state-spec.md`
- `.cursor/skills/superpowers/brainstorming/SKILL.md`
- `.cursor/agents/*.md`
- `.cursor/agents/devflow-*.toml`
- `.cursor/rules/*.mdc`
- `.cursor/checklists/*.md`
- `.cursor/skills/devflow-codex/SKILL.md`

`.cursor/` 保留给 CodeBuddy 使用。Cursor 工作流不得在运行时读取
`.cursor/` 的 agents、rules、runtime、commands、hooks 或 settings。

medium/large 需求必须先由主 Cursor 线程执行 TASK-01：读取并使用
`superpowers:brainstorming` 做需求分析与澄清，产物写入
`artifacts/{task_slug}/01-requirement/requirement-report.md`，再进入 TASK-02。
small 路径进入 SOLO，不执行 TASK-01。

## Cursor 适配差异

- Cursor 不注册仓库内 `slash start-devflow` 命令。
- 用 `$devflow-codex start ...` 或自然语言触发。
- 主 Cursor 线程负责 orchestration。
- TASK-02 及之后必须使用 Cursor `Task` 派发角色 agent。
- 不使用 `team_create`、`send_message`、`team_delete`。
- `workflow-state.json` 是阶段交接、恢复和审计的唯一状态。

## 恢复检查

新线程无法识别 skill 时，检查：

1. `.agents/skills/devflow-codex` 是否指向 `../../.cursor/skills/devflow-codex`
2. `.agents/skills/superpowers-brainstorming` 是否指向 `../../.cursor/skills/superpowers/brainstorming`
3. `.cursor/skills/devflow-codex/SKILL.md` 是否存在且权限为 `644`
4. `.cursor/skills/superpowers/brainstorming/SKILL.md` 是否存在
5. `.cursor` 是否为真实目录，而不是指向 `.cursor` 的符号链接

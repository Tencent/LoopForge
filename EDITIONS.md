# DevFlow Editions

DevFlow 有两套独立维护的实现。为避免同一宿主同时加载两套工作流，以及状态和
产物相互冲突，安装器在每个项目中只允许为同一宿主启用一种 edition；需要切换时
使用 `install --force`。这里的区分是发布合同，不是额外复制一层运行文件。

| Edition | 仓库事实源 | 入口 | 维护方式 |
| --- | --- | --- | --- |
| Portable | `skills/` | CodeBuddy/Cursor/Claude 使用 `/devflow`，Codex 使用 `$devflow`，Pi 使用 `/skill:devflow`，OpenCode 要求使用 `devflow` Skill | 修改 canonical Skill 和 adapter manifest |
| Classic（默认） | `.codebuddy/`、`.codex/`、`.cursor/`、`.claude/` | CodeBuddy/Cursor/Claude 使用 `/start-devflow`，Codex 使用 `$devflow-codex` | 以 `.codebuddy` 为参考行为；Cursor/Claude 由生成器产生 |

保留这些顶层目录是有意为之：`.xxx/` 是各宿主的原生项目目录，`skills/` 是
Agent Skills 的标准目录。如果移动到 `editions/classic/` 和
`editions/portable/`，直接打开仓库时宿主将无法按原生约定发现它们。

## 安装前确认

```bash
loopforge editions
loopforge plan claude
loopforge plan claude --edition portable
loopforge skills install claude
```

`plan` 只读取安装资产，不修改目标项目。实际安装后，edition 和每个托管文件的
摘要记录在项目的 `.devflow/install-state.json` 中。

Portable 的 Pi 适配是声明型的：`skills install pi` 只安装 `.pi/skills/`，isolated
（medium/large）模式还需要公开扩展 `pi install npm:pi-subagents` 提供 `subagent` 工具。
OpenCode 也是 Portable-only：`skills install opencode` 安装 `.opencode/skills/` 和两个
项目级 subagent，isolated 模式要求当前会话提供 `task` 工具。

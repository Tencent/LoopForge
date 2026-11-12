# NLForge Local: Classic / Portable

NLForge Local 内置两套工作流，源码位于 `local/`。为避免同一宿主同时加载两套工作流，以及状态和
产物相互冲突，安装器在每个项目中只允许为同一宿主启用一种 edition；需要切换时
使用 `install --force`。这里的区分是发布合同，不是额外复制一层运行文件。

| Edition | 仓库事实源 | 入口 | 维护方式 |
| --- | --- | --- | --- |
| Portable | `skills/` | CodeBuddy/Cursor/Claude/DeepSeek Harness/Hermes 使用 `/devflow`，Codex 使用 `$devflow`，Pi 使用 `/skill:devflow` | 修改 canonical Skill 和 adapter manifest |
| Classic（默认） | `.codebuddy/`、`.codex/`、`.cursor/`、`.claude/` | CodeBuddy/Cursor/Claude 使用 `/start-devflow`，Codex 使用 `$devflow-codex` | 以 `.codebuddy` 为参考行为；Cursor/Claude 由生成器产生 |

表中路径均相对于 `local/`。安装器将对应资产安装到目标项目的宿主原生目录，
例如 `.codebuddy/` 或 `.agents/skills/`。请在需要使用工作流的目标项目中执行安装命令；
克隆 LoopForge 平台仓库本身不会自动启用本地工作流。

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

DeepSeek Harness 使用 `skills install dsh` 安装到 `.dsh/skills/`。DSH base profile 默认提供
`subagent` 工具；如果当前 profile 没有该工具，插件会记录具体原因并将 medium/large 流程降级为
`single-context`，不会伪造 isolated 执行器。

Hermes Agent 使用 `skills install hermes` 安装到项目的 `.hermes/skills/`。Hermes 首次发现项目技能时
要求显式信任仓库：在项目根目录运行 `hermes skills trust .`。medium/large 阶段使用 Hermes 的
`delegate_task` 创建独立子 Agent；当前会话缺少该工具时必须记录原因并降级为 `single-context`。
如需独立查看状态、产物和审批，可选安装 `local/hermes-plugin/`；插件只调用项目内的
`workflow_state.py`，不会覆盖 Hermes profile 配置。

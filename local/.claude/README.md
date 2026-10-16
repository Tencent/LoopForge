# DevFlow Classic for Claude Code

本目录是由 `.codebuddy` 参考工作流和 Classic 公共契约生成的 Claude Code 宿主包。
它属于 Classic edition，不依赖 `skills/devflow` Portable edition。不要直接修改；
修改参考工作流或适配规则后运行 `python3 scripts/build-classic-hosts.py --write`。

## 常用命令

- 启动：`/start-devflow <需求>`
- 恢复：`/resume-devflow <task_slug>`
- 状态：`/status-devflow <task_slug>`
- 终止：`/abort-devflow <task_slug>`

单阶段命令见 `commands/`，状态和产物协议见 `runtime/`。

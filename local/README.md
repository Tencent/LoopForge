# LoopForge Local

LoopForge 的本地工作流组件，在 CodeBuddy、Codex、Cursor、Claude Code 或 Pi 中使用，无需启动平台。

Local workflows for LoopForge, running inside an existing coding assistant without the platform server.

```bash
# Run in the project where you want to install the workflow.
# 在需要使用工作流的目标项目中运行。
npx -y loopforge-cli@latest install claude
npx -y loopforge-cli@latest status claude
```

Start with `/start-devflow "<request>"` in Claude Code. Replace `claude` with `codebuddy`, `codex`, or `cursor` when installing for another host; the guides list each host's entry point. Pi uses the Portable edition.

在 Claude Code 中通过 `/start-devflow "<需求>"` 启动。其他宿主的安装命令、入口及 Portable 用法见统一文档。

- [使用指南](https://github.com/Tencent/LoopForge/blob/main/docs/local/usage.zh-CN.md) · [Usage guide](https://github.com/Tencent/LoopForge/blob/main/docs/local/usage.md)
- [Classic / Portable](https://github.com/Tencent/LoopForge/blob/main/docs/local/editions.md)
- [项目首页 / Project](https://github.com/Tencent/LoopForge) · [贡献 / Contributing](https://github.com/Tencent/LoopForge/blob/main/CONTRIBUTING.md)

当前运行内置的本地工作流，平台 SOP 导入与独立执行属于后续能力。已有包名 `loopforge-cli`、`devflow-workflow` 和 `loopforge` 命令保持兼容。

Platform SOP import and standalone execution are planned. Existing package names and commands remain compatible.

Third-party notices are maintained at the repository root. Packaging generates a local notice and includes it with `LICENSE` in npm, Python and release archives. Project policies and release history are shared at the repository root.

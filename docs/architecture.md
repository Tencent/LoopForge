# Architecture

仓库包含两个明确分离的 edition 和一个统一安装控制面。

## Classic Edition

`.codebuddy/` 是参考行为和主要人工维护面；`.codex/` 是已有 Codex 原生适配；
`.cursor/` 与 `.claude/` 由 `build-classic-hosts.py` 生成。四个目录都是完整宿主包，
不读取 `skills/devflow` 作为运行时来源。

## Portable Edition

`skills/` 提供独立的新 DevFlow 状态机、模板、规则、测试和 adapter，
支持 CodeBuddy、Codex、Cursor、Claude Code、Pi 与 OpenCode（Pi 只有声明型 adapter，
isolated 模式依赖公开扩展 `npm:pi-subagents`）。脚本只依赖 Python 3.8+
标准库；具体 Agent 调度能力由宿主提供。

`skills/manifest.json` 是跨宿主 Skill 集合清单。`skills/` 是这一层的唯一
事实源。逻辑角色、产物、规则和宿主能力分别由
对应 manifest 管理；Cursor 与 Claude Code 共用的 Portable 执行器正文只保留一份。
Portable 安装到目标项目时才生成 `.cursor/.claude/.agents/.codebuddy` 内容，不能
反向覆盖仓库内的 Classic 包。

安装到其他项目时优先创建相对软链接。不能使用软链接的平台可加
`--copy-skills`；安装器会记录自己管理的副本，之后只有显式
`--refresh-managed` 才覆盖这些副本，不碰项目自有 Skill。

## 统一安装控制面

`DevFlow` CLI 负责 install、status、update、uninstall 和 doctor。四个 target
保持薄层，安装核心统一处理计划、冲突检查、状态记录和用户文件保护。默认 edition
是 Classic；`loopforge install <host>` 安装完整宿主包，Portable 通过
`loopforge skills install <host>` 安装。安装器不改变两套工作流的内部状态机。

## 可选工具与私有扩展

`tools/` 存放不进入 Classic/Portable 安装包和 `loopforge-cli` npm 包的配套工具。
当前 `tools/tcmcp` 是腾讯云只读 MCP Initializr，由克隆仓库或 GHCR 镜像单独运行。

CI/CD、工单、企业代码托管、知识库、聊天通知，以及未开源的云运维能力，应保存在部署方的
私有仓库，通过本地配置或受控安装叠加。不要把 CAM 策略、内网域名或运维 MCP 写进
Classic/Portable 工作流模板。

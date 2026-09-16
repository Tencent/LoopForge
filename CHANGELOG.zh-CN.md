# Changelog

[English](CHANGELOG.md) | 简体中文

## Unreleased

- 新增 `tools/tcmcp`：腾讯云只读 MCP Initializr，与 `loopforge install` 分开文档化与运行。LoopForge 版本 tag 同时发布 `ghcr.io/tencent/loopforge-tcmcp`。

## 0.2.0

- 为 CodeBuddy、Cursor 和 Claude Code 的 Classic DevFlow 流程新增可选的本地可观测性看板。
- 新增工具耗时、transcript token 用量、会话成本、Agent 归属和 DevFlow 阶段进度等本地指标。
- 支持为内置价格表未覆盖的模型配置自定义定价。
- 修正轮次与耗时聚合、派发归属、会话计数以及中大型流程分类等看板统计口径。
- 修正 Classic 生成包校验，避免将 Python 缓存文件误报为未托管内容。
- 支持基于已有版本 tag 重新触发发布工作流。

## 0.1.0

- 重写中英文 README，突出项目价值、快速开始、工作流和 Edition 选择。
- 将首个公开版本升级为稳定版，并统一 npm、Python、CLI 和发布验证版本。
- Classic small 流程改为只创建 solo-developer 并由其直接完成最终汇总。
- 测试阶段改为根据目标项目现有体系选择单元、集成/API 或 E2E 测试。
- 发布 CodeBuddy、Codex 和跨宿主 DevFlow Skills。
- 移除组织内部运维、部署和 MCP 配置，保留原工作流权限语义并说明风险。
- 增加安全策略、贡献指南、第三方声明与自动验证。
- 保留 Superpowers 的原生文档路径，并为 DevFlow 模式提供独立产物覆盖规则。
- 增加 Cursor、Claude Code 仓库级投影、共享执行器事实源和 CI drift 检测。
- 支持可追踪的 Skill 复制安装与安全刷新，补齐 Cursor/Claude 安装冒烟测试。
- 明确区分 Classic 与 Portable 两个 edition，新增完整 Classic Cursor/Claude 宿主包。
- 增加可通过 pipx 安装的统一 `loopforge` CLI，支持四宿主的安装、状态、更新、卸载和诊断。
- 增加与原 Python CLI 共用安装核心的 npm/npx 入口，支持临时执行和 npm 全局安装。
- 增加统一 edition 注册表以及只读的 `loopforge editions`、`loopforge plan`，使两套事实源、入口和安装内容可发现并可校验。
- `loopforge install <host>` 默认安装 Classic，Portable 使用易识别的 `loopforge skills install <host>` 命令组。
- 增加显式 `--force` 覆盖安装，默认仍在用户文件或软链接冲突时停止。
- `install --force` 支持在同一宿主上直接切换 Classic 与 Portable edition。
- CodeBuddy Portable 的阶段执行器与调研助手统一启用自动运行和 `bypassPermissions`。

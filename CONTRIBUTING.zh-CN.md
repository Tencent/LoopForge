# Contributing

[English](CONTRIBUTING.md) | 简体中文

感谢参与 LoopForge。

## 开发流程

1. 从功能分支开始修改。
2. Classic/Portable 工作流模板保持通用，不要在其中写入特定组织、仓库或云资源信息。`tools/` 下的产品工具（如 `tools/tcmcp`）可以面向公有云产品，但不得包含内网域名、真实资源 ID 或凭据。
3. 不提交任何凭据、本地权限配置或生产数据。
4. 先标明改动属于 Classic、Portable 或共享行为合同。Portable 只修改 `skills/`；Classic 的 `.cursor/.claude` 不直接修改，运行 `build-classic-hosts.py --write`。
5. 修改共享工作流契约时分别说明两个 edition 的影响；不要在二者之间复制运行文件。
6. 提交前运行 `bash scripts/validate.sh` 和 `bash scripts/smoke-install.sh`。

## Pull Request 要求

- 说明改动解决的问题和适用运行时。
- 列出行为变化、兼容性影响和验证结果。
- 新增第三方内容时补充来源、许可证和修改说明。
- 不要把内部部署或运维能力放进 Classic/Portable 模板；未开源的运维能力使用单独的私有扩展。面向公开用户的可选工具放在 `tools/`，不要打进宿主工作流包。

提交信息建议遵循 Conventional Commits，例如 `feat: add workflow checkpoint validation`。

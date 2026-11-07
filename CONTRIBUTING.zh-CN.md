# 参与贡献

平台代码位于仓库根目录，原本地工作流版本位于 `local/`。开始修改前请阅读对应的 `AGENTS.md`。

平台改动应保留上游 engine、repo 与 handler 的分层；身份、外部提供商和部署差异应通过明确适配实现。修复行为问题时附带回归测试，说明 schema 与 runner 协议兼容性。

```bash
go test -p=2 ./... -count=1 -timeout=180s
go vet ./...
python3 scripts/check-public.py
bash scripts/scan-secrets.sh
```

社区贡献不需要私有来源清单，也不需要更新文件哈希。维护者在本地保存来源与同步审计记录，公共 CI 不依赖这些资料。长期流程见[维护策略](docs/upstream-sync.md)。

登录、语言/主题或执行链路的改动需要跑[公共浏览器验收](e2e/public/README.md)。真实模型与外部知识库测试需显式配置；请区分通过、失败、跳过与未运行。

LoopForge Local 请在 `local/` 内修改，遵循[本地开发约定](local/AGENTS.md)。先明确改动属于 Classic、Portable 或共享行为合同；三者与平台运行资产保持清晰边界。Portable 修改 `local/skills/`；Classic 的 Cursor/Claude 包在 `local/` 内运行 `python3 scripts/build-classic-hosts.py --write` 生成。工作流模板保持通用，可选工具放在 `local/tools/`，单独分发。

在 `local/` 内执行本地版检查：

```bash
python3 scripts/build-classic-hosts.py --check
bash scripts/validate.sh
bash scripts/smoke-install.sh
bash scripts/smoke-npm.sh
bash scripts/smoke-curl-install.sh
bash scripts/e2e.sh
bash scripts/test-observability.sh
bash scripts/scan-secrets.sh
```

[本地 E2E](local/tests/e2e/README.md) 使用模拟宿主与录制产物，无需模型凭据。分别说明对各工作流的影响，保留既有权限语义和用户文件保护；提交信息遵循 Conventional Commits。

PR 不应包含凭据、内部基础设施、员工权限默认值或生产数据。保留用户文件和第三方许可，并记录贡献来源。PR 说明应写清问题、最终行为和实际验证结果。

# Agent Compatibility and Verification

## 支持面

| 宿主 | Classic | Portable |
| --- | --- | --- | --- |
| CodeBuddy | `.codebuddy/` 完整包 | `.codebuddy/skills/devflow` + Team adapter |
| Codex | `.codex/` + `.agents/skills` | `.agents/skills/devflow` |
| Cursor | `.cursor/` 完整包 | `.cursor/skills` + 两个执行器 |
| Claude Code | `.claude/` 完整包 | `.claude/skills` + 两个执行器 |
| Pi | 无（Portable only） | `.pi/skills` + `subagent` adapter（需 `npm:pi-subagents`） |

Classic 的 Cursor/Claude 目录是生成产物；Portable 的事实只保存在 `skills/`。
两者只共享显式行为合同和测试，不共享宿主运行文件。

## 验证分层

1. **结构门禁**：校验 JSON/YAML、Skill frontmatter、manifest 引用和链接。
2. **漂移门禁**：`build-classic-hosts.py --check` 逐字比较 Classic 生成包。
3. **安装门禁**：统一 CLI 覆盖 5 宿主 × 2 edition（pi 仅 Portable），并验证重复安装、更新、卸载、
   mixed-edition 拒绝和用户文件保护。
4. **流程回归**：标准库单测和 CLI E2E 验证状态机、门禁、产物与恢复协议。
5. **真实宿主 canary**：发布前或宿主大版本升级后，在隔离 fixture 仓库用真实
   Cursor/Claude/Codex/CodeBuddy CLI 各跑一次 small 和一次 medium 流程，保存宿主
   版本、调用轨迹、最终状态和 diff。此层需要各宿主凭据和运行环境，不应伪装成
   无凭据的普通单测。

前四层在普通 PR CI 中运行。第五层建议使用受保护的手动或定时流水线；任何宿主
变更应先更新 manifest 的核对信息，再运行 canary。宿主 canary 失败时不能只更新
快照，应先判断是字段协议变化、工具缺失、权限变化还是模型行为回归。

## 更新步骤

1. 先判断变更属于 Classic、Portable 或共享行为合同。
2. Classic 变更运行 `python3 scripts/build-classic-hosts.py --write`；Portable 只修改 `skills/`。
3. 运行 `bash scripts/validate.sh`、`bash scripts/smoke-install.sh` 和 secret scan。
4. 若改动涉及宿主字段、调度或生命周期，核对官方文档并执行真实宿主 canary。
5. PR 中列出每个宿主的 `passed`、`failed`、`blocked` 或 `not-run`，不要用一个
   宿主的通过结果代表全部宿主。

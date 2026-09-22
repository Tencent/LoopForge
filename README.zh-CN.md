# LoopForge

[English](README.md) | 简体中文

**把 Codex、Claude Code、Cursor 和 CodeBuddy 变成一支可恢复的 AI 开发团队。**

规划 → 实现 → 审查 → 测试。LoopForge 为 Coding Agent 提供一套可确认、可检查、可恢复的交付流程，而不只是另一段提示词。

![LoopForge 本地观测面板，展示 Agent 活动、Token 成本、工具使用和工作流阶段](docs/assets/agent-observability-dashboard.png)

_看板示例使用合成演示数据。_

<p align="center">
  <strong>本地优先</strong> · <strong>厂商中立</strong> · <strong>可恢复</strong> · <strong>以证据交付</strong>
</p>

## 为什么使用 LoopForge

长任务往往以相似的方式失败：目标还没澄清就开始编码、修改超出范围、Agent 自己审查自己、跨会话丢失上下文，或者只留下代码差异，却没有证据说明它真的可用。

LoopForge 把一句需求变成可检查的交付记录：

- **编码前确认**：澄清目标、范围、排除项和验收标准；
- **职责分离**：设计、实现、审查和测试由独立角色承担；
- **中断后继续**：持久化工作流状态，从第一个未完成阶段恢复；
- **保留证据**：保存需求、决策、审查发现、测试结果和交付总结；
- **看见 Agent 的工作过程**：本地查看会话、Token/成本、工具、Skills、Rules 和阶段归因。

## 从一句需求到可验收结果

1. **先理解问题**：Agent 调研现有项目，澄清目标、范围和验收标准，而不是立即开始写代码。
2. **确认边界后再实施**：中大型任务会在你确认目标、范围、排除项、关键决策和验收标准后，再进入设计与实现。
3. **分工完成交付**：设计、实现、审查和测试由职责独立的 Agent 分阶段完成，减少同一上下文中的自审偏差。
4. **用证据说明结果**：需求、方案、代码变更、审查意见和测试结果都会落盘，方便检查、复盘和交接。
5. **中断后继续执行**：流程状态会被保存，下次可以从未完成的阶段继续，不必重新来过。

小改动会走精简的单 Agent 路径，避免为简单任务引入额外流程；中大型任务才会启用完整的多 Agent 协作。

```text
你的需求
   │
   ├─ 小改动 ───────> 理解 → 实现 → 自检 → 交付
   │
   └─ 中大型任务 ───> 澄清并确认 → 设计 → 实现 → 独立审查 → 测试 → 交付
                              │
                              └── 全程保存状态和产物，可随时恢复
```

## 快速开始

要求 Node.js 20+ 和 Python 3.8+。

在项目中安装默认的 Classic 工作流。下面以 Codex 为例，也可以使用 `claude`、`cursor` 或 `codebuddy`：

```bash
npx -y loopforge-cli@latest install codex
```

然后在对应 Coding Agent 中启动：

| 宿主 | 启动 | 恢复 |
|---|---|---|
| Codex | `$devflow-codex "你的需求"` | `$devflow-codex resume <task_slug>` |
| Claude Code | `/start-devflow "你的需求"` | `/resume-devflow <task_slug>` |
| Cursor | `/start-devflow "你的需求"` | `/resume-devflow <task_slug>` |
| CodeBuddy | `/start-devflow "你的需求"` | `/resume-devflow <task_slug>` |

如果安装或运行遇到问题，执行环境诊断：

```bash
npx -y loopforge-cli@latest doctor
```

完成一次 CodeBuddy Classic 运行后，一条命令打开本地观测面板：

```bash
npx -y loopforge-cli@latest dashboard
```

LoopForge 仅监听 `127.0.0.1`，观测数据保留在本地，并自动在浏览器打开。脚本场景可使用 `--no-open`、`--port` 或 `--build-only`。

## 你会得到什么

一次任务结束后，你会同时得到代码变更、已确认的需求、技术方案、独立审查意见、测试结果和交付总结。这些过程与结果默认保存在 `artifacts/{task_slug}/`，方便检查、复盘和交接。

<details>
<summary>查看默认产物目录</summary>

不同 Edition 的个别目录名称可能略有差异。

```text
01-requirement/   已确认的目标、范围与验收标准
02-design/        技术方案与执行计划
03-code/          代码变更与独立审查报告
04-e2e/           测试结果
05-knowledge/     可复用的项目知识
workflow-summary.md
```

</details>

Classic 工作流中断后，CodeBuddy、Cursor 和 Claude Code 使用 `/resume-devflow {task_slug}` 恢复，Codex 使用 `$devflow-codex resume {task_slug}`。状态查看、终止和单阶段命令见安装后对应宿主目录中的 README。

## 看见 Agent 如何完成工作

CodeBuddy 的 Classic Edition 内置本地可观测看板。它把落盘的流程证据变成可视化视图，集中呈现会话活动、Token 与成本趋势、Skill/Rule 使用情况、多 Agent 派发，以及 Devflow 阶段归因，方便定位高成本运行、确认自动化是否真正生效，并复盘任务如何在不同 Agent 之间流转。

看板的数据聚合和页面服务都在本机完成，不依赖外部遥测服务。运行日志可能包含工具参数和输出片段，因此 LoopForge 默认将它们排除在 Git 之外；未经检查不应对外分享原始日志。

<details>
<summary>打开本地看板</summary>

在项目根目录至少运行一次 CodeBuddy Classic 会话后执行：

```bash
npx -y loopforge-cli@latest dashboard
```

数据源、自定义模型定价和故障排查见[可观测性快速上手](.codebuddy/skills/agent-observability/references/quickstart.md)。

</details>

## 什么时候适合使用

LoopForge 尤其适合这些任务：

- 需求还比较模糊，需要先把目标和边界讨论清楚；
- 改动涉及多个模块，希望先形成方案再实施；
- 需要把实现、审查和测试交给不同角色完成；
- 任务可能跨越多个会话，需要保留状态并断点续跑；
- 团队希望留下可检查、可复盘的交付记录。

如果只是一次性的代码补全或非常明确的微小修改，直接使用编码 Agent 通常已经足够；LoopForge 会为进入其工作流的简单任务选择精简路径。

## 使用 Portable Edition

上面的“快速开始”安装的是默认的 Classic Edition，适合大多数用户。如果只希望安装基于 Agent Skills 的可移植工作流，请根据使用的 Coding Agent 选择下面的命令。为避免同一个 Coding Agent 同时加载两套工作流，以及状态和产物相互冲突，LoopForge 在每个项目中只允许为同一 Coding Agent 启用一种 Edition。如需切换，可重新安装并添加 `--force`。

### CodeBuddy

```bash
npx -y loopforge-cli@latest skills install codebuddy
```

安装后使用：

```text
/devflow "为订单列表增加状态筛选，并补充接口测试"
```

### Codex

```bash
npx -y loopforge-cli@latest skills install codex
```

安装后使用：

```text
$devflow "为订单列表增加状态筛选，并补充接口测试"
```

### Cursor

```bash
npx -y loopforge-cli@latest skills install cursor
```

安装后使用：

```text
/devflow "为订单列表增加状态筛选，并补充接口测试"
```

### Claude Code

```bash
npx -y loopforge-cli@latest skills install claude
```

安装后使用：

```text
/devflow "为订单列表增加状态筛选，并补充接口测试"
```

### Pi

```bash
npx -y loopforge-cli@latest skills install pi
pi install npm:pi-subagents
```

安装后使用：

```text
/skill:devflow "为订单列表增加状态筛选，并补充接口测试"
```

完整边界和目录说明见 [`EDITIONS.md`](EDITIONS.md)。

## 更新与卸载

如果希望长期使用，可以全局安装 CLI：

```bash
npm install --global loopforge-cli
```

默认 Classic Edition 的维护命令：

```bash
# CodeBuddy
loopforge update codebuddy
loopforge status codebuddy
loopforge uninstall codebuddy

# Codex
loopforge update codex
loopforge status codex
loopforge uninstall codex

# Cursor
loopforge update cursor
loopforge status cursor
loopforge uninstall cursor

# Claude Code
loopforge update claude
loopforge status claude
loopforge uninstall claude
```

Portable Edition 的维护命令：

```bash
# CodeBuddy
loopforge skills update codebuddy
loopforge skills status codebuddy
loopforge skills uninstall codebuddy

# Codex
loopforge skills update codex
loopforge skills status codex
loopforge skills uninstall codex

# Cursor
loopforge skills update cursor
loopforge skills status cursor
loopforge skills uninstall cursor

# Claude Code
loopforge skills update claude
loopforge skills status claude
loopforge skills uninstall claude

# Pi
loopforge skills update pi
loopforge skills status pi
loopforge skills uninstall pi
```

安装状态保存在 `.devflow/install-state.json`。检测到用户修改或文件冲突时，更新会停止而不是覆盖。只有在确认要替换冲突文件或切换 Edition 时才使用 `--force`，建议先运行 `loopforge plan` 检查变更。

如果已经克隆本仓库，也可以在仓库根目录从当前源码安装：

```bash
pipx install .
```

## 可选工具

`tools/tcmcp` 是腾讯云只读 MCP Initializr（Redis、CDB、TDSQL-C、TKE、CLS、COS）。它不包含在 `loopforge install` 或 npm CLI 包中。克隆仓库后：

```bash
cd tools/tcmcp
```

再按 [`tools/tcmcp/README.md`](tools/tcmcp/README.md) 启动。云 API 密钥不会写入生成的 zip 或容器镜像，只留在拉取资源或安装 MCP 客户端的本机。LoopForge 打 `vX.Y.Z` tag 发版时会发布 `ghcr.io/tencent/loopforge-tcmcp:<version>`。

## 权限与安全

部分宿主的 Classic 配置会启用自动执行或高权限 Agent 模式。请只在可信项目中使用；安装前可以通过 `loopforge plan codebuddy`、`loopforge plan codex`、`loopforge plan cursor` 或 `loopforge plan claude` 检查将写入的文件。不要把凭据、私有地址或生产运维配置写入工作流模板。

安全问题请参阅 [`SECURITY.md`](SECURITY.md)，并通过 GitHub Security Advisory 私密报告。

## 交流群

扫描下方企业微信二维码加入 LoopForge 交流群。

<img src="docs/community-wecom-qr.png" alt="LoopForge 交流群二维码" width="220" />

## 贡献

欢迎提交 Issue 和 Pull Request。请先查看公开的 [`ROADMAP.md`](ROADMAP.md)；修改工作流前请阅读 [`CONTRIBUTING.md`](CONTRIBUTING.md) 和仓库中的 `AGENTS.md`。版本变化见 [`CHANGELOG.md`](CHANGELOG.md)。

## 许可证

本项目使用 MIT License。第三方内容见 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)。

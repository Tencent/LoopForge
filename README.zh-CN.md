# LoopForge

[English](README.md) | 简体中文

**让编码 Agent 不只“写出代码”，而是按一套可确认、可审查、可恢复的流程完成交付。**

把一个需求交给编码 Agent 很容易，难的是让它在动手前理解目标，在实现中不偏离范围，并在结束前完成独立审查和测试。任务一旦变长，还会遇到上下文丢失、执行中断，以及最终只剩一份难以追溯的代码变更。

LoopForge 把这些步骤组织成一条完整的软件交付流程。你只需要描述要解决的问题；LoopForge 会根据任务复杂度选择执行方式，推动 Agent 完成需求澄清、方案设计、代码实现、独立审查和测试，并把关键决策与验证结果保存下来。

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

以下命令安装默认的 Classic Edition。不确定选哪一种时，直接使用这一版。

### CodeBuddy

```bash
npx -y loopforge-cli@latest install codebuddy
npx -y loopforge-cli@latest status codebuddy
```

安装后启动：

```text
/start-devflow "为订单列表增加状态筛选，并补充接口测试"
```

### Codex

```bash
npx -y loopforge-cli@latest install codex
npx -y loopforge-cli@latest status codex
```

安装后启动：

```text
$devflow-codex "为订单列表增加状态筛选，并补充接口测试"
```

### Cursor

```bash
npx -y loopforge-cli@latest install cursor
npx -y loopforge-cli@latest status cursor
```

安装后启动：

```text
/start-devflow "为订单列表增加状态筛选，并补充接口测试"
```

### Claude Code

```bash
npx -y loopforge-cli@latest install claude
npx -y loopforge-cli@latest status claude
```

安装后启动：

```text
/start-devflow "为订单列表增加状态筛选，并补充接口测试"
```

如果安装或运行遇到问题，执行环境诊断：

```bash
npx -y loopforge-cli@latest doctor
```

## 从终端启动

上面的步骤是「先安装工作流，再打开宿主，在聊天框里手敲入口命令」。全局安装 CLI 后可以省掉这次切换，直接用短别名 `lf`：

```bash
npm install --global loopforge-cli   # 或：pipx install .
lf install claude                    # 每个项目执行一次
lf run "为订单列表增加状态筛选，并补充接口测试"
```

`lf` 与 `loopforge` 完全等价，只是更短。`lf run` 会读取 `.devflow/install-state.json` 确认项目装了哪些宿主，探测 `PATH` 上对应的 Agent CLI（`claude`、`codex`、`cursor-agent`、`codebuddy`），再用该宿主该 edition 的入口提示词把它启动起来 —— Claude Code 是 `/start-devflow <需求>`，Codex 是 `$devflow-codex start <需求>`。

```bash
lf run "为订单列表增加状态筛选"    # 完整写法
lf "为订单列表增加状态筛选"        # 等价简写
lf run --dry-run "需求"           # 只打印将要执行的命令，不启动
lf run --host codex "需求"        # 项目装了多个宿主时显式指定
```

项目装了多个宿主、且它们的 CLI 都在 `PATH` 上时，`lf run` 会列出候选项让你选择；不在交互终端里（例如 CI）则会要求用 `--host` 指定。如果宿主 CLI 不在 `PATH` 上或命令名不同，用 `LOOPFORGE_CLI_<宿主大写>` 指过去：

```bash
LOOPFORGE_CLI_CURSOR=/opt/cursor/bin/cursor-agent lf run "需求"
```

项目还没装工作流时，`lf run` 会报错并给出该执行的安装命令，不会往项目里写入任何文件。

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

## 贡献

欢迎提交 Issue 和 Pull Request。修改工作流前请先阅读 [`CONTRIBUTING.md`](CONTRIBUTING.md) 和仓库中的 `AGENTS.md`。版本变化见 [`CHANGELOG.md`](CHANGELOG.md)。

## 许可证

本项目使用 MIT License。第三方内容见 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)。

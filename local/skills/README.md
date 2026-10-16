# DevFlow Skills

本目录提供一个可移植的软件交付工作流：

- `devflow/`：任务分级、需求澄清、方案、计划、实现、审查、测试、知识沉淀和总结。
- `devflow-clarify-requirements/`：需求阶段使用的澄清 Skill，由主协调 Agent 在当前会话中调用。

脚本只依赖 Python 3.8+ 标准库。当前支持 CodeBuddy、Codex、Cursor 和 Claude Code。

## 流程

```text
small:  SOLO → SUMMARY

medium / large:
REQUIREMENT → DESIGN → IMPLEMENT → REVIEW → TEST → [KNOWLEDGE] → SUMMARY
```

- REQUIREMENT：主 Agent 调研、询问用户并填写需求报告，不创建需求分析 Agent；报告校验通过后必须由用户明确确认，才能进入 DESIGN。
- DESIGN、IMPLEMENT、REVIEW、TEST：每阶段创建新的独立 Agent。DESIGN 一次盘点项目现有测试能力；TEST 可补测试与夹具但不能修改生产代码。项目已有 E2E 且改动落入其覆盖边界时必须同步补齐，没有 E2E 时不为流程强制引入。
- KNOWLEDGE：条件阶段。新增了可复用且尚未被项目文档覆盖的约束、坑、诊断或回退知识时创建 Agent；只有常规结果或已有知识时记录理由并跳过。
- SUMMARY：主 Agent 汇总已经验证的产物，不创建 leader/summarizer Agent。
- 代码或知识检索可以使用独立只读 helper，但 reviewer 只能用于正式 REVIEW。

工作流产物写入项目的 `artifacts/<task-slug>/`，状态保存在 `workflow-state.json`。

## 安装

推荐使用 LoopForge Local 提供的统一 CLI。以下源码安装命令在仓库根目录执行；Portable edition 使用独立的 `skills` 命令组：

```bash
pipx install ./local
loopforge skills install codebuddy
loopforge skills install codex
loopforge skills install cursor
loopforge skills install claude
```

`loopforge skills status/update/uninstall <host>` 和 `loopforge doctor` 使用
`.devflow/install-state.json` 保护用户文件。下面的 adapter 命令只保留为开发和
故障排查入口，不再是面向使用者的主安装方式。
遇到旧 Skill 文件或软链接冲突时默认停止；确认替换后使用
`loopforge skills install <host> --force`。

### CodeBuddy

将 `devflow` 暴露为项目 Skill，再运行 adapter 安装器：

```bash
mkdir -p <项目目录>/.codebuddy/skills
ln -s <本仓库绝对路径>/devflow <项目目录>/.codebuddy/skills/devflow
python3 <本仓库绝对路径>/devflow/adapters/codebuddy/install.py \
  --project-root <项目目录> \
  --refresh-managed
```

安装后使用 `/devflow <软件交付需求>`。只使用新命令 `/devflow`，不要与旧版 `/start-devflow`、`architect`、`leader` 工作流混用。新版 CodeBuddy 只需要：

- `devflow-stage-executor`
- `devflow-research-helper`

medium/large 会为每次运行创建一个 `multi-agents-devflow-<task-slug>-<run-id>` Team，避免同 slug 的历史 Team 把消息送回旧 main。实际 route 中由 Agent 执行的阶段按需创建独立 Team member，创建调用必须携带状态中的完整 `team_name`，并在初始 Prompt 中一次性传入完整阶段规则和任务；首次执行不再二次 `send_message` 派发。阶段结果通过 `send_message` 返回 main。普通 `task` subagent 不是合法的 CodeBuddy 阶段执行器。

### Cursor

```bash
python3 <本仓库绝对路径>/devflow/scripts/install_adapter.py \
  --adapter cursor \
  --project-root <项目目录> \
  --refresh-managed
```

重开 Cursor 会话后使用 `/devflow <需求>`。当前会话必须实际提供 `Task`。
安装器会从 Cursor manifest 生成两个宿主 Agent，并把 `.cursor/skills/`
链接到 canonical `skills/`；不要直接修改生成的 Agent 文件。

### Claude Code

```bash
python3 <本仓库绝对路径>/devflow/scripts/install_adapter.py \
  --adapter claude \
  --project-root <项目目录> \
  --refresh-managed
```

首次创建 `.claude/skills` 或 `.claude/agents` 后重启 Claude Code，再使用 `/devflow <需求>`。
Claude Code 的 Agent 使用自己的权限字段生成，但执行器正文与 Cursor 共用
同一个来源。

不能创建软链接的平台可在安装命令中增加 `--copy-skills`。复制安装会在宿主
目录记录托管清单；上游 Skill 更新后再次执行带 `--copy-skills` 和
`--refresh-managed` 的安装命令可确定性刷新，未由安装器管理的同名目录始终保留。

### Codex

将两个 Skill 目录复制或链接到 `${CODEX_HOME:-~/.codex}/skills/`，然后在项目中要求 Codex 使用 `$devflow`。Codex adapter 使用动态创建的独立 Agent，不需要生成项目级 Agent 文件。

## 运行模式

- `auto`：阶段门禁通过后继续下一阶段。
- `manual`：DESIGN、REVIEW、TEST 后等待人工批准。
- `isolated`：medium/large 默认模式，中间阶段使用独立 Agent。
- `single-context`：仅在宿主确实无法创建独立 Agent 时显式降级并记录原因。

无论 `auto` 还是 `manual`，medium/large 都会在 REQUIREMENT 报告完成后暂停，等待用户确认目标、范围、排除项、关键决策和验收标准。该确认是强制门禁，不属于自动流转。

前端、后端和混合任务共用同一个状态机与产物合同。DevFlow 会按任务加载 `references/frontend-workflow.md`、`backend-workflow.md` 或设计稿参考；不建议拆成两个重复 Skill。

## 恢复任务

不要对已有 slug 再次运行 `init`。CodeBuddy 中只需输入：

```text
/devflow resume <task-slug>
```

也支持中文：`/devflow 继续 <task-slug>`。不需要填写 `workflow-state.json` 路径。脚本调用为：

```bash
python3 <Skill 路径>/scripts/workflow_state.py resume \
  --project-root <项目目录> \
  --slug <task-slug> \
  --fresh-team
```

`resume` 会返回 JSON action：

- `prepare`：准备 next stage。
- `spawn-and-assign`：按 adapter 拓扑创建阶段 Agent 并登记真实 ID；CodeBuddy 创建同一 Team 下的新 member。
- `start-assigned`：启动已登记执行者。
- `continue-executor`：继续已有 in-progress Agent，不创建新 ID。
- `upgrade-team`：旧 CodeBuddy v2 状态尚未登记中间阶段执行者时，先原地升级为 Team v3。
- `rotate-team`：CodeBuddy 从新主会话恢复时先直接删除返回的同名旧 Team，再分配新 Team 并放弃旧 executor 绑定。
- `restart-required`：状态文件不存在；删除返回的同名旧 Team 后停止 resume，重新提交原始需求。
- `approve`：处理用户确认或 manual gate；REQUIREMENT 仅在用户明确确认后使用 `--user-confirmed`。
- `assess-knowledge`：根据最终 diff、审查和测试证据判断是否运行 KNOWLEDGE；按知识价值而不是任务规模决定。
- `completed`：流程已完成。

高级用法仍可通过 `--state <workflow-state.json>` 指定完整路径。

状态和产物检查：

```bash
python3 <Skill 路径>/scripts/workflow_state.py status --state <state-path>
python3 <Skill 路径>/scripts/workflow_state.py validate --state <state-path>
```

## 自测与评测

修改 Skill 后运行：

```bash
python3 devflow/scripts/validate_config.py
python3 -m unittest discover -s devflow/tests -v
python3 <skill-creator 路径>/scripts/quick_validate.py devflow
```

回归测试覆盖：Markdown 代码块标题误判、执行计划合同、项目指令发现、阶段提示自检、错误宿主 Agent 拒绝、resume 建议，以及不创建需求/总结子 Agent 的完整 medium 生命周期。另有 CLI E2E 用例覆盖真实临时项目、代码测试、阶段提示生成、产物门禁和流程完成。真实任务跑完后，再对其 `workflow-state.json` 执行 `validate`，检查实际产物和宿主交接。

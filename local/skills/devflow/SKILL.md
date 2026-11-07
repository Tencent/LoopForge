---
name: devflow
description: 在用户明确要求结构化、可恢复、多阶段的软件交付流程，或要求按 devflow 继续已有任务时使用。覆盖任务分级、需求、设计、计划、实现、设计稿还原、代码审查、测试、固定产物归档、知识沉淀和预览部署；适用于不同 Agent、语言、框架和仓库。普通的一次性小改、单独问答或无需流程编排的代码操作不应仅因属于软件开发而自动触发。
---

# DevFlow

## 必须执行

1. 将用户当前目录设为 `PROJECT_ROOT`，不向父目录寻找或自动切换项目。先读取宿主项目指令，再运行 `python3 <SKILL_ROOT>/scripts/inspect_context.py .` 发现实际项目约定。
2. 开始任何阶段前读取 [rules/core.md](rules/core.md)。`rules/` 不会被宿主自动加载；`build_stage_prompt.py` 按 [rules/manifest.json](rules/manifest.json) 确定性注入核心规则、当前阶段规则，以及由项目证据命中的专项规则。
3. 完整流程读取 [references/workflow-contract.md](references/workflow-contract.md)。medium/large 额外读取 [references/agent-execution.md](references/agent-execution.md) 和 [references/runtime-core.md](references/runtime-core.md)，并且只加载一个匹配的 `adapters/` 实现。
4. 每阶段优先用 `workflow_state.py prepare ... --emit-prompt` 一次完成模板准备和提示生成，再执行 `start → finish`；仅对已经 prepare 的恢复状态单独调用 `build_stage_prompt.py`。REQUIREMENT 和 SUMMARY 由协调者在当前上下文执行，不创建阶段 Agent；isolated 模式的其他 route 阶段在 prompt 后、start 前增加 `spawn → assign`。只有宿主真实返回 executor ID 后才能 `assign`，并登记 adapter 要求的 `executor_type`。
5. REQUIREMENT 调用 `$devflow-clarify-requirements` 完成证据分析、用户交互和需求报告。报告通过内容校验后必须向用户展示摘要并等待明确确认；只有确认后才能执行 `approve --stage REQUIREMENT --user-confirmed` 并进入 DESIGN。DESIGN 及后续 isolated 阶段只读取清单允许的上游产物和当前必要证据，协调者不得代写。

脚本仅依赖 Python 3.8+ 标准库。Python 不可用时读取 [references/manual-runtime.md](references/manual-runtime.md) 执行等价门禁，不得跳过。

## 按需读取

- 前端、后端、Go、SQL 和设计稿专项规则由阶段 Prompt 自动选择；分类不明确但用户或项目已确认时，用 `build_stage_prompt.py --profile <id>` 显式追加。
- 审查、测试和知识沉淀规则按阶段自动注入；规则入口见 [references/quality.md](references/quality.md)。
- 用户明确要求预览或部署：[references/preview-deploy.md](references/preview-deploy.md)

无法分类时先检查项目证据；不要为了“可能相关”注入全部专项规则。混合项目可以组合多个 profile。

## 不可绕过

- `agents/manifest.json` 是逻辑角色、职责和上游阶段的唯一事实源；`templates/manifest.json` 是产物的唯一事实源。adapter 只映射少量宿主执行器，不得复制逻辑角色清单。
- `prepare` 只生成缺失模板并记录基线，不覆盖已有产物。阶段执行者填写模板并删除所有 `{{...}}`；不要为标题措辞或排版反复返工。
- medium/large 的 REQUIREMENT 和 SUMMARY 由协调者执行；DESIGN、IMPLEMENT、REVIEW、TEST 分别使用独立 Agent。KNOWLEDGE 是条件阶段：有新增可复用知识才创建 Agent，否则用 `decide-knowledge --decision skip --reason ...` 留下依据并跳过；不得按 size 一刀切。宿主确实不支持时才允许带具体原因的 `single-context` 降级。
- REQUIREMENT 的用户确认门禁在 `auto` 和 `manual` 模式下都不可跳过；没有用户明确确认时不得创建 DESIGN 执行者。
- 保护用户已有改动，不自动 stash、commit、push、部署或执行其他未授权外部写操作。
- 只依据真实命令、观察和产物宣布完成，明确区分 passed、failed、blocked、not-run。

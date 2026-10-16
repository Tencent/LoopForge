# 执行上下文模型

## 必需拓扑

medium/large 默认使用 `isolated`：REQUIREMENT 由协调上下文完成；DESIGN、IMPLEMENT、REVIEW、TEST 每阶段即时创建新的独立执行上下文。TEST 后先做知识价值门禁，只有存在新增可复用知识时才创建 KNOWLEDGE Agent；SUMMARY 由协调者完成。协调者不得亲自生成中间 isolated 阶段产物，也不得用同一个 Agent 连续承担多个阶段。small 默认使用 `single-context`。

| 阶段 | 必需角色 | 职责边界 |
|---|---|---|
| REQUIREMENT | `devflow-requirement-owner` | 协调者调研必要证据、调用 `$devflow-clarify-requirements` 完成交互并写需求报告；不创建阶段 Agent |
| DESIGN | `devflow-architect` | 根据需求报告设计方案和计划 |
| IMPLEMENT | `devflow-developer` | 仅实现计划内改动并写 change report |
| REVIEW | `devflow-code-reviewer` | 仅审查已完成的实现和实际 diff |
| TEST | `devflow-test-engineer` | 独立补齐必要测试并执行风险分层验证；只写测试，不改生产代码 |
| KNOWLEDGE | `devflow-knowledge-engineer` | 条件执行：从最终证据提炼新增且跨任务可复用的知识 |
| SUMMARY | `devflow-summarizer` | 协调者承担该逻辑角色，汇总已验证产物并完成最终门禁；不创建 leader/summarizer Agent |

表中名称是工作流的逻辑角色，不要求宿主存在同名 Agent 定义。REQUIREMENT 的逻辑角色绑定协调者；adapter 应使用少量通用宿主执行器，并通过 `build_stage_prompt.py` 向其他阶段注入逻辑角色。DESIGN 及后续阶段仍须按需创建全新实例，不在初始化时预建未进入阶段的角色。CodeBuddy 实例必须是同一工作流 Team 中带 `team_name` 的独立 member，不得使用普通 task subagent。

协调者阶段执行：`prepare --emit-prompt` → `start` → 协调者填写产物 → `finish`。其他 isolated 阶段执行：`prepare --emit-prompt` → 通过 adapter 创建 Agent → 用宿主真实 ID 和执行器类型 `assign` → `start` → 等待产物 → `finish`。阶段 Agent 必须先运行提示中的自检命令并自行修正产物；不得把模板或格式修复交给协调者。不得手写、猜测或跨阶段复用 ID。

宿主明确不支持独立 Agent 时，medium/large 才能使用 `single-context`，初始化必须提供具体 `--fallback-reason` 并向用户说明隔离已降级；不得静默退化。IMPLEMENT 最多并行 3 个文件集合互不重叠的任务，存在共享文件、顺序依赖或迁移依赖时串行。

## 检索助手

阶段执行者或协调者可以为有边界的只读调研启动辅助 Agent，但只能登记为：

- `devflow-code-explorer`：定位代码、调用链、数据结构、API 和测试；不做审查结论，不修改代码，不写阶段主产物。
- `devflow-knowledge-retriever`：检索项目文档、历史决策和知识库；不修改代码，不写阶段主产物。

使用 adapter 的通用只读执行器创建新实例，并以 `devflow-code-explorer` 或 `devflow-knowledge-retriever` 作为审计角色登记；不要为每个逻辑 helper 复制宿主定义，也不要改用阶段角色顶替。

简单的 `rg`、单文件读取或当前 Agent 已能回答的问题不启动 helper。需要隔离大量检索上下文时，先把同一范围的相关问题合并给一个 helper；只有两个范围确实互不重叠且并行有收益时才启第二个。使用 `scripts/build_helper_prompt.py --role ... --purpose ... [--scope ...]` 生成含范围和停止条件的提示。任何 REVIEW 前的调研都禁止使用 `devflow-code-reviewer`。

## 上下文交接

上游输入由 `agents/manifest.json` 的精确产物清单定义。提示生成器只抽取当前角色需要的章节作为精简交接，下游默认不再打开完整报告；SUMMARY 只接收最终交付、审查、测试和可选知识证据。只额外传当前阶段必需的代码、diff 或 helper 事实摘要，不传完整对话、完整工具日志和无关推理。

`workflow-state.json` 中的 `executor_role`、`executor_type`、`executor_id`、`executor_topology`、`team_name` 和 `helpers` 是隔离审计证据。字段缺失、逻辑角色、宿主类型或拓扑不匹配、Team 名称不一致、同一 executor ID 跨阶段复用时，isolated 状态不得开始或通过。

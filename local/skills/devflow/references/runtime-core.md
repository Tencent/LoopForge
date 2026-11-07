# 运行时核心

保持流程语义与宿主无关，把 Agent 生命周期委托给选定的宿主适配器。

## 抽象操作

隔离运行时必须提供：

1. `spawn(role, prompt) -> executor_id`：创建真实、全新的 Agent，并返回宿主签发的标识。
2. `send(executor_id, message)`：在不更换 Agent 身份的前提下发送补充输入。
3. `wait(executor_id)`：等待完成或结构化交接。
4. `close(executor_id)`：阶段不再需要重试时释放 Agent。
5. `spawn_helper(role, bounded_prompt) -> executor_id`：创建只读代码检索或知识检索助手。

不得用自造标签代替宿主返回的 ID。协调者可以调用状态脚本、向用户提问、校验产物和派发任务，但不得生成隔离阶段的产物。

## 适配器选择

- CodeBuddy 提供 `task|team_create|send_message|team_delete|use_skill`：加载 [adapters/codebuddy/adapter.md](../adapters/codebuddy/adapter.md)，使用 Team member 拓扑；不得把普通 task subagent 当成阶段执行者。
- Codex 提供 `spawn_agent|send_message|wait_agent`：加载 [adapters/codex/adapter.md](../adapters/codex/adapter.md)。
- 没有受支持的生命周期 API：先报告限制并记录 `fallback_reason`，之后才能使用 `single-context`。

一个 isolated 流程只能选择一个适配器。除非适配器明确提供桥接，否则不要混用项目原生流程与本状态机。适配器只实现 `spawn/send/wait/close`，不得复制或改写核心状态、模板和规则语义。

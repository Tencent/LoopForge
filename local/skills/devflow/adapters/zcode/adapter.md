# ZCode 适配器

ZCode 从项目级 `.zcode/skills/` 原生发现 Portable Skill，安装器不改动 `~/.zcode` 用户配置。

**路由消歧**：在 `/skill devflow` 下，用户和本文所说的“工作流”只指 `workflow_state.py` 管理的 DevFlow 状态机，
禁止调用 ZCode 内置的 `CreateWorkflow`。若宿主强制要求调用而无法绕过，停止并报告，不得用 `single-context` 掩盖。

## 入口

`loopforge skills install zcode` 后，在 ZCode 中使用 `/skill devflow "<需求>"`（TUI 与 `--prompt` 均支持）。

## 能力映射

1. `spawn(role, prompt) -> executor_id`：调用 `Agent` 工具，`subagent_type: "general-purpose"`，传入简短 `description` 与完整阶段 `prompt`。
   新子会话不继承父会话聊天历史，但宿主仍会注入工作区 `AGENTS.md`；返回结果中的 `agentId` 就是 `executor_id`。
2. `wait(executor_id)`：前台调用同步返回子会话报告；若明确转入异步后台执行，此时结束本轮并等待宿主投递的
   `<task-notification>` 完成通知，不得提前 `finish`。若调用超时、取消或报错失败，走错误恢复与重试，不得推定后台仍在运行。
3. `close(executor_id)`：子 Agent 完成后由宿主回收；后台任务需中止时用 `TaskStop`。
4. `spawn_helper(role, bounded_prompt) -> executor_id`：调用 `Agent` 并指定 `subagent_type: "Explore"`，提示中限定只读边界，
   以 `devflow-code-explorer` 或 `devflow-knowledge-retriever` 为审计角色，用 `helper --executor-type dynamic --dispatch-tool agent` 登记返回的 `agentId`。
5. `send(executor_id, message)`：仅限同阶段。`SendMessage` 可用时可继续原 `agentId`，这是后台续跑，必须等到真实完成通知后才 `finish`；
   或在阶段为 `pending`/`failed`、已完成 `prepare` 且确认旧实例已停止写入后，新建 `Agent` 实例并重新 `assign`。
   `in_progress` 期间不得直接换绑 `agentId`，也不得借此绕过失败次数上限。

## 使用步骤

1. 确认 `devflow-clarify-requirements` 可发现；缺失时立即停止并报告，不自创替代访谈。
2. 确认当前会话提供 `Agent` 工具；缺失时先停止并报告限制，仅在用户明确授权并记录 `fallback_reason` 后才能降级为 `single-context`，不得标成 `isolated`。
3. medium/large 使用 `--execution-mode isolated --host-adapter zcode` 初始化。
4. REQUIREMENT 执行 `prepare --emit-prompt → start`，主会话用 `Skill` 工具调用 `{"skill": "devflow-clarify-requirements"}`（无前缀精确名），
   填写并校验需求报告后 `finish` 进入待批，展示摘要并获用户明确确认后执行 `approve --stage REQUIREMENT --user-confirmed`；不得创建需求分析子 Agent。
5. route 中其他 Agent 阶段先 `prepare --emit-prompt`，再用 `Agent`（`subagent_type: "general-purpose"`）创建新子会话，
   取返回的 `agentId`，`assign --executor-type dynamic --dispatch-tool agent`，再 `start`，等子会话返回并校验产物后 `finish`。协调者不得代写隔离阶段产物。
6. 不同阶段严禁复用 `agentId`。REVIEW 前不得使用 `devflow-code-reviewer`。SUMMARY 由主 Agent 在当前上下文完成。

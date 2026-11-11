# ZCode 适配器

支持当前稳定版以及后续仍提供公开 `Agent` 工具的 ZCode 版本；不绑定私有版本号。
当会话禁用了 `Agent` 工具（例如通过 `--disallowed-tools` 移除）时，medium/large 流程必须记录具体原因并按 [runtime-core.md](../../references/runtime-core.md) 降级为 `single-context`，不得伪造 isolated 执行器。

ZCode 从项目级 `.zcode/skills/` 原生发现 Portable Skill，无需修改 `~/.zcode` 用户配置。

## 能力映射

1. `spawn(role, prompt) -> executor_id`：调用 `Agent` 工具创建全新独立子会话（指定 `subagent_type: "general-purpose"` 以避免继承父会话历史），传入简短 `description` 与完整阶段 `prompt`；工具返回结果中的 `agentId` 即为宿主签发的 `executor_id`。
2. `send(executor_id, message)`：同阶段需要补充输入或重试时，调用 `SendMessage`（`to: "<agentId>"`）继续该子 Agent，或重新创建新 `Agent` 实例并重新 `assign`。
3. `wait(executor_id)`：前台调用 `Agent` 会同步等待子会话完成并返回报告；若转为后台执行，结束当前轮次等待宿主投递 `<task-notification>` 完成通知，不得提前 `finish`。
4. `close(executor_id)`：子 Agent 完成后由宿主自动回收，后台任务如需中止可调用 `TaskStop`。
5. `spawn_helper(role, bounded_prompt) -> executor_id`：调用只读子 Agent（如 `Agent` 指定 `subagent_type: "Explore"` 并在提示中限制只读边界），将返回的 `agentId` 通过 `helper --executor-type dynamic --dispatch-tool agent` 登记。

## 使用步骤

1. 确认 `devflow-clarify-requirements` 可通过 `.zcode/skills/` 发现，且当前会话提供 `Agent` 工具；缺失时先报告限制，仅在记录 `fallback_reason` 后降级为 `single-context`。
2. medium/large 使用 `--execution-mode isolated --host-adapter zcode` 初始化。
3. REQUIREMENT 执行 `prepare --emit-prompt → start`，由主 Agent 在当前会话完成需求澄清并填写需求报告，经用户明确确认后执行 `approve --stage REQUIREMENT --user-confirmed`；不得创建需求分析子 Agent。
4. route 中其他 Agent 阶段先用 `prepare --emit-prompt` 一次完成模板准备与提示生成，再调用 `Agent` 创建全新子会话，将返回的真实 `agentId` 用 `assign --executor-type dynamic --dispatch-tool agent` 登记后调用 `start`，待子会话返回并校验产物后调用 `finish`。协调者不得代写 isolated 阶段产物。
5. 不同阶段必须使用不同的 `agentId`；REVIEW 阶段前不得使用 `devflow-code-reviewer`。SUMMARY 由主 Agent 在当前上下文完成。

## 使用入口

在目标项目执行 `loopforge skills install zcode` 后，在 ZCode 中输入 `/skill devflow <需求>` 启动工作流。

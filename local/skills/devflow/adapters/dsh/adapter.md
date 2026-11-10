# DeepSeek Harness 适配器

支持当前稳定版以及后续仍提供公开 `subagent` 工具的 DSH 版本；不绑定私有版本号。
没有该工具的 profile 仍可运行，但 medium/large 必须记录原因并降级为
`single-context`。

DSH 通过 `subagent` 工具创建独立阶段执行器。每个阶段必须使用新的子 Agent，
并将工具返回的子 Agent 名称作为 `executor_id` 登记到 `workflow_state.py`。

## 能力映射

1. `spawn(role, prompt) -> executor_id`：调用 `subagent`，使用完整阶段提示；返回的
   子 Agent 名称就是 `executor_id`。
2. `wait(executor_id)`：结束当前轮次，等待 DSH 自动投递子 Agent 完成通知。
3. `close(executor_id)`：DSH 子 Agent 完成后由宿主回收，不需要额外调用。
4. `spawn_helper(role, bounded_prompt) -> executor_id`：同样使用 `subagent`，只读范围
   由提示明确限制。

DSH 没有 LoopForge 专用的 send/close 生命周期 API。不要伪造 executor ID，也不要把
普通当前上下文工作标记为 isolated；如果当前会话没有 `subagent` 工具，必须用具体
原因降级为 `single-context` 并记录 `fallback_reason`。

## 使用入口

安装 Portable Skill 后，在 DSH 中使用 `/devflow <需求>`，或通过 LoopForge DSH 插件的
`/loopforge start <需求>` 初始化任务。任务状态和产物保存在项目的
`artifacts/<task_slug>/` 下；需要人工确认时，插件会显示待批准阶段和批准命令。

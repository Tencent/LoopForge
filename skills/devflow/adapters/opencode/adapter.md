# OpenCode 适配器

仅在 OpenCode 当前会话实际提供 `task` 工具时使用。OpenCode 可自动发现
`.opencode/skills/` 中的 Skill；通过提示要求它使用 `devflow` Skill 启动流程。

1. 运行 `loopforge skills install opencode`，确认 `.opencode/skills/` 有两个 DevFlow
   Skill，且 `.opencode/agents/` 有两个托管的执行器。新建 OpenCode 会话以发现配置。
2. medium/large 使用 `--execution-mode isolated --host-adapter opencode` 初始化。
3. REQUIREMENT 执行 `prepare --emit-prompt → start`，由主 Agent 调用
   `devflow-clarify-requirements` 并填写需求报告，再 `finish`；禁止启动需求分析 subagent。
4. route 中其他 Agent 阶段用 `prepare --emit-prompt` 生成提示后，以
   `task(subagent_type="devflow-stage-executor", prompt=<完整提示>)` 创建新的 subagent。
   从返回的 `<task id="...">` 记录真实 task ID，并用
   `assign --executor-type devflow-stage-executor --dispatch-tool task` 登记；收到完成结果后
   才能 `finish`。
5. 只读调研使用 `task(subagent_type="devflow-research-helper", prompt=<完整提示>)`，以
   `helper --executor-type devflow-research-helper --dispatch-tool task` 登记。SUMMARY 由主
   Agent 执行，不创建 subagent。

不同阶段不得复用同一个 task ID。阶段执行器不得嵌套创建 helper；OpenCode 未提供
`task` 时，报告限制并仅在满足工作流合同的前提下显式降级。

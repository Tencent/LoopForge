# Claude Code 适配器

仅在 Claude Code 当前会话实际提供 `Agent` 工具时使用。自定义 subagent 不继承主会话历史，也不能继续派发 subagent，因此协调者必须提供完整阶段提示并负责所有 helper 派发。

1. 运行 `python3 <SKILL_ROOT>/scripts/install_adapter.py --adapter claude --project-root . --refresh-managed`，确认 `.claude/agents/` 中只有两个 DevFlow 托管执行器，`.claude/skills/` 指向同一份 canonical Skill。首次创建顶层 skills 目录后重启 Claude Code；也可用 `/agents` 检查 Agent 定义。
2. medium/large 使用 `--execution-mode isolated --host-adapter claude` 初始化。
3. REQUIREMENT 执行 `prepare --emit-prompt → start`，由主 Agent 调用 `/devflow-clarify-requirements` 并填写需求报告，再 `finish`；禁止启动需求分析 subagent。
4. route 中其他 Agent 阶段用 `prepare --emit-prompt` 生成提示后，以 `Agent` 工具选择 `devflow-stage-executor` 创建新的 subagent；明确要求前台执行，若宿主仍转为后台则等待完成通知，不得提前 `finish`。将真实 ID 用 `assign --executor-type devflow-stage-executor --dispatch-tool agent` 登记，再完成生命周期。
5. 只读调研选择 `devflow-research-helper`，以 `helper --executor-type devflow-research-helper --dispatch-tool agent` 登记。SUMMARY 由主 Agent执行，不创建 subagent。

不同阶段不得复用同一个 Agent ID。不得依赖自动 delegation 猜测角色，也不得让阶段 subagent 嵌套创建 helper。

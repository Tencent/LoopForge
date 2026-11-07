# Cursor 适配器

仅在 Cursor 当前会话实际提供 `Task` 时使用。Cursor 版本、模型或团队策略导致 `Task` 缺失时停止并说明，不能把 isolated 状态标成已执行。

1. 运行 `python3 <SKILL_ROOT>/scripts/install_adapter.py --adapter cursor --project-root . --refresh-managed`，确认 `.cursor/agents/` 中只有两个 DevFlow 托管执行器，`.cursor/skills/` 指向同一份 canonical Skill；当前会话没有发现新定义时重开会话。
2. medium/large 使用 `--execution-mode isolated --host-adapter cursor` 初始化。
3. REQUIREMENT 执行 `prepare --emit-prompt → start`，由主 Agent 调用 `devflow-clarify-requirements` 并填写需求报告，再 `finish`；禁止启动需求分析 subagent。
4. route 中其他 Agent 阶段用 `prepare --emit-prompt` 生成提示后，以 `Task(subagent_type="devflow-stage-executor", prompt=<完整提示>)` 创建新的前台 subagent；不要让阶段执行器继续嵌套派发。将真实 ID 用 `assign --executor-type devflow-stage-executor --dispatch-tool task` 登记，再完成生命周期。
5. 只读调研使用 `devflow-research-helper`，以 `helper --executor-type devflow-research-helper --dispatch-tool task` 登记。SUMMARY 由主 Agent执行，不创建 subagent。

不同阶段不得复用同一个 Task ID。Task 异步返回时使用宿主等待能力取得最终结果后才能执行 `finish`。

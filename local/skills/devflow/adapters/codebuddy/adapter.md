# CodeBuddy 适配器

仅在 `task`、`team_create`、`send_message`、`team_delete`、`use_skill` 均可调用时使用；CodeBuddy 必须使用 Team 拓扑，Shell/Python 不能替代 Agent 生命周期工具。

1. 如托管 Agent 或命令缺失，运行 `python3 <SKILL_ROOT>/adapters/codebuddy/install.py --project-root . --refresh-managed`。安装器不删除项目自有文件，但会报告 `architect.md`、`leader.md` 等旧版 DevFlow 冲突；它们不得用于当前 `/devflow`。
2. medium/large 使用 `--execution-mode isolated --host-adapter codebuddy` 初始化。状态机会生成带 `run_id` 的 `team_name`。创建前只对 `.codebuddy/teams/<team_name>` 做一次存在性判断；同名存在就立即调用 `team_delete(team_name=<team_name>)`，失败则运行 `python3 <SKILL_ROOT>/adapters/codebuddy/cleanup_team.py --project-root . --team-name <team_name>` 删除这个精确目录，然后直接 `team_create`。不得读取 Team 配置、成员、inbox 或状态，不得尝试恢复。
3. REQUIREMENT 执行 `prepare --emit-prompt → start`，由主 Agent 调用 `devflow-clarify-requirements` 并填写需求报告，再校验和 `finish`；此时状态必须为 `awaiting_approval=REQUIREMENT`。主 Agent 展示报告摘要并等待用户明确确认，之后才执行 `approve --stage REQUIREMENT --user-confirmed`。不得调用 `task` 创建需求分析 Agent，也不得在确认前创建 DESIGN member。
4. route 中其他 Agent 阶段第一次执行时，用 `prepare --emit-prompt` 一次完成准备和完整提示生成，再直接创建并启动唯一 Team member：`Task(name="devflow-<stage>-<task_slug>", team_name=<team_name>, subagent_name="devflow-stage-executor", mode="bypassPermissions", prompt=<完整阶段提示>)`。核心规则、阶段规则、专项规则、角色边界、输入输出和自检命令必须全部包含在这一次 `prompt` 中。严禁省略 `team_name`，严禁使用“待命”占位 Prompt，严禁把完整提示交给无 Team 的普通 `task` subagent，也不预建未进入阶段的 member。
5. `Task` 返回真实 member ID 后立即用 `assign --executor-type devflow-stage-team-member --dispatch-tool task --team-name <team_name>` 登记并调用 `start`；首次执行不得再用 `send_message` 重复派发阶段提示。member 通过 `send_message(recipient="main")` 交接，协调者校验并 `finish`。只有当前主会话内的失败重试才复用当前 Team 的 member ID；显式 resume 一律按第 8 条创建新 Team，不得联系旧 member。
6. SUMMARY 与 REQUIREMENT 一样由主 Agent执行，不调用 `task`。
7. 阶段成功完成后向当前 member 发送 `shutdown_request`。SUMMARY 完成后删除当前 Team；只处理状态或工具返回的精确 Team 名，不枚举其他 Team。
8. CodeBuddy 的用户入口固定为 `/devflow resume <slug>` 或 `/devflow 继续 <slug>`，内部调用 `resume --project-root . --slug <slug> --fresh-team`。返回 `rotate-team` 时，先对 `delete_team_name` 执行第 2 条的直接删除，再执行返回的 `rotate-team` 命令并创建新 Team；旧 in-progress 阶段会恢复为 pending。返回 `restart-required` 时，也只删除返回的 `delete_team_name`，随后用一句话说明状态文件缺失、无法 resume，要求用户重新提交原始需求；禁止继续检查或重复调用 resume。

只读检索助手先生成有边界的完整提示，再把该提示直接作为 `Task(..., team_name=<team_name>, subagent_name="devflow-research-helper", prompt=<完整检索提示>)` 的初始 Prompt；不得先发“待命”再二次派发。随后通过 `helper --executor-type devflow-research-team-member --dispatch-tool task --team-name <team_name>` 登记。完成后它向 `main` 返回事实并接受关闭；不得创建无 `team_name` 的 helper subagent。

除第 2 条已定义的精确目录删除降级外，任一 Team 生命周期调用失败时停止并报告，不得静默降级成 subagent 或 single-context。通用 `agents/` 保存逻辑角色，CodeBuddy 专有目录只保存两个宿主执行器和一个命令适配。

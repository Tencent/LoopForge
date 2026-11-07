# 运行时降级

仅在 Python 或脚本执行不可用时读取。使用宿主的文件读写和 Git 能力执行与脚本相同的流程。

## 状态操作

初始化前确认 `artifacts/<slug>/` 不存在；已存在时不得覆盖，恢复旧任务或更换 slug。复制 [workflow-state.json](../templates/workflow-state.json) 到 `artifacts/<slug>/workflow-state.json`，填入时间、当前目录、规模、模式和 [workflow.json](../config/workflow.json) 中对应 route；非 route 阶段标记 `skipped`。设计稿任务将 `requires_design_artifacts` 设为 `true`。medium/large 将 `execution_mode` 设为 `isolated`，并按 [runtime-core.md](runtime-core.md) 选择 `host_adapter`；仅在宿主不能创建独立 Agent 时设为 `single-context`，同时记录并向用户展示 `fallback_reason`。创建产物合同中的目录。

每次修改状态前复制为 `workflow-state.json.bak`，然后完整写入新 JSON：

1. `prepare`：仅允许 `stage == next_stage`、无待批准且阶段为 `pending|failed`。先记录必需产物哈希，再复制缺失模板并记录 `prepared_at`；不得覆盖已有产物。
2. `assign`：REQUIREMENT、SUMMARY 直接登记协调者身份，不创建或分配阶段 Agent；其他 isolated 阶段在 prepare 后创建新 Agent，登记固定逻辑角色、adapter 执行器类型、拓扑和宿主返回的 executor ID。Team adapter 还必须登记状态中的同一个 `team_name`；只读检索 Agent 同理。禁止将普通 subagent 冒充 Team member，也禁止将 reviewer 用于 REVIEW 前调研。
3. `start`：必须已 prepare；isolated 模式还必须已有合法 assign。改为 `in_progress` 并记录事件和时间。
4. `completed`：确认必需产物相对 prepare 基线为新建或变化，再检查未填占位、空内容和关键语义；设计稿任务额外检查设计上下文；通过后移动到下一阶段。
5. `failed`：失败计数加一并清除 prepare 基线；达到 2 次改为 `blocked`。REVIEW 失败回到 IMPLEMENT，其他阶段重试自身。
6. `overflow`：仅 SOLO 可用；升级为 medium isolated 路由，下一阶段设为 REQUIREMENT。
7. `manual`：DESIGN、REVIEW、TEST 完成后等待用户批准。
8. SUMMARY 完成前重新检查所有已完成阶段及产物，通过后才把 workflow 标记为 `completed`。

## 人工校验

- 计划：Scope、Tasks、Validation、Risks/Rollback 均有非空内容；Scope 含明确文件/目录；每个任务含目标文件；Validation 含可执行命令。
- 设计：overview、components、至少一个 section 均非空；section 含布局、内容、验证；evidence 至少有一张图片或 PDF。
- 报告：按模板检查未填写占位项、空正文、内容过少和关键语义；标题措辞差异不作为失败。
- Git 预检：确认当前目录、仓库根、分支、工作区状态和 remote；脏工作区不得自动清理。

无法可靠完成任一检查时将阶段标记为 blocked，不假定通过。

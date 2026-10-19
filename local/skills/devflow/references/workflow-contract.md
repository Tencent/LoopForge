# 流程合同

## 根目录与状态

完整流程在当前目录使用 `artifacts/<task-slug>/`。`task-slug` 只能包含字母、数字、点、下划线和连字符；未提供时先根据任务生成并向用户展示。

使用 Python 3.8+ 执行本 Skill 自带的 `scripts/workflow_state.py`；脚本仅依赖标准库。以下命令中的 `<PYTHON>` 优先取 `python3`，只有确认版本满足要求时才取 `python`：

- 初始化：`<PYTHON> <SKILL_ROOT>/scripts/workflow_state.py init --project-root . --slug <slug> --size <small|medium|large> --mode <auto|manual> [--require-design-artifacts] [--execution-mode <isolated|single-context>] [--host-adapter <adapter-id>]`
- 准备阶段：`prepare --state artifacts/<slug>/workflow-state.json --stage <stage>`
- 准备并生成阶段提示：`<PYTHON> <SKILL_ROOT>/scripts/workflow_state.py prepare --state ... --stage <stage> --emit-prompt [--request <用户目标>] [--input <额外路径>] [--profile <确认适用的专项规则>]`。这是一条调用；只有恢复一个已经 prepare 的阶段时才单独运行 `build_stage_prompt.py`。下游 prompt 以已确认上游产物为交接，不再重复嵌入原始需求文本；原始需求仍只用于专项规则匹配。
- 登记阶段执行者：`assign --state ... --stage <stage> --role <required-role> --executor-type <adapter-stage-executor> --executor-id <host-agent-id> --dispatch-tool <adapter-dispatch-tool> [--team-name <workflow-team>]`（协调者阶段不执行；Team adapter 必须传状态中的 team_name）
- 登记只读检索助手：`helper --state ... --stage <stage> --role <helper-role> --executor-type <adapter-research-executor> --executor-id <host-agent-id> --purpose <bounded-purpose> --dispatch-tool <adapter-dispatch-tool> [--team-name <workflow-team>]`
- 开始阶段：`start --state artifacts/<slug>/workflow-state.json --stage <stage>`
- 结束阶段：`finish --state ... --stage <stage> --result <completed|failed|overflow>`
- 用户/手动门禁批准：`approve --state ... --stage <stage> [--user-confirmed]`。REQUIREMENT 必须在用户明确确认需求报告后传 `--user-confirmed`；不得由 Agent 自行推断确认。
- 恢复建议：`resume --project-root . --slug <slug> [--fresh-team]`；也兼容 `resume --state ...`（只读返回下一动作，不重新初始化）
- 升级旧 CodeBuddy 状态：`upgrade-team --state ...`（仅 policy v2 且尚未登记/执行任何中间阶段 Agent 时允许，原地切换为 Team v3）
- 更换 Team 所有者：`rotate-team --state ...`（Team 拓扑恢复时直接放弃旧 executor 绑定，把旧 in-progress 阶段恢复为 pending，并返回新 Team 名）
- 检查：`status|validate --state ...`

同一 slug 已存在时 `init` 拒绝覆盖；恢复任务对现有状态运行 `resume`，按返回的 action 继续，禁止重新 `init`。设计稿任务传 `--require-design-artifacts`。medium/large 默认 `isolated`，必须选择与真实生命周期工具匹配的 `--host-adapter`。

Team 拓扑的 `team_name` 是状态机在初始化时根据 task slug 和随机 `run_id` 分配的运行实例标识。调用方只能使用状态文件返回的完整名称，不得仅按 slug 重建名称或附着到历史同名 Team；同一 slug 的新流程与旧 Team 必须拥有不同名称。
在另一个 CodeBuddy 主会话中恢复时使用 `resume --fresh-team`。返回结果会给出 `delete_team_name`：直接删除这个精确 Team，不读取其配置、成员、inbox 或状态，再调用 `rotate-team` 并创建新 Team。状态文件不存在时返回 `restart-required`，删除返回的旧 Team 名后停止恢复，不重复尝试。

## 固定路由

```text
small:  PHASE-0 -> SOLO -> SUMMARY
medium/large: PHASE-0 -> REQUIREMENT -> DESIGN -> IMPLEMENT -> REVIEW
              -> TEST -> [KNOWLEDGE] -> SUMMARY
```

`SOLO` 内仍须完成计划、实现、自审和针对性测试，只是合并为一个阶段。宿主支持子 Agent 时可按职责委派；否则顺序执行。阶段累计失败 2 次后停止自动流转。

规模判断使用影响范围而非代码行数：单模块且预计不超过 3 个文件、无接口/数据/架构影响为 small；跨模块或约 4–10 个文件为 medium；更大范围或涉及接口、数据模型、迁移、架构及高风险发布为 large。`SOLO` 中发现超限时以 `overflow` 结束并升级到 medium 路由。

REQUIREMENT 在 `auto` 和 `manual` 模式下都必须等待用户确认需求报告，确认前不得创建 DESIGN Agent。除此之外，`auto` 连续流转；`manual` 还在 DESIGN、REVIEW、TEST 完成后设置等待批准。REVIEW 失败回到 IMPLEMENT 修复，其他阶段失败重试当前阶段。

## 产物合同

```text
artifacts/<task-slug>/
├── workflow-state.json
├── 01-requirement/requirement-report.md
├── 02-design/tech-design.md
├── 02-design/execution-plan.md
├── 02-design/design-context/           # 仅设计稿任务
├── 03-code/change-report.md
├── 03-code/api-docs.md                  # IMPLEMENT 有 API 变动时
├── 03-code/review-report.md
├── 04-test/test-report.md
├── 05-knowledge/knowledge-report.md
├── 01-solo/solo-report.md               # 仅 small 路由
├── 01-solo/api-docs.md                   # SOLO 有 API 变动时
└── workflow-summary.md
```

每个阶段只接收 manifest 声明的精确产物。提示生成器从已验证产物中确定性提取当前角色所需章节作为精简交接；Agent 默认不再打开上游全文，只有交接缺失或矛盾时才读取源文件。

[templates/manifest.json](../templates/manifest.json) 是固定产物和条件蓝图的唯一事实源；实际模板位于 `templates/`。`prepare` 只复制固定产物，不覆盖已有文件。HTTP API 有变动时，执行者按 manifest 中的 `api-docs-*` 蓝图生成固定路径的 `api-docs.md`。阶段执行者删除所有 `{{...}}`，新增产物必须登记 manifest。

校验器阻断缺失的合同章节、未填写占位项、空内容、内容过少和关键语义错误；不为标题样式或排版偏好增加额外门禁。

## 阶段门禁

- `REQUIREMENT`：范围、排除项、验收标准和未决问题明确；内容校验通过后向用户展示摘要并进入强制确认门禁。
- `DESIGN`：方案、影响文件、风险、回滚和验证命令明确；`finish` 自动运行执行计划校验。
- `IMPLEMENT`：每批最多 3 个计划任务；本批验证通过后才标记完成。
- `REVIEW`：阻断问题为零；否则回到 `IMPLEMENT`。实际 diff 有 HTTP API 变动时，接口文档缺失或与契约不一致按 P1 阻断。
- `TEST`：复用 DESIGN 盘点的项目现有测试能力。已有 E2E 且改动落入其覆盖边界时补齐相应用例；没有时不强制引入。可补测试但不得修改生产代码，并区分失败、阻塞和未运行。
- `KNOWLEDGE`：条件阶段。TEST 后先执行知识价值判断；新增模块/接口约束、意外失败模式、诊断方法、兼容/数据/安全/运维坑或可复用恢复方式时运行。只有常规结果、一次性过程或已有文档已覆盖时，使用 `decide-knowledge --decision skip --reason ...` 跳过。不得按 size 决定。
- `SUMMARY`：`finish` 自动重新验证实际 route 中所有已完成阶段及必需产物，不混用 small 和 medium/large 合同。

每次 `prepare` 记录必需产物基线。`finish --result completed` 只接受 prepare 后新建或变化的产物，不能用旧报告直接过门禁。

外部写操作仍必须获得用户明确授权，不能由状态机自动触发。

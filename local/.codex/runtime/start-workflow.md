# Codex DevFlow 启动协议

主 Codex 线程作为 orchestrator，通过 `workflow-state.json` 驱动阶段流转。

## Step 0：读取配置

读取：

1. `.codex/assets/devflow.defaults.yaml`
2. `.codex/runtime/workflow-state-spec.md`
3. `.codex/rules/global.mdc`

可选读取 `.codex/workflows/devflow.md` 做恢复检查。禁止在运行期读取
`.codebuddy/` 作为 Codex 工作流来源。

## Step 1：工作区初始化

1. 从需求生成 `TASK_SLUG`。
2. 创建 `artifacts/{TASK_SLUG}/`。
3. 创建阶段目录：
   - `01-requirement/`
   - `02-design/`
   - `03-code/`
   - `04-e2e/`
   - `05-knowledge/`
   - `01-solo/`
4. 从 `.codex/assets/workflow-state-template.json` 创建 `workflow-state.json`。
5. 写入 `task_id`、`task_slug`、`workspace_root`、`artifacts_dir`、`created_at`、`updated_at`。

## Step 2：Phase 0

主线程直接完成：

1. 解析需求标题和描述。
2. 扫描相关代码、协议、文档、测试入口。
3. 按代码改动量、影响模块数、风险等级判定 `size_class`。
4. 保存 Phase 0 结果：
   - small：写入 `01-requirement/requirement-report.md`，记录原始需求和轻量理解，供 solo-developer 使用
   - medium/large：只写 `01-requirement/phase0-scan.md`；不得提前创建 `requirement-report.md`，避免被误判为 TASK-01 已完成
5. 更新 `workflow-state.json.last_event = "workflow_initialized"`。

## Step 3：路由

- `small`：进入 SOLO，由 `solo-developer` 规则完成实现、验证、总结；
  small 路径不执行 TASK-01。
- `medium` / `large`：进入 Step 3.5，由主线程执行 TASK-01 需求分析与澄清，
  再进入 TASK-02 技术方案。
- 如果用户明确要求“先给方案，不改代码”，在 TASK-02 或 TASK-03 前暂停，等待确认。

## Step 3.5：TASK-01 需求分析与澄清

主 Codex 线程直接执行，禁止委托给 subagent，因为该阶段需要和用户多轮对话。

0. 代码调研委托 explorer 只读 agent（结果不进主线程上下文）⚡⭐：
   - 主线程**不亲自**大规模 read/search 代码库，而是通过 `multi_agent_v1.spawn_agent`
     派发一个 explorer 类型的只读调研 agent，仅回收 **≤500 字**结构化摘要作为
     brainstorming 输入——避免一次性调研结果在后续 6+ 次派发中被反复重放。
   - 调研 prompt 必含四段输出：相关模块与文件；影响面（模块/服务/接口/数据库表）；
     技术约束与风险；初步实现方向（1–2 条）。
   - 调研约束：read/search 总计 ≤15 次；优先语义搜索；单文件只读关键片段
     （≤200 行）；只返回摘要，禁止回传大段源码。
   - 禁止委托的是下述 brainstorming 多轮对话本身，调研委派不受此限。
1. 必须调用或显式使用 `superpowers:brainstorming` skill：
   - 优先使用 Codex 已发现的 `$superpowers:brainstorming` /
     `$superpowers-brainstorming` skill。
   - 如果当前会话没有注入该 skill，读取
     `.codex/skills/superpowers/brainstorming/SKILL.md` 并完整执行其中
     `DevFlow Override`。
   - medium/large 不允许直接跳过 TASK-01，也不允许以“需求已足够明确”、
     “无需追加澄清”为由自动完成 TASK-01。
2. 注入 devflow 覆盖约束：
   - 输出路径固定为 `artifacts/{TASK_SLUG}/01-requirement/requirement-report.md`
   - 不写入 `docs/superpowers/specs/`
   - 不提交 git
   - 不调用 `writing-plans`
   - 需求报告必须包含 AI 代码/上下文分析、澄清 Q&A、收敛结论、纳入范围、
     排除范围、成功标准、风险和待确认问题
3. 需求澄清必须一问一答推进。即使已有用户输入看起来足够完整，也必须向用户
   发起一次明确的澄清或确认：
   - 有歧义时，提出一个具体澄清问题，等待用户回答。
   - 无明显歧义时，复述需求边界、成功标准和排除范围，询问用户是否确认。
   - 用户明确回复确认前，禁止写入 `TASK-01_completed`，禁止路由到 TASK-02。
4. 用户确认后，先把澄清 Q&A 和确认结论写入 requirement-report.md，再更新
   `workflow-state.json`：
   - `current_stage = "TASK-01"`
   - `stages["TASK-01"].status = "completed"`
   - `stages["TASK-01"].artifact_path = "{artifacts_dir}/01-requirement/requirement-report.md"`
   - `last_event = "TASK-01_completed"`
   - `next_target = "architect"`
5. 之后按 routing table 进入 TASK-02。

## Step 3.6：上下文压缩检查点（进入派发循环前必做）⭐

代码调研摘要 + brainstorming 多轮对话的产物已**全部持久化**到
`01-requirement/requirement-report.md`，其在主线程会话中的详细历史对后续
派发循环已无价值。进入 Step 4 前主动压缩，让派发循环从**轻量、干净**的
上下文起步，避免一次性重上下文随 6+ 次派发反复重放。

触发条件：`config.multi.compact_after_brainstorming == true`（默认 true）。

1. **若运行时支持显式压缩**（如 `/compact`）→ 执行压缩，仅保留下述【派发交接卡】。
2. **若不支持** → 主线程显式输出并锚定【派发交接卡】，此后派发循环**仅依赖
   它 + workflow-state.json + requirement-report.md（按需回读）**，
   不再引用调研/对话原文。

```
【派发交接卡】（≤200 字，派发循环唯一需要的常驻上下文）
- task_id / task_slug / size_class
- artifacts_dir / workflow_state_path
- requirement_report: 01-requirement/requirement-report.md（需求细节全在此，下游自读）
- 当前：last_event = "TASK-01_completed" / next_target = "architect"
```

> ⛔ 下游角色本就从 `requirement-report.md` 读取需求细节，主线程无需保留
> brainstorming 对话原文。

## Step 4：Codex multi-agent 派发

TASK-02 及之后的角色阶段必须通过 Codex `multi_agent_v1.spawn_agent`
派发。主 Codex 线程只负责准备上下文、派发任务、接收结果、合并产物和更新
`workflow-state.json`。

### 4.1 派发规则

| 阶段 | Codex agent type | 角色文件 | 产物 |
|------|------------------|----------|------|
| TASK-02 | explorer | `.codex/agents/architect.md` | `02-design/tech-design.md`, `02-design/execution-plan.md` |
| TASK-03 | worker | `.codex/agents/developer.md` | `03-code/change-report.md`, `03-code/api-docs.md` |
| CODE-REVIEW | explorer | `.codex/agents/code-reviewer.md` | `03-code/code-review-report.md` |
| TASK-04 | explorer | `.codex/agents/test-engineer.md` | `04-e2e/test-report.md` |
| TASK-05 | explorer | `.codex/agents/knowledge-engineer.md` | `05-knowledge/knowledge-report.md` |
| final summary（仅 medium/large） | explorer | `.codex/agents/leader.md` | `workflow-summary.md` |

### 4.2 派发 prompt 必含内容

主线程派发每个 agent 时必须包含：

1. 当前 `workflow-state.json` 路径。
2. 当前阶段名称和目标产物路径。
3. 对应 `.codex/agents/<role>.md` 的职责摘要。
4. 必要的 `.codex/rules/` 和 checklist。
5. “你不是唯一 agent，不要回滚或覆盖其他 agent/用户改动”的协作约束。
6. 对 worker 阶段，必须声明文件所有权边界。
7. 阶段报告格式约束：完成时必须返回**紧凑结构化报告**（≤500 字），必含
   `event`、`stage`、`status`（passed/failed/blocked/not-run）、`next_target`、
   `artifact_paths` 和关键约束/决策摘要（1–3 条，引用而非内联全文）；
   禁止回传大段源码、完整工具日志。

### 4.3 状态记录

每次 `spawn_agent` 成功后，主线程必须在 `workflow-state.json.decisions[]`
追加记录：

```json
{
  "kind": "codex_agent_spawned",
  "stage": "TASK-02",
  "role": "architect",
  "agent_type": "explorer",
  "agent_id": "<spawn_agent returned id>"
}
```

### 4.4 降级规则

只有工具清单中不存在 `multi_agent_v1.spawn_agent`，或调用该工具返回
“tool unavailable / not found” 类错误时，才允许主线程内联执行该阶段。
降级时必须写入 `decisions[]`：

```json
{
  "kind": "codex_agent_spawn_unavailable",
  "stage": "TASK-02",
  "role": "architect",
  "reason": "multi_agent_v1.spawn_agent not available in this session"
}
```

### 4.5 每个阶段执行同一协议

1. 读取 `workflow-state.json`（仅做「读→局部改→写」；`decisions[]` 只增不减、
   越读越大，写完勿让全量 JSON 长留上下文）。
2. 将当前阶段状态写为 `in_progress`。
3. `spawn_agent` 派发对应角色。
4. 等待 agent 返回阶段报告。
5. 检查产物是否存在且路径正确。
6. 写回阶段状态、`last_event`、`next_target` 和 `updated_at`。
7. 主线程按 `.codex/assets/devflow.defaults.yaml.routing_table` 结合阶段报告中的
   `event` 继续调度；仅在报告与状态校验不符、失败重试计数判断、中断恢复时
   才回读全量 JSON 复核。

## Step 5：恢复

`$devflow-codex resume TASK_SLUG`：

1. 读取 `artifacts/{TASK_SLUG}/workflow-state.json`。
2. 根据 `current_stage`、`last_event`、各阶段 `status` 判断恢复点。
3. 保留现有产物，不重置 `task_slug`。
4. 从最近未完成阶段继续。

## Step 6：状态

`$devflow-codex status TASK_SLUG`：

1. 读取 `workflow-state.json`。
2. 汇总当前阶段、最近事件、下一目标、已产物、阻塞项。
3. 不修改文件。

## Step 7：完成

流程完成时：

1. medium/large 由 leader 生成 `workflow-summary.md`；small 使用 solo-developer 已生成的总结。
2. 更新 `summary.status = "completed"`。
3. 保留全部中间产物和 `workflow-state.json`。
4. 向用户汇总改动、验证、风险和后续建议。

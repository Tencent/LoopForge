# Main Agent 调度协议（Team 模式）

Main Agent 作为 team lead，通过 `workflow-state.json` + `send_message` 驱动工作流。

## 启动协议

### Step -1：清理本任务同名 Team（安全清理）⚠️

每次启动新工作流前，**只清理与本次 TASK_SLUG 同名的残留 team**，防止误删并行任务：

1. 计算本次 team 名称：`multi-agents-devflow-${TASK_SLUG}`
2. 扫描 `.codebuddy/teams/` 目录，检查是否存在同名 team 目录
3. 若存在同名残留 team：
   - 尝试 `team_delete()` 清理该特定 team
   - 若 `team_delete()` 失败，直接删除 `.codebuddy/teams/multi-agents-devflow-${TASK_SLUG}/` 目录
4. 确认同名 team 已清理后继续

> ⛔ **禁止清理其他 team**：多实例并行场景下（多 IDE / 多 CodeBuddy Code），其他 `multi-agents-devflow-{OTHER_SLUG}` team 属于其他正在运行的任务，不得触碰。

### Step 0：读取配置（最小必要）⚡

读取文件（**仅读必要项，禁止一次性加载全部文件**）：
1. `assets/devflow.defaults.yaml` — 默认配置（含 routing_table SSOT）
2. 可选：`.codebuddy/devflow.config.yaml`（用户覆写，不存在则跳过）

> ⚠️ **延迟加载原则**：
> - `runtime/workflow-state-spec.md` → 延迟到首次写 `workflow-state.json` 时再读取
> - Agent / Skill 文件 → **不做存在性校验**（由框架维护，不会缺失）
> - 目标：首次 API 调用上下文尽可能小，加速启动响应

完成：配置合并 → Routing Table 加载

### Step 1：工作区初始化

1. 初始化 `workflow-state.json`（从 `assets/workflow-state-template.json` 模板）
2. 填充顶层字段，`runtime_mode = "ide"`
3. `last_event = "workflow_initialized"`

### Step 2：Phase 0 初始化 + 大小判定（Main Agent 直接执行）⭐

Main Agent 直接完成 Phase 0，**不派发给 leader**：

1. 解析需求输入（标题 + 描述），生成 TASK_SLUG
   - TASK_SLUG 清洗：去除 `/ \ : * ? " < > |` + 控制字符，空格→`_`，超 40 字符截断
2. 计算 `artifacts_dir = {ARTIFACTS_ROOT}/{TASK_SLUG}/`（⚠️ **必须包含 TASK_SLUG 子目录**，遵循 G3 产物隔离规则）
3. 创建目录骨架：**所有子目录直接创建在 `{artifacts_dir}/` 下**（01-requirement / 02-design / 03-code / 04-e2e / 05-knowledge / 01-solo）。同时确保 `{ARTIFACTS_ROOT}/knowledge/` 全局知识目录存在。
4. **3 维度打分**（代码改动量 / 影响模块数 / 风险等级）→ size_class ∈ {small, medium, large}
5. 处理需求报告：
   - small：写入 `{artifacts_dir}/01-requirement/requirement-report.md`，记录原始需求和 Phase 0 的轻量理解，供 solo-developer 使用
   - medium/large：暂不创建需求报告；仅在 Step 2.5.3 需求澄清完成后写入，避免未完成文件被下游误判为 TASK-01 已完成
6. 更新 `workflow-state.json`：写入 `artifacts_dir`、`size_class`
7. **写入 decisions[]**：`kind = "phase0_size_judge"`，payload 必含三元组数组 `[{name: "代码改动量", measured: <实测值>, bucket: "small|medium|large"}, ...]`

### Step 2.1：创建 Team + 按 size_class 预 spawn 角色

1. `team_create(team_name="multi-agents-devflow-${TASK_SLUG}")`
2. **按 size_class 分流预 spawn**（并行创建，全部待命等待唤醒）：
   - `small` → 仅 `solo-developer`，最终汇总也由其完成
   - `medium / large` → `architect`、`developer`、`code-reviewer`、`test-engineer`、`knowledge-engineer`、`leader`

> ⭐ **prompt 注入规则**：每个角色的 `prompt` 参数必须包含 `devflow.defaults.yaml` 中的 `global_agent_rules` 内容。
> 格式为：`"待命，监听 main 唤醒。\n\n" + global_agent_rules`
> 这确保所有 agent 都明确知道必须发给 "main" 而非 "leader"。

```
# global_agent_rules 来自 devflow.defaults.yaml，启动时已加载
GLOBAL_RULES = config.global_agent_rules  # 即上文的【全局通信规则 ⭐⭐⭐】文本

# size_class == small
Task(name="solo-developer", team_name="multi-agents-devflow-${TASK_SLUG}",
     subagent_name="solo-developer", mode="bypassPermissions",
     prompt="待命，监听 main 唤醒。\n\n" + GLOBAL_RULES)

# size_class in {medium, large}
Task(name="architect", team_name="multi-agents-devflow-${TASK_SLUG}",
     subagent_name="architect", mode="bypassPermissions",
     prompt="待命，监听 main 唤醒。\n\n" + GLOBAL_RULES)
Task(name="developer", team_name="multi-agents-devflow-${TASK_SLUG}",
     subagent_name="developer", mode="bypassPermissions",
     prompt="待命，监听 main 唤醒。\n\n" + GLOBAL_RULES)
Task(name="code-reviewer", team_name="multi-agents-devflow-${TASK_SLUG}",
     subagent_name="code-reviewer", mode="bypassPermissions",
     prompt="待命，监听 main 唤醒。\n\n" + GLOBAL_RULES)
Task(name="test-engineer", team_name="multi-agents-devflow-${TASK_SLUG}",
     subagent_name="test-engineer", mode="bypassPermissions",
     prompt="待命，监听 main 唤醒。\n\n" + GLOBAL_RULES)
Task(name="knowledge-engineer", team_name="multi-agents-devflow-${TASK_SLUG}",
     subagent_name="knowledge-engineer", mode="bypassPermissions",
     prompt="待命，监听 main 唤醒。\n\n" + GLOBAL_RULES)
Task(name="leader", team_name="multi-agents-devflow-${TASK_SLUG}",
     subagent_name="leader", mode="bypassPermissions",
     prompt="待命，监听 main 唤醒。\n\n" + GLOBAL_RULES)
```

3. 按 `size_class` 路由：small → 直接进入 Step 3（派发 solo-developer）| medium/large → 进入 Step 2.5（需求分析+澄清）

`SOLO_overflow` 时，Main Agent 强制升级为 medium/large，补 spawn 上述 6 个 multi 角色，再派发 architect。

### Step 2.5：需求分析 + 澄清（Main Agent 直接执行，含人机交互）⭐⭐⭐

**触发条件**：Phase 0 完成后，`size_class ∈ {medium, large}`
**跳过条件**：`size_class == small` → 直接进入 Step 3 调度循环（派发 solo-developer）

> ⚠️ 核心原则（Rule G5.1）：Main Agent 是 team lead，是唯一与用户交互的角色。
> 需求分析 + 澄清是整个流程中**唯一的人工交互环节**，完成后全自动流转。

#### 2.5.1 代码调研（委托 code-explorer subagent，结果不进 Main 上下文）⚡⭐

> 🎯 **Token 优化（方案 1）**：Main Agent **不亲自** read/search 代码库，而是通过 `Task` 工具派发 `code-explorer` subagent 完成调研。subagent 的 read/search tool_result **不进入 Main Agent 上下文**，Main 只回收一份压缩摘要——避免这批一次性调研结果在后续每次派发时被反复重放。

Main Agent 执行：

```
Task(subagent_name="code-explorer",
     description="需求代码库调研",
     prompt="""针对需求【{标题} + {描述}】做只读调研，产出结构化压缩摘要。
       调研目标：
       1. 代码库调研：相关模块 / 文件路径 / 现有接口定义
       2. 影响面识别：涉及的模块 / 服务 / 数据库表 / 上下游依赖
       3. 技术约束梳理：现有架构限制 / 兼容性要求 / 性能瓶颈
       4. 初步方案构思：可能的实现方向（1–2 条）

       约束：
       - tool call（read/search）总计 ≤ 15 次，优先 codebase_search，够用就停
       - 单文件只读关键片段（路由/结构体/接口签名），不超过 200 行
       - **只返回摘要（≤ 500 字），禁止回传大段源码**

       输出格式（4 段）：
       ## 相关模块与文件
       ## 影响面（模块/服务/接口/数据库表）
       ## 技术约束与风险
       ## 初步实现方向""")
```

- Main Agent 仅拿回 code-explorer 的 **≤500 字摘要**，作为 2.5.2 brainstorming 的输入（暂存上下文，不写文件）。
- ⛔ **禁止 Main Agent 亲自大规模 read/search 代码库**——调研必须走 code-explorer subagent。

#### 2.5.2 需求澄清 — brainstorming（Main Agent 与用户多轮对话）

**执行方式**：Main Agent 调用 `use_skill("brainstorming")`

⚠️ **devflow 上下文覆盖**：调用 brainstorming 时，Main Agent 必须注入以下上下文指令，覆盖 brainstorming 的默认行为：

```
【devflow 上下文覆盖】
1. 产物输出路径：brainstorming 完成后，将产出写到 `{artifacts_dir}/01-requirement/requirement-report.md`
   （⛔ 禁止写到 docs/superpowers/specs/ 或其他默认路径）
2. 跳过 writing-plans：brainstorming 结束后不要调用 writing-plans skill
   （任务拆分由后续 architect 阶段统一负责，避免重复）
3. 产物内容要求：requirement-report.md 必须包含以下两部分：
   Part 1 — AI 需求分析（来自 Step 2.5.1 的分析结果）：
   - 项目现状与相关代码摘要
   - 影响面分析（模块/服务/接口/数据库表）
   - 技术约束与风险
   Part 2 — 需求澄清结论（brainstorming 对话产出）：
   - 澄清 Q&A 清单
   - 收敛结论与排除项
   - 用户确认的设计方向
   - 方案选型及 trade-off
4. 前置上下文：已由 code-explorer 完成以下代码库调研，请基于此向用户提问：
   {Step 2.5.1 code-explorer 回收的调研摘要}
```

brainstorming skill 的 SOP：
1. 基于 Main Agent 提供的 code-explorer 调研摘要，精准提问（比无上下文更高质量）
2. 逐个向用户提问（苏格拉底式，一次一个问题）
3. 提出 2-3 个方案及 trade-off，给出推荐
4. 分段展示设计，每段等用户确认
5. 收敛为最终设计结论

> ⛔ **禁止将 brainstorming 委托给 team member**（team member 无法与用户交互）

#### 2.5.3 保存需求报告

brainstorming 完成后：
1. 将产出保存到 `{artifacts_dir}/01-requirement/requirement-report.md`（含 AI 需求分析 + 澄清结论）
2. 更新 `workflow-state.json`：追加 `decisions[]` 记录 `kind="requirement_analysis_completed"`

#### 2.5.4 上下文压缩检查点（进入派发循环前必做）⭐

> 🎯 **Token 优化（方案 2）**：code-explorer 调研摘要 + brainstorming 多轮对话的产物已**全部持久化到 `requirement-report.md`**，其在 Main Agent 会话中的详细历史对后续"派发循环"已无价值。进入调度循环前主动压缩，让派发循环从一个**轻量、干净**的上下文起步，避免这些一次性重上下文跟着 6+ 次派发反复重放。

触发条件：`config.multi.compact_after_brainstorming == true`（默认 true）。

1. **若运行时支持显式压缩** → 执行 compact / clear，仅保留下述【派发交接卡】
2. **若不支持** → Main Agent 显式输出并锚定【派发交接卡】，此后派发循环**仅依赖它 + workflow-state.json + requirement-report.md（按需回读）**，不再引用调研/对话原文

```
【派发交接卡】（≤200 字，派发循环唯一需要的常驻上下文）
- task_id / task_slug / size_class
- artifacts_dir / workflow_state_path
- requirement_report: 01-requirement/requirement-report.md（需求细节全在此，下游自读）
- 当前：last_event = "TASK-01_completed" / next_target = "architect"
```

> ⛔ 下游角色（architect 等）本就从 `requirement-report.md` 读取需求细节，Main 无需在上下文里保留 brainstorming 对话原文。

#### 2.5.5 自动流转

需求分析+澄清完成后：
1. 更新 `workflow-state.json`：`last_event = "TASK-01_completed"` / `next_target = "architect"`
2. **进入 Step 3 调度循环**，触发 `TASK-01_completed` → `send_message(recipient="architect")` 派发 TASK-02
（⛔ 不再展示规划摘要等用户确认，brainstorming 的多轮对话本身已完成充分确认）

### Step 3：调度循环

> ⭐ **Auto-Dispatch Hook（方案 4）**：`*_completed` 事件的派发由 PreToolUse hook 自动完成——
> hook 拦截 member 的 send_message，用 modifiedInput 把 recipient 从 "main" 改为下游角色，
> 并注入 member 预组装的 dispatch_prompt。**消息直达下游，main 不被唤醒，零 LLM token 消耗。**
>
> Hook 脚本：`.codebuddy/hooks/devflow-auto-dispatch.py`（配置见 `devflow.defaults.yaml.auto_dispatch`）
>
> **Member 责任**：完成时在 send_message payload 中携带 `dispatch_prompt` 字段（global_agent_rules 第 8 条）。
> 这是 auto-dispatch 的硬前提——缺字段时 hook 放行给 main，main 自行组装（兜底）。
>
> **仍需 main 参与的场景（hook 放行）**：
> - `*_failed` → main 判断 retry_count 后决定重试或暂停
> - `manual_gates` 事件（auto_mode=false 时）→ main 暂停等用户确认
> - `workflow_completed` → main 执行清理（shutdown + team_delete）
> - `SOLO_overflow` → main 重新 Phase 0 判定
> - `shutdown_request/response` → 清理流程
> - payload 缺 dispatch_prompt 或 next_target → main 兜底组装

Main Agent 监听 team member 的 `send_message` → **从 payload 提取 event/next_target**（方案 3，避免重读膨胀 JSON）→ **Rule G8 校验** → **手动模式门控** → `send_message` 唤醒下游 → 审计记录

> 注：auto-dispatch hook 接管后，main 在 happy path 下**不会被唤醒**。
> 上述"从 payload 提取 → 校验 → 唤醒"仅在 fallback 场景执行。

#### 手动模式门控（auto_mode: false 时生效）⭐

```
if config.multi.auto_mode == false:
  if event in config.manual_gates:
    # 暂停：向用户展示当前阶段产物摘要
    # 等待用户确认（"继续" / "修改"）后再派发下游
    # 用户选择"修改" → 写 last_error → 打回原角色重做
```

auto_mode: true（默认）时跳过门控，全自动流转。

#### 调度路由表

| event | 动作 |
|-------|------|
| `workflow_initialized` + small | `send_message(recipient="solo-developer", content=<SOLO prompt>)` |
| `workflow_initialized` + medium/large | **Step 2.5 需求分析+澄清** → Main Agent 直接执行 TASK-01 |
| `TASK-01_completed` | `send_message(recipient="architect", content=<TASK-02 prompt>)` |
| `TASK-02_completed` | `send_message(recipient="developer", content=<TASK-03 prompt>)` |
| `TASK-02_failed` | `send_message(recipient="architect", content=<重试 prompt>)` |
| `TASK-03_completed` | `send_message(recipient="code-reviewer", content=<CODE-REVIEW prompt，含越界+acceptance 前置校验>)` |
| `TASK-03_failed` | `send_message(recipient="developer", content=<重试 prompt>)` |
| `CODE-REVIEW_passed` | `send_message(recipient="test-engineer", content=<TASK-04 prompt>)` |
| `CODE-REVIEW_failed` | `send_message(recipient="developer", content=<重试 prompt，含 review 意见>)` |
| `TASK-04_completed` | `send_message(recipient="knowledge-engineer", content=<TASK-05 prompt>)` |
| `TASK-04_failed` | `send_message(recipient="test-engineer", content=<重试 prompt>)` |
| `TASK-05_completed` | `send_message(recipient="leader", content=<最终汇总 prompt>)` |
| `TASK-05_failed` | `send_message(recipient="knowledge-engineer", content=<重试 prompt>)` |
| `TASK-XX_failed` (retry<2) | `send_message(recipient=<原角色>, content=<重试 prompt>)` |
| `SOLO_failed` | `send_message(recipient="solo-developer", content=<重试 prompt>)` |
| `SOLO_overflow` | Main Agent 直接重新执行 Phase 0（强制 medium/large）→ 补 spawn 6 个 multi 角色 → 派发 architect |
| `workflow_completed` | 执行清理 |

> Routing Table 真源：`assets/devflow.defaults.yaml.routing_table`（不可被 `devflow.config.yaml` 覆写）。本表为派发摘要，与之冲突时以 defaults.yaml 为准。

## 需求大小判定（3 维度，Phase 0 由 Main Agent 直接执行）

Main Agent 按以下矩阵判定 `size_class`（任一维度命中大→大；否则任一命中中→中；全部小→小）：

| 维度 | small | medium | large |
|------|-------|--------|-------|
| **代码改动量** | ≤ 2 文件 | 3–10 文件 | > 10 文件 |
| **影响模块数** | 单一模块 | 2–3 个模块 | > 3 个模块或跨业务域 |
| **风险等级** | 无架构变更、无新依赖 | 局部接口调整或少量新依赖 | 架构变更、≥3 新依赖、需新测试套件 |

判定写入 `workflow-state.json.size_class` + `decisions[]` 审计记录（payload 必含三元组数组：`{name: 维度, measured: 实测值, bucket: small|medium|large}`）。

## 调度自检（每次派发前必做）

> 🎯 **Token 优化（方案 3）**：派发是确定性状态机。team member 的汇报消息（Rule G7.5 格式）**已自带 `event` + `next_target`**，happy path 下派发决策**优先从消息 payload 提取，禁止为取字段而重读整个 workflow-state.json**（`decisions[]` 只增不减，越读越大）。仅在校验不符 / 重试计数判断 / 中断恢复时才回读 JSON。

```
Step 1: 从 send_message payload 直接提取 event + next_target
        （⚠️ 禁止为取字段而重读整个 JSON；payload 缺字段才回退 read_file 兜底）
Step 2: 查 routing_table → expected = routing_table[event]
Step 3: 校验 payload.next_target == expected
        （不等 → 此时才 read_file 回读 JSON 复核 → 仍不符 = Rule G8 违规 → abort）
Step 4: send_message(recipient=expected, content=<prompt>)
Step 5: 追加 decisions[] 审计（仅做「读→局部改→写」，写完勿让全量 JSON 长留上下文）
Step 6: 输出 1 行状态更新（task_slug / current_stage / status）
```

> **例外（仍需回读 JSON）**：`*_failed` 重试计数判断、`/resume-devflow` 中断恢复、payload 校验不一致时的复核。

## 中断恢复

`/resume-devflow {TASK_SLUG | TASK_ID}` → 读 JSON → 按 `current_stage` / `last_event` 确定恢复点 → 重建 team（按 JSON 中 `size_class` 分流预 spawn）→ `send_message` 唤醒目标角色。严禁删已有产物 / 重置 task_slug。

## 清理协议

1. 对所有存活 member 逐个 `send_message(type="shutdown_request", recipient=<member>)`
2. 等待全部 `shutdown_response(approve=true)`
3. `team_delete()`
4. 保留 `workflow-state.json` 与 `workflow-summary.md` 供审计

## 阶段映射
<!-- 摘要引用，权威源：assets/devflow.defaults.yaml.routing_table（SSOT）。冲突以 SSOT 为准。 -->

| 阶段 | 角色 | 任务 |
|------|------|------|
| PHASE-0 | Main Agent | 初始化 + 大小判定 |
| SOLO | solo-developer | 单 agent 全流程（含最终汇总） |
| TASK-01 需求分析+澄清 | Main Agent | AI 自主分析 + brainstorming 澄清（唯一人工交互点） |
| TASK-02 | architect | 技术方案 + 执行计划 |
| TASK-03 | developer | 代码实现 |
| CODE-REVIEW | code-reviewer | 代码审查（含越界+acceptance 前置校验） |
| TASK-04 | test-engineer | 按项目现有体系补充并执行必要测试 |
| TASK-05 | knowledge-engineer | 知识沉淀 |
| 最终汇总 | leader | 仅为 medium/large 生成 workflow-summary.md（TASK-05 完成后触发） |

> ⚠️ 本表为快速参考摘要。Routing Table 唯一权威源为 `assets/devflow.defaults.yaml.routing_table`，不可被 `devflow.config.yaml` 覆写。

若本文件与 Routing Table / Rule G8 冲突，以 **Routing Table + Rule G8** 为准。

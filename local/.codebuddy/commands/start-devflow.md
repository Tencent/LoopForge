---
description: 启动 devflow 多智能体开发工作流
argument-hint: [需求标题] | 需求描述
allowed-tools: ["use_skill", "task", "team_create", "send_message", "read_file", "write_to_file", "search_content", "list_dir", "execute_command", "ask_followup_question", "codebase_search"]
---

# /start-devflow

## 用法
```
/start-devflow {需求描述}
/start-devflow 需求标题：{标题}\n需求描述：{描述}
```

支持传入 `TASK_ID` 或 `TASK_SLUG`；未传 TASK_ID 时由 Main Agent Phase 0 自动生成。

## Main Agent 动作（严格顺序）

### 第零步：清理本任务同名 Team（安全清理）⚠️

启动新工作流前，**只清理与本次任务同名的残留 team**，防止误删其他正在运行的并行任务：

1. 解析本次任务的 `TASK_SLUG`（从参数或 Phase 0 生成）
2. 计算本次 team 名称：`multi-agents-devflow-${TASK_SLUG}`
3. 扫描 `.codebuddy/teams/` 目录，检查是否存在**同名** team 目录
4. 若存在同名残留 team：
   - 尝试执行 `team_delete()` 清理该特定 team
   - 若 `team_delete()` 失败，则直接删除 `.codebuddy/teams/multi-agents-devflow-${TASK_SLUG}/` 目录
5. 确认同名 team 已清理后，方可继续后续步骤

> ⛔ **禁止清理其他 team**：多个 IDE 实例或多个 CodeBuddy Code 进程可能同时运行不同任务，每个任务有独立的 team。
> 只有与本次 `TASK_SLUG` 完全匹配的 team 才能被清理，其余 team 不得触碰。

**异常处理**：
- 若 `.codebuddy/teams/` 目录不存在 → 跳过此步
- 若存在非 `multi-agents-devflow-` 前缀的目录 → 忽略（非 devflow team）
- 若存在其他 `multi-agents-devflow-{OTHER_SLUG}` 目录 → 不清理（属于其他任务）

### 第一步：解析参数 + 配置读取

1. **解析参数**：提取 title（可空）+ requirement（必填，空则终止）+ TASK_ID（可选）
2. **读取配置**（仅 `assets/devflow.defaults.yaml`；可选 `devflow.config.yaml`）

> ⛔ **禁止启动前校验 agent/skill 文件存在性**：这些文件由框架维护，不会凭空消失。
> 不必要的 read_file 会显著增加首次响应延迟。

### 第二步：Phase 0 初始化 + 大小判定（Main Agent 直接执行）

Main Agent 直接完成 Phase 0，**不派发给 leader**：
1. 解析需求输入，生成 TASK_SLUG
2. 创建 `artifacts_dir` 目录骨架
3. 3 维度打分 → `size_class` 判定
4. 初始化 `workflow-state.json`

### 第二步续：创建 Team + 按 size_class 预 spawn 角色

1. `team_create(team_name="multi-agents-devflow-${TASK_SLUG}")` 创建团队
2. 按 Phase 0 判定的 `size_class` 分流，一条 assistant message 内预 spawn 该模式所需角色：
   - `small` → spawn 1 角色：`solo-developer`（最终汇总由其自行完成，不 spawn leader）
   - `medium / large` → spawn 6 角色：`architect` / `developer` / `code-reviewer` / `test-engineer` / `knowledge-engineer` / `leader`（不 spawn `solo-developer`）
   - 每个角色的 prompt 必须注入 `devflow.defaults.yaml` 中的 `global_agent_rules`
   - 格式：`"待命，监听 main 唤醒。\n\n" + global_agent_rules`
   - `mode="bypassPermissions"`

> ⭐ `global_agent_rules` 确保所有 agent 明确知道：完成后必须 `send_message(recipient="main")`，严禁发给 "leader" 或其他 member。
> Phase 0 由 Main Agent 完成后直接继续，不等 leader。
> `SOLO_overflow` 时补 spawn 6 个 multi 角色，再派发 architect。

### 第三步：需求分析 + 澄清（Main Agent 直接执行）⭐⭐⭐

**触发条件**：`size_class ∈ {medium, large}`（small 跳过此步，直接进入第四步调度循环派发 solo-developer）

**执行流程**：

#### 3.1 AI 自主需求分析
Main Agent 自主完成（不与用户交互）：
1. 阅读项目结构、相关模块代码、现有接口定义
2. 识别需求涉及的模块、服务、数据库表、上下游依赖
3. 梳理技术约束、兼容性要求、架构限制
4. 分析结果暂存上下文，作为 brainstorming 输入

#### 3.2 需求澄清 — brainstorming
Main Agent 调用 `use_skill("brainstorming")`，注入 devflow 上下文覆盖：
- 产物输出到 `{artifacts_dir}/01-requirement/requirement-report.md`（⛔ 禁止写到默认路径）
- 跳过 writing-plans 调用（任务拆分由 architect 统一负责）
- 产物包含：AI 需求分析结果 + 澄清 Q&A + 收敛结论 + 排除项

#### 3.3 自动流转
完成后直接进入第四步调度循环，派发 architect 执行 TASK-02。
（⛔ 不再展示规划摘要等用户确认，brainstorming 多轮对话本身已完成充分确认）

### 第四步：进入调度循环

通过 `send_message(recipient=<role>)` 唤醒 team member，按 Routing Table（SSOT：`devflow.defaults.yaml`）驱动：

| event | 动作 |
|-------|------|
| `size_class == small` | `send_message(recipient="solo-developer", ...)` 唤醒 Solo |
| `size_class ∈ {medium, large}` | 第三步执行需求分析+澄清（TASK-01） |
| `TASK-01_completed` | `send_message(recipient="architect", ...)` 直接派发方案设计（TASK-02） |
| `TASK-02_completed` | `send_message(recipient="developer", ...)` 直接派发编码（TASK-03） |
| `TASK-02_failed` | `send_message(recipient="architect", ...)` 重试 |
| `TASK-03_completed` | `send_message(recipient="code-reviewer", ...)` 直接派发代码审查（越界+acceptance 前置校验） |
| `TASK-03_failed` | `send_message(recipient="developer", ...)` 重试 |
| `CODE-REVIEW_passed` | `send_message(recipient="test-engineer", ...)` 直接派发测试（TASK-04） |
| `CODE-REVIEW_failed` | `send_message(recipient="developer", ...)` 打回修复（含 review 意见） |
| `TASK-04_completed` | `send_message(recipient="knowledge-engineer", ...)` 直接派发知识沉淀（TASK-05） |
| `TASK-04_failed` | `send_message(recipient="test-engineer", ...)` 重试 |
| `TASK-05_completed` | `send_message(recipient="leader", ...)` 最终汇总 |
| `TASK-05_failed` | `send_message(recipient="knowledge-engineer", ...)` 重试 |
| `SOLO_failed` | `send_message(recipient="solo-developer", ...)` 重试 |
| `SOLO_overflow` | Main Agent 直接重新 Phase 0（强制 medium/large）→ 补 spawn 6 个 multi 角色 → 派发 architect |
| `workflow_completed` | 执行清理（shutdown_request → team_delete） |

> ⚠️ 无 leader 门禁审核：各阶段完成后直接流转下一阶段执行角色。leader 仅在流程末尾做最终汇总。

全流程自动流转，brainstorming 后不再有人工卡点。

中断后用 `/resume-devflow {TASK_SLUG}` 续跑。

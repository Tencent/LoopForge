# workflow-state.json 持久化状态规范

角色间上下文传递的唯一持久化通道。所有角色开始前必须读本文档。

## 文件定位

- **路径**：`{ARTIFACTS_ROOT}/{TASK_SLUG}/workflow-state.json`（简称 `{WORKFLOW_STATE_PATH}`）
- **ARTIFACTS_ROOT**：`devflow.defaults.yaml` 的 `artifacts.root_dir`（默认 `{project_root}/artifacts`）
- **artifacts_dir**：`{ARTIFACTS_ROOT}/{TASK_SLUG}/`（⚠️ 每个需求独立子目录，遵循 G3 产物隔离规则）
- **生命周期**：Main Agent Phase 0 初始化时创建，工作流结束后保留作审计

## JSON Schema (v1.3)

```json
{
  "version": "1.3",
  "task_id": "", "task_slug": "",
  "run_mode": "auto", "runtime_mode": "ide",
  "size_class": "medium", "current_stage": "TASK-02",
  "last_event": null, "next_target": null,
  "workspace_root": "", "artifacts_dir": "",
  "created_at": "", "updated_at": "",
  "project_config": {
    "name": "", "default_branch": "master",
    "coding_standards": ""
  },
  "stages": {
    "SOLO":        { "status": "pending", "executor": "solo-developer", ... },
    "TASK-02":     { "status": "pending", "executor": "architect", ... },
    "TASK-03":     { "status": "pending", "executor": "developer", ... },
    "CODE-REVIEW": { "status": "pending", "executor": "code-reviewer", ... },
    "TASK-04":     { "status": "pending", "executor": "test-engineer", ... },
    "TASK-05":     { "status": "pending", "executor": "knowledge-engineer", ... }
  },
  "decisions": [],
  "summary": { "status": "not_started", "report_path": null, "completed_at": null }
}
```

每个 stage 含：`status`、`executor`、`description`、`artifact_path`、`artifact_http`、`review_result`、`review_comment`、`retry_count`、`started_at`、`completed_at`、`self_check`（hard_gate/check_time/actor）、`api_docs_path`（可选，TASK-03/SOLO 阶段，API 文档路径）、`api_docs_generated`（可选，bool）。

> 注意：v1.3 移除了 TASK-01 阶段（需求分析已合并进 Main Agent Step 2.5）、新增 CODE-REVIEW 阶段。各阶段完成后直接流转下一阶段（不经 leader 产物审核）。medium/large 在 TASK-05 后由 leader 汇总，small 由 solo-developer 自行汇总。

## 阶段枚举

| 阶段 | 执行者 | 说明 |
|------|--------|------|
| PHASE-0 | Main Agent | 初始化 + 大小判定 |
| SOLO | solo-developer | 单 agent 全流程（仅 small） |
| TASK-02 | architect | 技术方案 + 执行计划 |
| TASK-03 | developer | 代码实现 |
| CODE-REVIEW | code-reviewer | 代码审查 |
| TASK-04 | test-engineer | 按项目现有体系补充并执行必要测试 |
| TASK-05 | knowledge-engineer | 知识沉淀 |

## 顶层字段

| 字段 | 说明 |
|------|------|
| `version` | Schema 版本 `"1.3"` |
| `task_id` | 需求标识符 |
| `task_slug` | `{标题}_{YYYYMMDD_HHMM}`，不可变 |
| `run_mode` | `"auto"` / `"manual"` |
| `runtime_mode` | `"ide"`（team-async 模式），不可变 |
| `size_class` | `"small"` / `"medium"` / `"large"`，Main Agent Phase 0 写入，不可变 |
| `current_stage` | 当前阶段 |
| `last_event` | 最近事件（如 `"TASK-02_completed"`） |
| `next_target` | 下一目标角色名 |
| `last_error` | 最近错误信息，打回时写入原因，通过时置 `null` |

## Status 流转

```
pending → in_progress → completed → passed / failed
pending → skipped（可选阶段）
failed → in_progress（重试）
```

## 各角色读写权限

> 原则：每个角色只写自己负责的字段，读取不限。

- **Main Agent**：初始化全部顶层 + Phase 0 字段 + `current_stage` + `updated_at`
- **Leader**：最终汇总 `decisions`（追加） + `summary`
- **各执行角色**：自己阶段的 `status` / `artifact_*` / `self_check` / `completed_at`
- **code-reviewer**：CODE-REVIEW 阶段的 `status` / `review_result` / `review_comment` / `artifact_path`
- **不可变字段**：`version` / `task_id` / `task_slug` / `runtime_mode` / `created_at`

## 操作规范

### 读取（每次 spawn 第一步）

`read_file` → 解析 JSON → 提取所需字段

### 启动写入（阶段开始时，启动协议完成后立即执行）

角色被唤醒、读取 JSON 完成启动协议后，**立即**写入以下字段：

1. `read_file` 重新读取（确保最新）
2. **获取当前实际时间**：`execute_command("date -u +%Y-%m-%dT%H:%M:%S%z")` → 赋值给 `now`
3. 更新字段：`stages[当前阶段].status = "in_progress"` + `stages[当前阶段].started_at = now` + `updated_at = now` + `current_stage = 当前阶段`
4. `write_to_file` 写回完整 JSON

> ⚠️ `started_at` 必须在阶段**开始**时写入，不得推迟到阶段结束。

### 完成写入（阶段结束时，任务最后一步）

1. `read_file` 重新读取（确保最新）
2. **获取当前实际时间**：`execute_command("date -u +%Y-%m-%dT%H:%M:%S%z")` → 赋值给 `now`
3. 内存更新自己的字段。⚠️ 所有时间戳字段（`updated_at` / `completed_at` / `summary.completed_at`）**必须使用步骤 2 获取的 `now` 值**，禁止复用之前读取到的时间值或缓存值
4. `write_to_file` 写回完整 JSON

### 写入内容参考

**执行角色完成时**：`status="completed"` + `artifact_path` + `completed_at=now` + `self_check` + `last_event="TASK-XX_completed"` + `next_target=<routing_table 目标>`

**code-reviewer 完成时**：`status="completed"` + `review_result="passed|failed"` + `review_comment` + `artifact_path` + `completed_at=now` + `last_event="CODE-REVIEW_passed|failed"`

**Leader 汇总时**：`summary.status="completed"` + `summary.report_path` + `summary.completed_at=now`

**任一角色判定自身失败时** ⭐：`status="failed"` + `stages[当前阶段].retry_count = (原值 or 0) + 1` + `last_error=<失败原因>` + `completed_at=now` + `last_event="TASK-XX_failed"`。`retry_count` 只能由失败角色自己在这一步递增，不得由 Main Agent 代写；Main Agent 在调度自检时只读取该值判断重试或暂停（见"调度自检"章节），不修改它。

## 初始化协议

Main Agent 从模板创建 → 填充 `task_id`/`task_slug`/`run_mode`/`runtime_mode`/`project_config` → Phase 0 3 维度打分写入 `size_class` → `current_stage="PHASE-0"` → `last_event="workflow_initialized"`

## 中断恢复

读 JSON → 按 `current_stage` + `status` 确定恢复点：
- `pending` → spawn 执行角色
- `in_progress` → 有产物则进下一阶段，无则重新执行
- `completed` → 按 routing_table 派发下一角色
- `failed` → retry_count<2 重试，否则暂停

## 错误处理

- 角色异常（未写 JSON）：retry_count<2 重试；≥2 暂停
- JSON 损坏：从 `.bak` 恢复或人工干预
- 写入冲突：串行推进，每次写前重读

## 禁止行为

- ❌ 写权限外字段 / 跳过读或写 JSON / 修改 decisions 历史 / 修改不可变字段 / 删除 JSON / size_class

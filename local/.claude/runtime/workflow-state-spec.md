# workflow-state.json 持久化状态规范

`workflow-state.json` 是角色间上下文传递、恢复和审计的唯一持久化通道。
所有阶段开始前必须读取本文档和对应状态文件。

## 文件定位

- 路径：`{ARTIFACTS_ROOT}/{TASK_SLUG}/workflow-state.json`
- `ARTIFACTS_ROOT`：`.claude/assets/devflow.defaults.yaml` 的 `artifacts.root_dir`
- `artifacts_dir`：`{ARTIFACTS_ROOT}/{TASK_SLUG}/`
- 生命周期：Phase 0 初始化时创建，工作流结束后保留作审计

## JSON Schema

```json
{
  "version": "1.3",
  "task_id": "",
  "task_slug": "",
  "run_mode": "auto",
  "runtime_mode": "ide",
  "size_class": "medium",
  "current_stage": "TASK-02",
  "last_event": null,
  "next_target": null,
  "workspace_root": "",
  "artifacts_dir": "",
  "created_at": "",
  "updated_at": "",
  "project_config": {
    "name": "",
    "default_branch": "master",
    "coding_standards": ""
  },
  "stages": {
    "SOLO": { "status": "pending", "executor": "solo-developer" },
    "TASK-01": { "status": "pending", "executor": "main" },
    "TASK-02": { "status": "pending", "executor": "architect" },
    "TASK-03": { "status": "pending", "executor": "developer" },
    "CODE-REVIEW": { "status": "pending", "executor": "code-reviewer" },
    "TASK-04": { "status": "pending", "executor": "test-engineer" },
    "TASK-05": { "status": "pending", "executor": "knowledge-engineer" }
  },
  "decisions": [],
  "summary": { "status": "not_started", "report_path": null, "completed_at": null }
}
```

每个 stage 可包含：

- `status`
- `executor`
- `description`
- `artifact_path`
- `artifact_http`
- `review_result`
- `review_comment`
- `retry_count`
- `started_at`
- `completed_at`
- `self_check`
- `api_docs_path`
- `api_docs_generated`

## 阶段枚举

| 阶段 | 执行者 | 说明 |
|------|--------|------|
| PHASE-0 | main | 初始化 + 大小判定 |
| SOLO | solo-developer | 单 agent 全流程 |
| TASK-01 | main | 需求分析 + superpowers:brainstorming 澄清 |
| TASK-02 | architect | 技术方案 + 执行计划 |
| TASK-03 | developer | 代码实现 |
| CODE-REVIEW | code-reviewer | 代码审查 |
| TASK-04 | test-engineer | 按项目现有体系补充并执行必要测试 |
| TASK-05 | knowledge-engineer | 知识沉淀 |

## 顶层字段

| 字段 | 说明 |
|------|------|
| `version` | Schema 版本 |
| `task_id` | 需求标识符 |
| `task_slug` | 不可变任务 slug |
| `run_mode` | `auto` 或 `manual` |
| `runtime_mode` | 兼容字段，Claude Code 中固定保留 |
| `size_class` | `small`、`medium`、`large` |
| `current_stage` | 当前阶段 |
| `last_event` | 最近事件 |
| `next_target` | 下一目标角色名 |
| `last_error` | 最近错误信息 |

## 状态流转

```text
pending -> in_progress -> completed -> passed / failed
pending -> skipped
failed -> in_progress
```

## 读写权限

- Main：初始化顶层字段、Phase 0 字段、TASK-01 字段、`current_stage`、`updated_at`
- Leader：最终汇总 `decisions` 和 `summary`
- 各执行角色：自己阶段的状态、产物、自检和完成时间
- code-reviewer：CODE-REVIEW 阶段的审查结果和审查意见
- 不可变字段：`version`、`task_id`、`task_slug`、`runtime_mode`、`created_at`

## 操作规范

### 读取

每次阶段开始前读取 `workflow-state.json`，解析 JSON，提取所需字段。

### 阶段开始写入

1. 重新读取最新 JSON。
2. 获取当前时间。
3. 更新当前阶段：
   - `status = "in_progress"`
   - `started_at = now`
   - `updated_at = now`
   - `current_stage = 当前阶段`
4. 写回完整 JSON。

### 阶段完成写入

1. 重新读取最新 JSON。
2. 获取当前时间。
3. 更新当前阶段状态、产物、自检、完成时间。
4. 写入 `current_stage = 当前阶段`。阶段完成后仍应保留为刚完成的阶段，
   由 `last_event` 和 `next_target` 表示下一步路由。
5. 写入 `last_event` 和 `next_target`。
6. 写回完整 JSON。

TASK-01 完成时固定写入：

- `stages["TASK-01"].status = "completed"`
- `stages["TASK-01"].artifact_path = "{artifacts_dir}/01-requirement/requirement-report.md"`
- `current_stage = "TASK-01"`
- `last_event = "TASK-01_completed"`
- `next_target = "architect"`

## 初始化协议

Main 从模板创建状态文件，填充 `task_id`、`task_slug`、`run_mode`、
`runtime_mode`、`project_config`，Phase 0 写入 `size_class`，
`current_stage = "PHASE-0"`，`last_event = "workflow_initialized"`。

## 中断恢复

读取 JSON 后按 `current_stage` 与阶段状态恢复：

- `pending`：从该阶段开始
- `in_progress`：有完整产物则进入下一阶段，否则重新执行该阶段
- `completed`：按 routing table 进入下一阶段
- `failed`：`retry_count < 2` 时重试，否则暂停并向用户报告

## 禁止行为

- 写权限外字段
- 跳过读写 JSON
- 修改 decisions 历史
- 修改不可变字段
- 删除 JSON
- 删除既有阶段产物

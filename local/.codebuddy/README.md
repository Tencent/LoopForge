# DevFlow — 多智能体开发工作流

## 启动命令

```
/start-devflow {需求描述}
```

中断后恢复：`/resume-devflow {TASK_SLUG}`

---

## 命令清单

### 一、工作流主控

| 命令 | 用途 |
|------|------|
| `/start-devflow {需求描述}` | 启动一次完整工作流（自动判定 small/medium/large） |
| `/resume-devflow {TASK_SLUG}` | 中断后从断点续跑，已有产物不重做 |
| `/status-devflow {TASK_SLUG\|TASK_ID}` | 查看当前进度（只读） |
| `/abort-devflow {TASK_SLUG\|TASK_ID}` | 优雅中止工作流，保留已有产物 |

### 二、单阶段命令（高级用法）

> 用于某阶段产物想重跑、或只想用某一项能力。前置条件：上游阶段产物已存在。

| 命令 | 对应阶段 | 执行角色 |
|------|---------|---------|
| `/analyze-requirement {需求描述}` | TASK-01 需求分析 | Main Agent（含 brainstorming） |
| `/design-solution [TASK_SLUG]` | TASK-02 技术方案 | architect |
| `/develop-code [TASK_SLUG]` | TASK-03 编码 | developer |
| `/review-code [TASK_SLUG]` | CODE-REVIEW 审查 | code-reviewer |
| `/run-tests [TASK_SLUG]` | TASK-04 测试 | test-engineer |
| `/distill-knowledge [TASK_SLUG]` | TASK-05 知识沉淀 | knowledge-engineer |

### 三、配套 Git 命令

| 命令 | 用途 |
|------|------|
| `/git-prepare {分支名} [基准分支]` | 拉最新代码并切到新分支（启动 devflow 前用） |
| `/git-commit ["提交信息"]` | 暂存 + Conventional Commits 提交 + push |

> ⚠️ 命令格式：斜杠命令与参数之间**必须有一个空格**，否则无法识别。

---

## 中大需求（修改 ≥ 3 个文件）→ 多 Agent 模式

**人工交互**：仅需求澄清阶段（brainstorming）需要与用户对话，后续全流程自动完成。

**流程**：需求澄清 → 技术方案 → 编码 → 代码审查 → 测试 → 知识沉淀 → 最终汇总

**产物路径**：`artifacts/{task_slug}/`

| 产物 | 路径 |
|------|------|
| 需求分析报告 | `01-requirement/requirement-report.md` |
| 技术方案 + 执行计划 | `02-design/tech-design.md` + `execution-plan.md` |
| 代码变更报告 + 审查报告 | `03-code/change-report.md` + `review-report.md` |
| 测试报告与新增用例 | `04-e2e/` |
| 知识沉淀 | `{artifacts_root}/knowledge/{task_slug}.md` |
| 最终汇总 | `workflow-summary.md` |

---

## 小需求（修改 ≤ 2 个文件）→ 单 Agent 模式

**人工交互**：无，全流程自动完成（需求理解 → 编码 → 自检 → 知识沉淀）。

**产物路径**：`artifacts/{task_slug}/`

| 产物 | 路径 |
|------|------|
| Solo 全流程产物 | `01-solo/` |
| 知识沉淀 | `{artifacts_root}/knowledge/{task_slug}.md` |
| 最终汇总 | `workflow-summary.md` |

---

## 注意事项

1. **命令格式**：`/start-devflow` 和需求描述之间**必须有一个空格**，否则命令无法识别。

2. **启动日志刷屏**：多 Agent 模式启动时，CodeBuddy 会打印大量初始化信息（spawn 角色、team 创建等），直接跳过拉到最下面即可，无需关注。

3. **代码调研较慢**：需求分析阶段会自主调研代码库，CodeBuddy IDE 默认内置了 Code Explorer Agent 来辅助代码探索，但该 Agent 调研速度较慢。如果感觉太慢，可手动关闭：IDE 右上角设置 → Agent → 用户 Agent，关闭 Code Explorer。

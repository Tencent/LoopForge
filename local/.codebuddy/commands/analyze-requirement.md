---
description: 单独执行需求分析（不启动完整 devflow 流程）
argument-hint: [需求标题] | 需求描述
allowed-tools: ["use_skill", "read_file", "write_to_file", "search_content", "list_dir", "execute_command", "ask_followup_question", "codebase_search"]
---

# /analyze-requirement

## 用法
```
/analyze-requirement {需求描述}
```

单独执行需求分析阶段（TASK-01），不启动完整 devflow 工作流。适用于：
- 只想做需求分析和澄清，暂不进入方案设计
- 预先生成 requirement-report.md 供后续使用

## Main Agent 动作

### 第一步：环境准备

1. **解析参数**：提取 title + requirement（必填）
2. **读取配置**：`assets/devflow.defaults.yaml`
3. **确认 workflow-state.json 存在**：
   - 已存在 → 读取 task_slug / artifacts_dir
   - 不存在 → 提示用户先执行 `/start-devflow` 初始化（或自动执行 Phase 0 初始化）

### 第二步：AI 自主需求分析

Main Agent 自主完成（不与用户交互）：
1. 阅读项目结构、相关模块代码、现有接口定义
2. 识别需求涉及的模块、服务、数据库表、上下游依赖
3. 梳理技术约束、兼容性要求、架构限制
4. 分析结果暂存上下文

### 第三步：需求澄清 — brainstorming

Main Agent 调用 `use_skill("brainstorming")`，注入 devflow 上下文覆盖：
- 产物输出到 `{artifacts_dir}/01-requirement/requirement-report.md`
- 跳过 writing-plans 调用
- 产物包含：AI 需求分析结果 + 澄清 Q&A + 收敛结论

### 第四步：保存产物

1. 保存 `requirement-report.md`（含 YAML front matter）
2. 更新 `workflow-state.json`：
   - `stages.TASK-01.status = "completed"`
   - `last_event = "TASK-01_completed"`
   - 追加 `decisions[]` 记录 `kind="requirement_analysis_completed"`

### 完成提示

输出：需求分析已完成，可执行 `/design-solution` 进入方案设计阶段。

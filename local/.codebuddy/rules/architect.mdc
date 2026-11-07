---
description: "架构师规则：方案先行、调研顺序、决策确认、方案边界。"
alwaysApply: false
enabled: true
---

# Architect 规则

## Rule 1：方案先行
需求报告（requirement-report.md）→ 架构/代码调研 → 技术方案 → 自动流转给 developer。方案粒度到文件级 + 函数级。

## Rule 2：调研顺序
1. 读取 `01-requirement/requirement-report.md`（含 AI 需求分析 + 澄清结论）→ 2. 检索知识库架构文档 → 3. 检索组件设计 → 4. 读代码。禁止不读报告就设计。

## Rule 3：方案边界
✅ 技术方案、架构评估、链路分析、变更点识别、execution-plan 编排
❌ 写代码、执行测试

## Rule 4：execution-plan 格式强制约束

`execution-plan.md` 必须严格按 `architect.md Step 5` 的 `parallel_tasks` YAML 格式产出。禁止串行步骤式 plan，禁止参考 `tech-design` skill 模板的排期章节。

## Rule 5：接口变更必须包含 API 文档任务

当技术方案涉及新增或修改 HTTP API 接口时，`execution-plan.md` 的 `parallel_tasks` 中**必须**包含一个 API 文档子任务（产物为 `03-code/api-docs.md`），要求：
- 列出所有新增/变更的接口（路由、方法、请求/响应格式、鉴权方式、错误码）
- 该任务可与编码实现并行或放在最后一个 code group 中
- `files_whitelist` 包含 `{artifacts_dir}/03-code/api-docs.md`
- acceptance 标准：所有新增/变更接口均有完整的请求参数、响应结构、错误码说明

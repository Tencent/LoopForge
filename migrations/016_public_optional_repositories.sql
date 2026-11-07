-- Explicit operator upgrade for public SOP instructions.
-- Each statement changes only an unchanged original field; operator edits are preserved.
SET NAMES utf8mb4;
START TRANSACTION;
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[0].taskPrompt', CAST('{"role": "需求定型", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-backend-delivery' AND JSON_EXTRACT(payload, '$.Agents[0].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[0].id')) = 'generic-backend-delivery-clarify';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[1].taskPrompt', CAST('{"role": "方案设计", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-backend-delivery' AND JSON_EXTRACT(payload, '$.Agents[1].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[1].id')) = 'generic-backend-delivery-design';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[2].taskPrompt', CAST('{"role": "开发实现", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-backend-delivery' AND JSON_EXTRACT(payload, '$.Agents[2].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[2].id')) = 'generic-backend-delivery-implement';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[3].taskPrompt', CAST('{"role": "独立评审", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-backend-delivery' AND JSON_EXTRACT(payload, '$.Agents[3].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[3].id')) = 'generic-backend-delivery-review';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[4].taskPrompt', CAST('{"role": "测试验证", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-backend-delivery' AND JSON_EXTRACT(payload, '$.Agents[4].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].id')) = 'generic-backend-delivery-test';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[5].taskPrompt', CAST('{"role": "交付汇总", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-backend-delivery' AND JSON_EXTRACT(payload, '$.Agents[5].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[5].id')) = 'generic-backend-delivery-deliver';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[1].instructions', '工作领域：后端服务、数据与 API。阅读已确认需求与可用代码，写出最小实现方案、接口影响、风险与验证计划到 design.md。未绑定仓库的新项目可在任务工作区设计和实现；修改已有系统或修复缺陷时必须取得相关源码，缺少时明确阻塞并请求补充。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-backend-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[1].instructions')) = '工作领域：后端服务、数据与 API。阅读已确认需求及现有代码，写出最小实现方案、接口影响、风险与验证计划到 design.md。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[5].instructions', '工作领域：后端服务、数据与 API。核对各阶段实际产物与验证证据，将完成内容、真实测试结果、已知限制及交付物清单写到 delivery.md。未绑定仓库时交付任务工作区中的源码和报告，无需 Git Token；仅在用户明确要求且已配置目标 GitHub 仓库及凭据时创建 PR，不得自行合并。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-backend-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[5].instructions')) = '工作领域：后端服务、数据与 API。核对各阶段证据与代码 diff，将实际完成内容、测试结果、已知限制及交付物清单写到 delivery.md。通过已配置的 GitHub MCP 创建 PR（仅当任务明确要求）；不得自行合并 PR。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Rules[0].content', '先阅读仓库约束并明确范围；只修改任务所需文件。不得泄露凭据，不得伪造测试或交付证据。使用项目现有构建与测试命令，报告真实结果与限制。发布 PR 前检查 diff；未经任务授权不得合并主分支。仓库绑定是可选的：未绑定时在任务工作区创建新项目并交付源码、报告；已有系统的修改或缺陷修复必须取得相关代码，缺少时明确阻塞并请求补充。没有目标仓库和凭据时不执行推送或创建 PR。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-backend-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Rules[0].content')) = '先阅读仓库约束并明确范围；只修改任务所需文件。不得泄露凭据，不得伪造测试或交付证据。使用项目现有构建与测试命令，报告真实结果与限制。发布 PR 前检查 diff；未经任务授权不得合并主分支。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[0].taskPrompt', CAST('{"role": "需求定型", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-frontend-delivery' AND JSON_EXTRACT(payload, '$.Agents[0].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[0].id')) = 'generic-frontend-delivery-clarify';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[1].taskPrompt', CAST('{"role": "方案设计", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-frontend-delivery' AND JSON_EXTRACT(payload, '$.Agents[1].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[1].id')) = 'generic-frontend-delivery-design';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[2].taskPrompt', CAST('{"role": "开发实现", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-frontend-delivery' AND JSON_EXTRACT(payload, '$.Agents[2].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[2].id')) = 'generic-frontend-delivery-implement';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[3].taskPrompt', CAST('{"role": "独立评审", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-frontend-delivery' AND JSON_EXTRACT(payload, '$.Agents[3].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[3].id')) = 'generic-frontend-delivery-review';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[4].taskPrompt', CAST('{"role": "测试验证", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-frontend-delivery' AND JSON_EXTRACT(payload, '$.Agents[4].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].id')) = 'generic-frontend-delivery-test';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[5].taskPrompt', CAST('{"role": "交付汇总", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-frontend-delivery' AND JSON_EXTRACT(payload, '$.Agents[5].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[5].id')) = 'generic-frontend-delivery-deliver';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[1].instructions', '工作领域：前端页面、交互、可访问性与响应式。阅读已确认需求与可用代码，写出最小实现方案、接口影响、风险与验证计划到 design.md。未绑定仓库的新项目可在任务工作区设计和实现；修改已有系统或修复缺陷时必须取得相关源码，缺少时明确阻塞并请求补充。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-frontend-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[1].instructions')) = '工作领域：前端页面、交互、可访问性与响应式。阅读已确认需求及现有代码，写出最小实现方案、接口影响、风险与验证计划到 design.md。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[5].instructions', '工作领域：前端页面、交互、可访问性与响应式。核对各阶段实际产物与验证证据，将完成内容、真实测试结果、已知限制及交付物清单写到 delivery.md。未绑定仓库时交付任务工作区中的源码和报告，无需 Git Token；仅在用户明确要求且已配置目标 GitHub 仓库及凭据时创建 PR，不得自行合并。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-frontend-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[5].instructions')) = '工作领域：前端页面、交互、可访问性与响应式。核对各阶段证据与代码 diff，将实际完成内容、测试结果、已知限制及交付物清单写到 delivery.md。通过已配置的 GitHub MCP 创建 PR（仅当任务明确要求）；不得自行合并 PR。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Rules[0].content', '先阅读仓库约束并明确范围；只修改任务所需文件。不得泄露凭据，不得伪造测试或交付证据。使用项目现有构建与测试命令，报告真实结果与限制。发布 PR 前检查 diff；未经任务授权不得合并主分支。仓库绑定是可选的：未绑定时在任务工作区创建新项目并交付源码、报告；已有系统的修改或缺陷修复必须取得相关代码，缺少时明确阻塞并请求补充。没有目标仓库和凭据时不执行推送或创建 PR。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-frontend-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Rules[0].content')) = '先阅读仓库约束并明确范围；只修改任务所需文件。不得泄露凭据，不得伪造测试或交付证据。使用项目现有构建与测试命令，报告真实结果与限制。发布 PR 前检查 diff；未经任务授权不得合并主分支。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[0].taskPrompt', CAST('{"role": "需求定型", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'issue-fix-production' AND JSON_EXTRACT(payload, '$.Agents[0].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[0].id')) = 'issue-fix-production-clarify';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[1].taskPrompt', CAST('{"role": "方案设计", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'issue-fix-production' AND JSON_EXTRACT(payload, '$.Agents[1].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[1].id')) = 'issue-fix-production-design';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[2].taskPrompt', CAST('{"role": "开发实现", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'issue-fix-production' AND JSON_EXTRACT(payload, '$.Agents[2].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[2].id')) = 'issue-fix-production-implement';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[3].taskPrompt', CAST('{"role": "独立评审", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'issue-fix-production' AND JSON_EXTRACT(payload, '$.Agents[3].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[3].id')) = 'issue-fix-production-review';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[4].taskPrompt', CAST('{"role": "测试验证", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'issue-fix-production' AND JSON_EXTRACT(payload, '$.Agents[4].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].id')) = 'issue-fix-production-test';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[5].taskPrompt', CAST('{"role": "交付汇总", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'issue-fix-production' AND JSON_EXTRACT(payload, '$.Agents[5].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[5].id')) = 'issue-fix-production-deliver';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[0].instructions', '工作领域：缺陷根因、复现与回归。确认目标、范围、用户、边界与可测试验收标准，输出 requirement.md。不要开始编码。可以不绑定仓库，但必须先取得用户提供的待修代码和复现材料；缺少时明确列出阻塞项并请求补充，不得编造复现或声称已修复。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'issue-fix-production' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[0].instructions')) = '工作领域：缺陷根因、复现与回归。确认目标、范围、用户、边界与可测试验收标准，输出 requirement.md。不要开始编码。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[1].instructions', '工作领域：缺陷根因、复现与回归。阅读已确认需求与可用代码，写出最小实现方案、接口影响、风险与验证计划到 design.md。未绑定仓库的新项目可在任务工作区设计和实现；修改已有系统或修复缺陷时必须取得相关源码，缺少时明确阻塞并请求补充。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'issue-fix-production' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[1].instructions')) = '工作领域：缺陷根因、复现与回归。阅读已确认需求及现有代码，写出最小实现方案、接口影响、风险与验证计划到 design.md。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[5].instructions', '工作领域：缺陷根因、复现与回归。核对各阶段实际产物与验证证据，将完成内容、真实测试结果、已知限制及交付物清单写到 delivery.md。未绑定仓库时交付任务工作区中的源码和报告，无需 Git Token；仅在用户明确要求且已配置目标 GitHub 仓库及凭据时创建 PR，不得自行合并。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'issue-fix-production' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[5].instructions')) = '工作领域：缺陷根因、复现与回归。核对各阶段证据与代码 diff，将实际完成内容、测试结果、已知限制及交付物清单写到 delivery.md。通过已配置的 GitHub MCP 创建 PR（仅当任务明确要求）；不得自行合并 PR。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Rules[0].content', '先阅读仓库约束并明确范围；只修改任务所需文件。不得泄露凭据，不得伪造测试或交付证据。使用项目现有构建与测试命令，报告真实结果与限制。发布 PR 前检查 diff；未经任务授权不得合并主分支。仓库绑定是可选的：未绑定时在任务工作区创建新项目并交付源码、报告；已有系统的修改或缺陷修复必须取得相关代码，缺少时明确阻塞并请求补充。没有目标仓库和凭据时不执行推送或创建 PR。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'issue-fix-production' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Rules[0].content')) = '先阅读仓库约束并明确范围；只修改任务所需文件。不得泄露凭据，不得伪造测试或交付证据。使用项目现有构建与测试命令，报告真实结果与限制。发布 PR 前检查 diff；未经任务授权不得合并主分支。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[0].taskPrompt', CAST('{"role": "需求定型", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_EXTRACT(payload, '$.Agents[0].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[0].id')) = 'generic-product-delivery-clarify';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[1].taskPrompt', CAST('{"role": "方案设计", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_EXTRACT(payload, '$.Agents[1].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[1].id')) = 'generic-product-delivery-design';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[2].taskPrompt', CAST('{"role": "独立评审", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_EXTRACT(payload, '$.Agents[2].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[2].id')) = 'generic-product-delivery-review';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[3].taskPrompt', CAST('{"role": "交付汇总", "objective": "完成本节点约定的工作并交付产物。", "inputs": ["任务需求：{{input}}", "仓库上下文：{{repository}}"]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_EXTRACT(payload, '$.Agents[3].taskPrompt') IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[3].id')) = 'generic-product-delivery-deliver';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Workflow.description', '完成需求分析、产品方案文档与独立验收；无需代码仓库或 Git Token。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Workflow.description')) = '完成需求分析、方案文档与独立验收。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[0].instructions', '工作领域：产品方案、用户场景与验收规范。确认目标、范围、用户场景、业务边界与可检查的验收标准，输出 requirement.md。信息不足时明确列出假设和待确认项。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[0].instructions')) = '工作领域：产品方案、用户场景与验收规范。确认目标、范围、用户、边界与可测试验收标准，输出 requirement.md。不要开始编码。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[1].instructions', '工作领域：产品方案、用户场景与验收规范。阅读 requirement.md，梳理用户旅程、功能范围、交互流程、业务规则、风险和验收标准，将产品方案写到 design.md。只交付文档，无需代码仓库，不执行编码或 Git 操作。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[1].instructions')) = '工作领域：产品方案、用户场景与验收规范。阅读已确认需求及现有代码，写出最小实现方案、接口影响、风险与验证计划到 design.md。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[2].instructions', '工作领域：产品方案、用户场景与验收规范。依据 requirement.md 独立审查 design.md 的完整性、一致性、边界场景与验收标准。将具体问题、文档位置、影响及修改建议写到 review.md。发现阻断问题必须明确写出 REJECT；不得伪造验证结论。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[2].instructions')) = '工作领域：产品方案、用户场景与验收规范。独立审查改动及测试证据；按严重程度列出有位置与影响的缺陷及修复建议到 review.md。发现阻断问题必须明确写出 REJECT。不得伪造通过结论。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[3].instructions', '工作领域：产品方案、用户场景与验收规范。在人工验收后核对 requirement.md、design.md 和 review.md，将实际完成内容、验收结论、待确认问题、已知限制及交付物清单写到 delivery.md。只交付工作区文档，无需 Git Token，不创建代码提交或 PR。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[3].instructions')) = '工作领域：产品方案、用户场景与验收规范。核对各阶段证据与代码 diff，将实际完成内容、测试结果、已知限制及交付物清单写到 delivery.md。通过已配置的 GitHub MCP 创建 PR（仅当任务明确要求）；不得自行合并 PR。';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Rules[0].id', 'public-product-documents'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Rules[0].id')) = 'public-engineering';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Rules[0].name', '产品文档与验收约束'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Rules[0].name')) = '工程与验证约束';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[0].rules[0]', 'public-product-documents'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[0].rules[0]')) = 'public-engineering';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[1].rules[0]', 'public-product-documents'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[1].rules[0]')) = 'public-engineering';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[2].rules[0]', 'public-product-documents'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[2].rules[0]')) = 'public-engineering';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[3].rules[0]', 'public-product-documents'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[3].rules[0]')) = 'public-engineering';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Rules[0].content', '根据用户输入和工作区文档完成产品方案；无需代码仓库、Git Token 或代码构建。明确假设与待确认项，不得伪造事实、验证或验收结果，不得泄露凭据。文档写入任务工作区并按节点约定归档；发现阻断问题明确说明并返工。'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-product-delivery' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Rules[0].content')) = '先阅读仓库约束并明确范围；只修改任务所需文件。不得泄露凭据，不得伪造测试或交付证据。使用项目现有构建与测试命令，报告真实结果与限制。发布 PR 前检查 diff；未经任务授权不得合并主分支。';
COMMIT;

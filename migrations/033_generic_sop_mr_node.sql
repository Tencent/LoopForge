-- Idempotent MySQL migration: add a 「发起 MR」 node to the built-in code
-- delivery SOPs (backend, frontend, defect fix). The product SOP delivers
-- documents only and is left alone.
--
-- Before this, the deliver node opened a PR only when the user explicitly asked
-- for one, so a succeeded run could leave every source change uncommitted in a
-- workspace that is reclaimed right after success. The new node sits between
-- 人工验收 and 交付汇总, commits and pushes the work branch, opens the MR/PR and
-- records it in mr-body.md. Its command gate fails while a repository with
-- commits beyond the run's target branch has uncommitted changes, sits off the
-- run's work branch, or has not pushed HEAD to that work branch on origin. A
-- second gate (mode mr) has the engine look the MR up on GitHub or Gongfeng:
-- every pushed work branch with new commits needs a non-closed MR into the
-- run's target branch, and a host that cannot be queried goes to a person.
--
-- New installs get these templates from the seed; existing installs get them
-- from this script, which scripts/install-schema-extensions.sh replays on
-- install and on ./start.sh pull, so ./start.sh pull upgrades the graph
-- without a manual step. Only templates whose node graph and deliver position
-- are still the shipped original are rewritten; the deliver prompt is replaced
-- only when unedited. Project SOPs already adopted from these templates are
-- copies and are not touched. After it, a run of these SOPs needs a Git
-- credential with push access when a repository is bound: without one it stops
-- at 发起 MR for a person instead of succeeding.
SET NAMES utf8mb4;
-- The three original graphs differ only in the template prefix of their agent IDs.
SET @lf_033_nodes = '[
  {"id": "clarify", "name": "需求定型", "type": "agent", "agentId": "__template__-clarify", "maxAttempts": 2, "position": {"x": 100, "y": 150}, "artifactOutputs": [{"name": "clarify", "path": "requirement.md", "mediaType": "text/markdown", "required": true}]},
  {"id": "design", "name": "方案设计", "type": "agent", "agentId": "__template__-design", "maxAttempts": 2, "position": {"x": 345, "y": 150}, "artifactOutputs": [{"name": "design", "path": "design.md", "mediaType": "text/markdown", "required": true}]},
  {"id": "implement", "name": "开发实现", "type": "agent", "agentId": "__template__-implement", "maxAttempts": 2, "position": {"x": 590, "y": 150}, "artifactOutputs": [{"name": "implement", "path": "implementation.md", "mediaType": "text/markdown", "required": true}]},
  {"id": "review", "name": "独立评审", "type": "agent", "agentId": "__template__-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]},
  {"id": "test", "name": "测试验证", "type": "agent", "agentId": "__template__-test", "maxAttempts": 2, "position": {"x": 1080, "y": 150}, "artifactOutputs": [{"name": "test", "path": "test-report.md", "mediaType": "text/markdown", "required": true}]},
  {"id": "accept", "name": "人工验收", "type": "approval", "maxAttempts": 1, "rejectTo": "implement", "position": {"x": 1325, "y": 150}},
  {"id": "deliver", "name": "交付汇总", "type": "agent", "agentId": "__template__-deliver", "maxAttempts": 2, "position": {"x": 1570, "y": 150}, "artifactOutputs": [{"name": "deliver", "path": "delivery.md", "mediaType": "text/markdown", "required": true}]}
]';
SET @lf_033_edges = '[
  {"id": "e-0", "source": "clarify", "target": "design", "when": "success"},
  {"id": "e-1", "source": "design", "target": "implement", "when": "success"},
  {"id": "e-2", "source": "implement", "target": "review", "when": "success"},
  {"id": "e-3", "source": "review", "target": "test", "when": "success"},
  {"id": "e-4", "source": "test", "target": "accept", "when": "success"},
  {"id": "e-5", "source": "accept", "target": "deliver", "when": "success"}
]';
START TRANSACTION;
UPDATE generic_templates
SET payload = JSON_ARRAY_APPEND(
    JSON_SET(payload, '$.Workflow.edges[5].target', 'mr', '$.Workflow.nodes[6].position.x', 1815),
    '$.Workflow.nodes', CAST('{"id": "mr", "name": "发起 MR", "type": "agent", "agentId": "generic-backend-delivery-mr", "maxAttempts": 2, "position": {"x": 1570, "y": 150}, "artifactOutputs": [{"name": "mr-body", "path": "mr-body.md", "mediaType": "text/markdown", "required": true}]}' AS JSON),
    '$.Workflow.edges', CAST('{"id": "e-6", "source": "mr", "target": "deliver", "when": "success"}' AS JSON),
    '$.Agents', CAST('{"version": 1, "updatedAt": "2026-11-16T00:00:00Z", "id": "generic-backend-delivery-mr", "name": "发起 MR", "executorId": "codebuddy", "rules": ["public-engineering"], "skills": [], "mcpServers": [], "prompt": "工作领域：后端服务、数据与 API。人工验收已通过，本节点把本轮代码改动提交到工作分支、推送并发起 MR/PR，不再修改业务逻辑。\\n\\n1. 未绑定代码仓库时不推送、不创建 MR/PR：在 mr-body.md 写明「未绑定代码仓库，源码保留在任务工作区」并列出交付文件，然后提交 pass。\\n2. 已绑定仓库时逐个处理每个仓库检出目录，本轮没有改动的仓库跳过，不创建空 MR/PR：确认当前在本轮工作分支上，不得在主分支提交，也不得切换或新建分支；检查 git status 与 diff，只提交本任务相关的源码改动，不提交 .agentflow、依赖目录、构建产物和 SOP 阶段文档；用概括本次改动的提交信息 commit，再 git push -u origin 当前工作分支。平台已注入 Git 凭据，不得打印、回显或写入任何令牌。\\n3. 以仓库上下文中的目标分支为目标创建 MR/PR：GitHub 仓库优先用已配置的 GitHub MCP，没有时用 curl 调 GitHub API，令牌取 $AGENTFLOW_GIT_TOKEN；其它托管平台用已配置的对应 MCP。本轮工作分支已有指向同一目标分支的 MR/PR 时更新它，不重复创建。\\n4. 标题概括本次改动；描述写需求摘要、改动要点、测试结果（依据 test-report.md）和已知限制。不得合并 MR/PR，不得推送主分支或强推。\\n5. 把每个仓库的 MR/PR 链接、源分支、目标分支和提交 SHA 写到 mr-body.md。\\n6. 推送或创建 MR/PR 失败（缺少凭据、无权限、网络错误等）时不得提交 pass：在 mr-body.md 记录失败原因，提交 human，并在 actionRequired 写明需要人工处理什么，例如配置 Git Token 后重试，或到托管平台手动创建 MR。\\n\\nMR 约束：{{existingMR}}\\n\\n完成本节点约定的工作并交付产物。\\n\\n### 输入\\n\\n- 任务需求：{{input}}\\n- 仓库上下文：{{repository}}", "loop": {"enabled": true, "maxIterations": 2, "exitCondition": "all_gates_pass", "onExhausted": "human"}, "verification": [{"id": "mr-pushed", "name": "改动已提交并推送", "mode": "command", "stage": "after_execution", "commands": ["fail=0\\ntab=\\"$(printf ''\\\\t'')\\"\\nwhile IFS=\\"$tab\\" read -r d work base; do\\n  [ -n \\"$d\\" ] || continue\\n  dirty=\\"$(git -C \\"$d\\" status --porcelain --untracked-files=all -- . \\":(exclude).agentflow\\" \\":(exclude,glob)**/node_modules/**\\")\\" || { printf \\"%s 不是可用的 Git 检出目录\\\\n\\" \\"$d\\"; fail=1; continue; }\\n  if [ -n \\"$dirty\\" ]; then printf \\"%s 有未提交的改动：\\\\n%s\\\\n\\" \\"$d\\" \\"$dirty\\"; fail=1; continue; fi\\n  refs=HEAD\\n  if git -C \\"$d\\" rev-parse -q --verify \\"refs/heads/$work\\" >/dev/null; then refs=\\"HEAD refs/heads/$work\\"; fi\\n  ahead=\\"$(git -C \\"$d\\" rev-list --count $refs --not \\"refs/remotes/origin/$base\\")\\" || { printf \\"%s 找不到目标分支 origin/%s\\\\n\\" \\"$d\\" \\"$base\\"; fail=1; continue; }\\n  [ \\"$ahead\\" != 0 ] || continue\\n  branch=\\"$(git -C \\"$d\\" branch --show-current)\\"\\n  if [ \\"$branch\\" != \\"$work\\" ]; then printf \\"%s 当前在 %s，不在本轮工作分支 %s\\\\n\\" \\"$d\\" \\"${branch:-分离的 HEAD}\\" \\"$work\\"; fail=1; continue; fi\\n  head=\\"$(git -C \\"$d\\" rev-parse HEAD)\\"\\n  pushed=\\"$(git -C \\"$d\\" ls-remote origin \\"refs/heads/$work\\")\\" || { printf \\"%s 无法读取远端 origin\\\\n\\" \\"$d\\"; fail=1; continue; }\\n  if [ \\"${pushed%%\\"$tab\\"*}\\" != \\"$head\\" ]; then printf \\"%s 的工作分支 %s 在 origin 上不是 %s，本轮提交还没推送\\\\n\\" \\"$d\\" \\"$work\\" \\"$head\\"; fail=1; fi\\ndone <<EOF\\n$AGENTFLOW_WORK_BRANCHES\\nEOF\\nexit \\"$fail\\""], "required": true, "onFailure": "loop", "executionScope": "run"}, {"id": "mr-opened", "name": "MR 已创建", "mode": "mr", "stage": "after_execution", "required": true, "onFailure": "loop"}]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-backend-delivery'
  AND JSON_EXTRACT(payload, '$.Workflow.nodes') = CAST(REPLACE(@lf_033_nodes, '__template__', id) AS JSON)
  AND JSON_EXTRACT(payload, '$.Workflow.edges') = CAST(@lf_033_edges AS JSON)
  AND JSON_SEARCH(payload, 'one', 'generic-backend-delivery-mr', NULL, '$.Agents[*].id') IS NULL;
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[5].prompt', '工作领域：后端服务、数据与 API。核对各阶段实际产物与验证证据，将完成内容、真实测试结果、已知限制及交付物清单写到 delivery.md。未绑定仓库时交付任务工作区中的源码和报告，无需 Git Token。代码已由「发起 MR」节点提交、推送并创建 MR/PR：按 mr-body.md 把链接、分支和提交 SHA 写进交付物清单；本节点不再修改代码、推送或创建 PR，不得合并。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-backend-delivery'
  AND ROW_COUNT() = 1
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[5].id')) = 'generic-backend-delivery-deliver'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[5].prompt')) = '工作领域：后端服务、数据与 API。核对各阶段实际产物与验证证据，将完成内容、真实测试结果、已知限制及交付物清单写到 delivery.md。未绑定仓库时交付任务工作区中的源码和报告，无需 Git Token；仅在用户明确要求且已配置目标 GitHub 仓库及凭据时创建 PR，不得自行合并。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}';
UPDATE generic_templates
SET payload = JSON_ARRAY_APPEND(
    JSON_SET(payload, '$.Workflow.edges[5].target', 'mr', '$.Workflow.nodes[6].position.x', 1815),
    '$.Workflow.nodes', CAST('{"id": "mr", "name": "发起 MR", "type": "agent", "agentId": "generic-frontend-delivery-mr", "maxAttempts": 2, "position": {"x": 1570, "y": 150}, "artifactOutputs": [{"name": "mr-body", "path": "mr-body.md", "mediaType": "text/markdown", "required": true}]}' AS JSON),
    '$.Workflow.edges', CAST('{"id": "e-6", "source": "mr", "target": "deliver", "when": "success"}' AS JSON),
    '$.Agents', CAST('{"version": 1, "updatedAt": "2026-11-16T00:00:00Z", "id": "generic-frontend-delivery-mr", "name": "发起 MR", "executorId": "codebuddy", "rules": ["public-engineering"], "skills": [], "mcpServers": [], "prompt": "工作领域：前端页面、交互、可访问性与响应式。人工验收已通过，本节点把本轮代码改动提交到工作分支、推送并发起 MR/PR，不再修改业务逻辑。\\n\\n1. 未绑定代码仓库时不推送、不创建 MR/PR：在 mr-body.md 写明「未绑定代码仓库，源码保留在任务工作区」并列出交付文件，然后提交 pass。\\n2. 已绑定仓库时逐个处理每个仓库检出目录，本轮没有改动的仓库跳过，不创建空 MR/PR：确认当前在本轮工作分支上，不得在主分支提交，也不得切换或新建分支；检查 git status 与 diff，只提交本任务相关的源码改动，不提交 .agentflow、依赖目录、构建产物和 SOP 阶段文档；用概括本次改动的提交信息 commit，再 git push -u origin 当前工作分支。平台已注入 Git 凭据，不得打印、回显或写入任何令牌。\\n3. 以仓库上下文中的目标分支为目标创建 MR/PR：GitHub 仓库优先用已配置的 GitHub MCP，没有时用 curl 调 GitHub API，令牌取 $AGENTFLOW_GIT_TOKEN；其它托管平台用已配置的对应 MCP。本轮工作分支已有指向同一目标分支的 MR/PR 时更新它，不重复创建。\\n4. 标题概括本次改动；描述写需求摘要、改动要点、测试结果（依据 test-report.md）和已知限制。不得合并 MR/PR，不得推送主分支或强推。\\n5. 把每个仓库的 MR/PR 链接、源分支、目标分支和提交 SHA 写到 mr-body.md。\\n6. 推送或创建 MR/PR 失败（缺少凭据、无权限、网络错误等）时不得提交 pass：在 mr-body.md 记录失败原因，提交 human，并在 actionRequired 写明需要人工处理什么，例如配置 Git Token 后重试，或到托管平台手动创建 MR。\\n\\nMR 约束：{{existingMR}}\\n\\n完成本节点约定的工作并交付产物。\\n\\n### 输入\\n\\n- 任务需求：{{input}}\\n- 仓库上下文：{{repository}}", "loop": {"enabled": true, "maxIterations": 2, "exitCondition": "all_gates_pass", "onExhausted": "human"}, "verification": [{"id": "mr-pushed", "name": "改动已提交并推送", "mode": "command", "stage": "after_execution", "commands": ["fail=0\\ntab=\\"$(printf ''\\\\t'')\\"\\nwhile IFS=\\"$tab\\" read -r d work base; do\\n  [ -n \\"$d\\" ] || continue\\n  dirty=\\"$(git -C \\"$d\\" status --porcelain --untracked-files=all -- . \\":(exclude).agentflow\\" \\":(exclude,glob)**/node_modules/**\\")\\" || { printf \\"%s 不是可用的 Git 检出目录\\\\n\\" \\"$d\\"; fail=1; continue; }\\n  if [ -n \\"$dirty\\" ]; then printf \\"%s 有未提交的改动：\\\\n%s\\\\n\\" \\"$d\\" \\"$dirty\\"; fail=1; continue; fi\\n  refs=HEAD\\n  if git -C \\"$d\\" rev-parse -q --verify \\"refs/heads/$work\\" >/dev/null; then refs=\\"HEAD refs/heads/$work\\"; fi\\n  ahead=\\"$(git -C \\"$d\\" rev-list --count $refs --not \\"refs/remotes/origin/$base\\")\\" || { printf \\"%s 找不到目标分支 origin/%s\\\\n\\" \\"$d\\" \\"$base\\"; fail=1; continue; }\\n  [ \\"$ahead\\" != 0 ] || continue\\n  branch=\\"$(git -C \\"$d\\" branch --show-current)\\"\\n  if [ \\"$branch\\" != \\"$work\\" ]; then printf \\"%s 当前在 %s，不在本轮工作分支 %s\\\\n\\" \\"$d\\" \\"${branch:-分离的 HEAD}\\" \\"$work\\"; fail=1; continue; fi\\n  head=\\"$(git -C \\"$d\\" rev-parse HEAD)\\"\\n  pushed=\\"$(git -C \\"$d\\" ls-remote origin \\"refs/heads/$work\\")\\" || { printf \\"%s 无法读取远端 origin\\\\n\\" \\"$d\\"; fail=1; continue; }\\n  if [ \\"${pushed%%\\"$tab\\"*}\\" != \\"$head\\" ]; then printf \\"%s 的工作分支 %s 在 origin 上不是 %s，本轮提交还没推送\\\\n\\" \\"$d\\" \\"$work\\" \\"$head\\"; fail=1; fi\\ndone <<EOF\\n$AGENTFLOW_WORK_BRANCHES\\nEOF\\nexit \\"$fail\\""], "required": true, "onFailure": "loop", "executionScope": "run"}, {"id": "mr-opened", "name": "MR 已创建", "mode": "mr", "stage": "after_execution", "required": true, "onFailure": "loop"}]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-frontend-delivery'
  AND JSON_EXTRACT(payload, '$.Workflow.nodes') = CAST(REPLACE(@lf_033_nodes, '__template__', id) AS JSON)
  AND JSON_EXTRACT(payload, '$.Workflow.edges') = CAST(@lf_033_edges AS JSON)
  AND JSON_SEARCH(payload, 'one', 'generic-frontend-delivery-mr', NULL, '$.Agents[*].id') IS NULL;
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[5].prompt', '工作领域：前端页面、交互、可访问性与响应式。核对各阶段实际产物与验证证据，将完成内容、真实测试结果、已知限制及交付物清单写到 delivery.md。未绑定仓库时交付任务工作区中的源码和报告，无需 Git Token。代码已由「发起 MR」节点提交、推送并创建 MR/PR：按 mr-body.md 把链接、分支和提交 SHA 写进交付物清单；本节点不再修改代码、推送或创建 PR，不得合并。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-frontend-delivery'
  AND ROW_COUNT() = 1
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[5].id')) = 'generic-frontend-delivery-deliver'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[5].prompt')) = '工作领域：前端页面、交互、可访问性与响应式。核对各阶段实际产物与验证证据，将完成内容、真实测试结果、已知限制及交付物清单写到 delivery.md。未绑定仓库时交付任务工作区中的源码和报告，无需 Git Token；仅在用户明确要求且已配置目标 GitHub 仓库及凭据时创建 PR，不得自行合并。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}';
UPDATE generic_templates
SET payload = JSON_ARRAY_APPEND(
    JSON_SET(payload, '$.Workflow.edges[5].target', 'mr', '$.Workflow.nodes[6].position.x', 1815),
    '$.Workflow.nodes', CAST('{"id": "mr", "name": "发起 MR", "type": "agent", "agentId": "issue-fix-production-mr", "maxAttempts": 2, "position": {"x": 1570, "y": 150}, "artifactOutputs": [{"name": "mr-body", "path": "mr-body.md", "mediaType": "text/markdown", "required": true}]}' AS JSON),
    '$.Workflow.edges', CAST('{"id": "e-6", "source": "mr", "target": "deliver", "when": "success"}' AS JSON),
    '$.Agents', CAST('{"version": 1, "updatedAt": "2026-11-16T00:00:00Z", "id": "issue-fix-production-mr", "name": "发起 MR", "executorId": "codebuddy", "rules": ["public-engineering"], "skills": [], "mcpServers": [], "prompt": "工作领域：缺陷根因、复现与回归。人工验收已通过，本节点把本轮代码改动提交到工作分支、推送并发起 MR/PR，不再修改业务逻辑。\\n\\n1. 未绑定代码仓库时不推送、不创建 MR/PR：在 mr-body.md 写明「未绑定代码仓库，源码保留在任务工作区」并列出交付文件，然后提交 pass。\\n2. 已绑定仓库时逐个处理每个仓库检出目录，本轮没有改动的仓库跳过，不创建空 MR/PR：确认当前在本轮工作分支上，不得在主分支提交，也不得切换或新建分支；检查 git status 与 diff，只提交本任务相关的源码改动，不提交 .agentflow、依赖目录、构建产物和 SOP 阶段文档；用概括本次改动的提交信息 commit，再 git push -u origin 当前工作分支。平台已注入 Git 凭据，不得打印、回显或写入任何令牌。\\n3. 以仓库上下文中的目标分支为目标创建 MR/PR：GitHub 仓库优先用已配置的 GitHub MCP，没有时用 curl 调 GitHub API，令牌取 $AGENTFLOW_GIT_TOKEN；其它托管平台用已配置的对应 MCP。本轮工作分支已有指向同一目标分支的 MR/PR 时更新它，不重复创建。\\n4. 标题概括本次改动；描述写需求摘要、改动要点、测试结果（依据 test-report.md）和已知限制。不得合并 MR/PR，不得推送主分支或强推。\\n5. 把每个仓库的 MR/PR 链接、源分支、目标分支和提交 SHA 写到 mr-body.md。\\n6. 推送或创建 MR/PR 失败（缺少凭据、无权限、网络错误等）时不得提交 pass：在 mr-body.md 记录失败原因，提交 human，并在 actionRequired 写明需要人工处理什么，例如配置 Git Token 后重试，或到托管平台手动创建 MR。\\n\\nMR 约束：{{existingMR}}\\n\\n完成本节点约定的工作并交付产物。\\n\\n### 输入\\n\\n- 任务需求：{{input}}\\n- 仓库上下文：{{repository}}", "loop": {"enabled": true, "maxIterations": 2, "exitCondition": "all_gates_pass", "onExhausted": "human"}, "verification": [{"id": "mr-pushed", "name": "改动已提交并推送", "mode": "command", "stage": "after_execution", "commands": ["fail=0\\ntab=\\"$(printf ''\\\\t'')\\"\\nwhile IFS=\\"$tab\\" read -r d work base; do\\n  [ -n \\"$d\\" ] || continue\\n  dirty=\\"$(git -C \\"$d\\" status --porcelain --untracked-files=all -- . \\":(exclude).agentflow\\" \\":(exclude,glob)**/node_modules/**\\")\\" || { printf \\"%s 不是可用的 Git 检出目录\\\\n\\" \\"$d\\"; fail=1; continue; }\\n  if [ -n \\"$dirty\\" ]; then printf \\"%s 有未提交的改动：\\\\n%s\\\\n\\" \\"$d\\" \\"$dirty\\"; fail=1; continue; fi\\n  refs=HEAD\\n  if git -C \\"$d\\" rev-parse -q --verify \\"refs/heads/$work\\" >/dev/null; then refs=\\"HEAD refs/heads/$work\\"; fi\\n  ahead=\\"$(git -C \\"$d\\" rev-list --count $refs --not \\"refs/remotes/origin/$base\\")\\" || { printf \\"%s 找不到目标分支 origin/%s\\\\n\\" \\"$d\\" \\"$base\\"; fail=1; continue; }\\n  [ \\"$ahead\\" != 0 ] || continue\\n  branch=\\"$(git -C \\"$d\\" branch --show-current)\\"\\n  if [ \\"$branch\\" != \\"$work\\" ]; then printf \\"%s 当前在 %s，不在本轮工作分支 %s\\\\n\\" \\"$d\\" \\"${branch:-分离的 HEAD}\\" \\"$work\\"; fail=1; continue; fi\\n  head=\\"$(git -C \\"$d\\" rev-parse HEAD)\\"\\n  pushed=\\"$(git -C \\"$d\\" ls-remote origin \\"refs/heads/$work\\")\\" || { printf \\"%s 无法读取远端 origin\\\\n\\" \\"$d\\"; fail=1; continue; }\\n  if [ \\"${pushed%%\\"$tab\\"*}\\" != \\"$head\\" ]; then printf \\"%s 的工作分支 %s 在 origin 上不是 %s，本轮提交还没推送\\\\n\\" \\"$d\\" \\"$work\\" \\"$head\\"; fail=1; fi\\ndone <<EOF\\n$AGENTFLOW_WORK_BRANCHES\\nEOF\\nexit \\"$fail\\""], "required": true, "onFailure": "loop", "executionScope": "run"}, {"id": "mr-opened", "name": "MR 已创建", "mode": "mr", "stage": "after_execution", "required": true, "onFailure": "loop"}]}' AS JSON)), updated_at = UTC_TIMESTAMP()
WHERE id = 'issue-fix-production'
  AND JSON_EXTRACT(payload, '$.Workflow.nodes') = CAST(REPLACE(@lf_033_nodes, '__template__', id) AS JSON)
  AND JSON_EXTRACT(payload, '$.Workflow.edges') = CAST(@lf_033_edges AS JSON)
  AND JSON_SEARCH(payload, 'one', 'issue-fix-production-mr', NULL, '$.Agents[*].id') IS NULL;
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[5].prompt', '工作领域：缺陷根因、复现与回归。核对各阶段实际产物与验证证据，将完成内容、真实测试结果、已知限制及交付物清单写到 delivery.md。未绑定仓库时交付任务工作区中的源码和报告，无需 Git Token。代码已由「发起 MR」节点提交、推送并创建 MR/PR：按 mr-body.md 把链接、分支和提交 SHA 写进交付物清单；本节点不再修改代码、推送或创建 PR，不得合并。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}'), updated_at = UTC_TIMESTAMP()
WHERE id = 'issue-fix-production'
  AND ROW_COUNT() = 1
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[5].id')) = 'issue-fix-production-deliver'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[5].prompt')) = '工作领域：缺陷根因、复现与回归。核对各阶段实际产物与验证证据，将完成内容、真实测试结果、已知限制及交付物清单写到 delivery.md。未绑定仓库时交付任务工作区中的源码和报告，无需 Git Token；仅在用户明确要求且已配置目标 GitHub 仓库及凭据时创建 PR，不得自行合并。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}';
COMMIT;

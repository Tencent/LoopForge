-- Run after 033_generic_sop_mr_node.sql and the Git credential cutover.
-- Replace the shipped authentication instruction in the built-in MR roles.
--
-- JSON_TABLE returns its string columns with the server's default collation
-- (utf8mb4_0900_ai_ci on MySQL 8), while a schema created by compose.yaml uses
-- utf8mb4_unicode_ci. Comparing the two directly fails with
-- "Illegal mix of collations ... for operation '='" (ERROR 1267) on Compose
-- deployments, so the join compares both sides under one explicit collation.
SET NAMES utf8mb4;

SET @old_mr_auth = 'GitHub 仓库优先用已配置的 GitHub MCP，没有时用 curl 调 GitHub API，令牌取 $AGENTFLOW_GIT_TOKEN；其它托管平台用已配置的对应 MCP。';
SET @new_mr_auth = '使用托管平台的 CLI（如 gh、glab）或已配置的 MCP，也可查阅该平台官方 API。调用 API 需要令牌时，用 git credential fill 按仓库主机获取，只在进程内存中使用。';

UPDATE generic_templates t
JOIN JSON_TABLE(t.payload, '$.Agents[*]' COLUMNS (
    agent_order FOR ORDINALITY,
    agent_id VARCHAR(64) PATH '$.id',
    agent_prompt TEXT PATH '$.prompt'
)) a ON a.agent_id COLLATE utf8mb4_unicode_ci = CONCAT(t.id, '-mr') COLLATE utf8mb4_unicode_ci
SET t.payload = JSON_SET(t.payload, CONCAT('$.Agents[', a.agent_order - 1, '].prompt'),
    REPLACE(a.agent_prompt, @old_mr_auth, @new_mr_auth)),
    t.updated_at = UTC_TIMESTAMP()
WHERE t.id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production')
  AND INSTR(a.agent_prompt, @old_mr_auth) > 0;

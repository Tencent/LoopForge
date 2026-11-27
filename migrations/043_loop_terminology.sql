-- Align shipped template names and copy with the LOOP terminology.
SET NAMES utf8mb4;
START TRANSACTION;

UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Workflow.name', REPLACE(name, ' SOP', ' LOOP')),
    name = REPLACE(name, ' SOP', ' LOOP')
WHERE (id, BINARY name) IN (
    ('generic-backend-delivery', '通用后端交付 SOP'),
    ('generic-frontend-delivery', '通用前端交付 SOP'),
    ('issue-fix-production', '通用缺陷修复 SOP'),
    ('generic-product-delivery', '通用产品方案 SOP')
)
AND BINARY JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Workflow.name')) = BINARY name;

-- Compare the exact shipped prompts from 033 and 038 (also used by the previous seed).
-- Platform admins can customize a generic template in place, so its other
-- fields and any edited MR prompt must retain their original text.
UPDATE generic_templates AS t
JOIN JSON_TABLE(t.payload, '$.Agents[*]' COLUMNS (
    agent_order FOR ORDINALITY,
    agent_id VARCHAR(64) PATH '$.id',
    agent_prompt LONGTEXT CHARACTER SET utf8mb4 PATH '$.prompt'
)) AS a ON BINARY a.agent_id = BINARY CONCAT(t.id, '-mr')
SET t.payload = JSON_SET(t.payload, CONCAT('$.Agents[', a.agent_order - 1, '].prompt'),
    REPLACE(a.agent_prompt, '和 SOP 阶段文档', '和 LOOP 阶段文档'))
WHERE t.id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production')
  AND SHA2(a.agent_prompt, 256) IN (
      'dc4bb66f5a301f0affc8ddb29683012947cffbc798e66ef1b47ce42dc104754b',
      '68b834a6f74ced5f28dcec1e3b555211c5ccac5b3cde282c7c3cf3a428dbb27d',
      '8a1d9ff9abc4a995d744b8bf2dbdf90f2b934fd0992d96eb8ca702f90d748153',
      'f03ad1ab67800b281c5ea3506e1b027c713b41de8ae4663c1f03cbfd2c678010',
      '1dbe8250fcab0ed40f528b95985646191fb1ba110b39e95a88dd391dc4b1d3f0',
      '0405772fea2df276928fa7660515d1170c5e6522466b5bdf745e61b3d0da04b7'
  );

UPDATE projects
SET payload = JSON_SET(payload, '$.description', '从创建需求、选择 LOOP 到查看运行与审批，体验完整交付流程。')
WHERE id = 'welcome'
AND BINARY JSON_UNQUOTE(JSON_EXTRACT(payload, '$.description')) = '从创建需求、选择 SOP 到查看运行与审批，体验完整交付流程。';

COMMIT;

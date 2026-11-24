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

-- Older installs can have the original MR text from migration 033.
UPDATE generic_templates
SET payload = REPLACE(payload, '和 SOP 阶段文档', '和 LOOP 阶段文档')
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production')
AND LOCATE('和 SOP 阶段文档', payload) > 0;

UPDATE projects
SET payload = JSON_SET(payload, '$.description', '从创建需求、选择 LOOP 到查看运行与审批，体验完整交付流程。')
WHERE id = 'welcome'
AND BINARY JSON_UNQUOTE(JSON_EXTRACT(payload, '$.description')) = '从创建需求、选择 SOP 到查看运行与审批，体验完整交付流程。';

COMMIT;

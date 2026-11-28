-- 044 通用产品方案交付：独立评审不通过时打回「方案设计」返工。
-- Manual data migration. Before this the review node had no rejectTo, so a
-- REJECT (submit_gate_result rework/fail) failed the whole run instead of
-- sending design.md back for another pass. Rework budget: 2.
-- Only touches the template when node[2] is still the untouched review node
-- without a rework target; operator-edited templates are left alone.
SET NAMES utf8mb4;

UPDATE generic_templates
SET payload = JSON_SET(payload,
      '$.Workflow.nodes[2].rejectTo', 'design',
      '$.Workflow.nodes[2].maxReworks', 2),
    updated_at = NOW(3)
WHERE id = 'generic-product-delivery'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Workflow.nodes[2].id')) = 'review'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Workflow.nodes[2].agentId')) = 'generic-product-delivery-review'
  AND JSON_EXTRACT(payload, '$.Workflow.nodes[2].rejectTo') IS NULL;

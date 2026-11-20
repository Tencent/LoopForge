-- Optional upgrade for existing installations. Run after the prior catalog
-- migrations with template edits paused. New seeds already inherit the CLI.
-- Keep edited roles, explicit models and existing run snapshots.
SET NAMES utf8mb4;
START TRANSACTION;

UPDATE generic_templates AS target
JOIN (
  SELECT id, inherited_agents
  FROM (
    SELECT id, agent_order,
      JSON_ARRAYAGG(IF(inherit_cli, JSON_REMOVE(definition, '$.executorId'), definition))
        OVER (PARTITION BY id ORDER BY agent_order ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS inherited_agents,
      SUM(inherit_cli) OVER (PARTITION BY id) AS changed
    FROM (
      SELECT t.id, a.agent_order, a.definition,
        (a.version = 1 AND a.executor_id = 'codebuddy'
          AND COALESCE(a.model, '') IN ('', 'default')
          AND COALESCE(a.effort, '') = ''
          AND a.agent_id REGEXP CONCAT('^', t.id, '-(clarify|design|implement|review|test|deliver|mr)$')) AS inherit_cli
      FROM generic_templates AS t
      JOIN JSON_TABLE(t.payload, '$.Agents[*]' COLUMNS (
        agent_order FOR ORDINALITY,
        definition JSON PATH '$',
        agent_id VARCHAR(64) PATH '$.id',
        version INT PATH '$.version',
        executor_id VARCHAR(64) PATH '$.executorId',
        model VARCHAR(128) PATH '$.model',
        effort VARCHAR(64) PATH '$.effort'
      )) AS a
      WHERE t.id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
    ) AS candidates
  ) AS templates_with_order
  WHERE agent_order = 1 AND changed > 0
) AS migrated ON migrated.id = target.id
SET target.payload = JSON_SET(target.payload, '$.Agents', JSON_EXTRACT(migrated.inherited_agents, '$')),
    target.updated_at = UTC_TIMESTAMP();

UPDATE agents
SET payload = JSON_REMOVE(payload, '$.executorId'), updated_at = UTC_TIMESTAMP()
WHERE id = 'baseline-align-resolver'
  AND JSON_EXTRACT(payload, '$.version') = 1
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.executorId')) = 'codebuddy'
  AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.model')), '') IN ('', 'default')
  AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.effort')), '') = '';

COMMIT;

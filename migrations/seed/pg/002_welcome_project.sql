-- Postgres 版欢迎项目种子（对应 migrations/seed/002_welcome_project.sql）。
--
-- 显式创建的本地欢迎项目，不需要已存在的账号：admin 存在时由它持有项目，
-- 否则项目无主，交由平台管理员处理。
--
-- 与 MySQL 版的差异：
-- - 字符串字面量一律写成 E'...'：与 MySQL 的解码规则逐字节一致。
-- - MySQL 的用户变量 @welcome_owner 换成标量子查询
--   (SELECT id FROM users WHERE login_name = 'admin' LIMIT 1)：没有 admin 时它是
--   NULL，与 MySQL 一致——项目照样插入，只是无主。Postgres 里不带 FROM 的 SELECT
--   恒返回一行，所以这里不会退化成零行。
-- - JSON_SET -> jsonb_set（路径改用 Postgres 的数组写法 '{ownerId}' / '{members}'）；
--   JSON_ARRAY() -> '[]'::jsonb；
--   JSON_ARRAY(JSON_OBJECT(...)) -> jsonb_build_array(jsonb_build_object(...))。
-- - FROM DUAL 删除：Postgres 的 SELECT 不带 FROM 时恒返回一行。
-- - 冲突处理改成 ON CONFLICT DO NOTHING；UTC_TIMESTAMP() -> now()。
-- - MySQL 版的字符集与事务控制语句已删除：psql 以 ON_ERROR_STOP=1 单事务执行。

INSERT INTO projects (id, project_key, name, owner_user_id, payload, created_at, updated_at)
SELECT 'welcome',
       'LOOP',
       'LoopForge 体验项目',
       (SELECT id FROM users WHERE login_name = 'admin' LIMIT 1),
       jsonb_set(
         jsonb_set(
           E'{"id": "welcome", "key": "LOOP", "name": "LoopForge 体验项目", "description": "从创建需求、选择 LOOP 到查看运行与审批，体验完整交付流程。", "color": "#3867f4", "workflowId": "welcome-workflow", "clarification": {"enabled": false}, "createdAt": "2026-10-15T00:00:00Z", "updatedAt": "2026-10-15T00:00:00Z"}'::jsonb,
           '{ownerId}',
           to_jsonb(COALESCE((SELECT id FROM users WHERE login_name = 'admin' LIMIT 1), 0))
         ),
         '{members}',
         CASE
           WHEN (SELECT id FROM users WHERE login_name = 'admin' LIMIT 1) IS NULL
             THEN '[]'::jsonb
           ELSE jsonb_build_array(
                  jsonb_build_object(
                    'userId', (SELECT id FROM users WHERE login_name = 'admin' LIMIT 1),
                    'role', 'maintainer'
                  )
                )
         END
       ),
       now(),
       now()
ON CONFLICT DO NOTHING;

INSERT INTO project_members (project_id, user_id, role, created_at)
SELECT 'welcome',
       (SELECT id FROM users WHERE login_name = 'admin' LIMIT 1),
       'maintainer',
       now()
WHERE (SELECT id FROM users WHERE login_name = 'admin' LIMIT 1) IS NOT NULL
ON CONFLICT DO NOTHING;

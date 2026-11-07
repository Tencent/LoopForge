-- Explicit welcome project for local installations. It does not require an
-- existing account: when `admin` exists it owns the project, otherwise the
-- project is ownerless and managed by platform administrators.
SET NAMES utf8mb4;
START TRANSACTION;
SET @welcome_owner := (SELECT id FROM users WHERE login_name='admin' LIMIT 1);
INSERT IGNORE INTO projects (id,project_key,name,owner_user_id,payload,created_at,updated_at)
VALUES ('welcome','LOOP','LoopForge 体验项目',@welcome_owner,JSON_SET('{"id": "welcome", "key": "LOOP", "name": "LoopForge 体验项目", "description": "从创建需求、选择 SOP 到查看运行与审批，体验完整交付流程。", "color": "#3867f4", "workflowId": "welcome-workflow", "clarification": {"enabled": false}, "createdAt": "2026-10-15T00:00:00Z", "updatedAt": "2026-10-15T00:00:00Z"}','$.ownerId',COALESCE(@welcome_owner,0),'$.members',IF(@welcome_owner IS NULL,JSON_ARRAY(),JSON_ARRAY(JSON_OBJECT('userId',@welcome_owner,'role','maintainer')))),UTC_TIMESTAMP(),UTC_TIMESTAMP());
INSERT IGNORE INTO project_members (project_id,user_id,role,created_at) SELECT 'welcome',@welcome_owner,'maintainer',UTC_TIMESTAMP() FROM DUAL WHERE @welcome_owner IS NOT NULL;
COMMIT;

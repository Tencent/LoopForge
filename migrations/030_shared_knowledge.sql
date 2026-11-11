-- Run manually on the control database after 026_builtin_knowledge.sql.
-- No content-plane DDL: D11 uses the existing bases and bindings tables.
-- Safe to repeat. Shared libraries never grant project write permissions.
UPDATE project_knowledge_bindings AS binding
JOIN knowledge_bases AS base ON base.id = binding.base_id
SET binding.permission = 'read'
WHERE base.layer = 'shared' AND binding.permission <> 'read';

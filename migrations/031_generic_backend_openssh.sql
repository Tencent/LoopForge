-- Idempotent MySQL migration: add openssh-client to the built-in
-- generic-backend Dockerfile.
--
-- The platform image and its built-in sandbox Dockerfile gained openssh-client
-- for SSH repositories, but the seeded generic-backend row kept the old text.
-- A sandbox whose Dockerfile matches the built-in one runs on the platform
-- image; the stale row instead builds a separate image on first use, without
-- ssh. Only the unmodified previous default is rewritten, so an edited
-- Dockerfile is left alone. scripts/install-schema-extensions.sh runs this on
-- install and on ./start.sh pull.

UPDATE dockerfiles
SET payload = JSON_SET(payload, '$.content', 'FROM node:22-bookworm\nRUN apt-get update && apt-get install -y --no-install-recommends git openssh-client python3 ripgrep ca-certificates curl && rm -rf /var/lib/apt/lists/*\nRUN npm install -g @openai/codex @anthropic-ai/claude-code @tencent-ai/codebuddy-code\nWORKDIR /workspace\n')
WHERE id = 'generic-backend'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.content')) = 'FROM node:22-bookworm\nRUN apt-get update && apt-get install -y --no-install-recommends git python3 ripgrep ca-certificates curl && rm -rf /var/lib/apt/lists/*\nRUN npm install -g @openai/codex @anthropic-ai/claude-code @tencent-ai/codebuddy-code\nWORKDIR /workspace\n';

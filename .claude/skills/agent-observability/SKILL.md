---
name: agent-observability
description: Add or maintain CodeBuddy hook observability for projects that need local metrics for tool latency, transcript token usage, session cost, and multi-agent trace attribution. Use when wiring hook-based telemetry, debugging missing usage events, and validating AgentLens state.
---

## Workflow

1. Merge `templates/settings-hook.json` into the user or project `settings.json`.
2. Keep hook entries command-only and keep timeouts around 5 to 10 seconds.
3. Run a normal CodeBuddy session and inspect `logs/metrics.ndjson`.
4. Inspect `logs/.state.json` when usage replay, offsets, or AgentLens turn state look wrong.
5. Keep the runtime generic and verify output from `metrics.ndjson` and `.state.json`.

## Read Next

- Read `references/quickstart.md` for setup and smoke-test steps.
- Read `references/schema-v2.md` when you need field definitions or sidecar state shape.
## Key Files

- `scripts/main.py`: hook entrypoint for `session-start`, `user-prompt-submit`, `pre`, `post`, and `stop`
- `scripts/run_hook.sh`: stable shell wrapper for CodeBuddy hook commands
- `scripts/core/collector.py`: transcript parsing, pre/post pairing, and session usage recording
- `scripts/core/agentlens.py`: AgentLens trace emission and agent or step grouping
- `scripts/core/state.py`: persisted hook state, offsets, and AgentLens sidecar state
- `scripts/core/devflow.py`: optional devflow-awareness — detects a `multi-agents-devflow-*` team, reads `workflow-state.json`, and emits `stage_transition` events. No-ops entirely on non-devflow projects.
- `templates/settings-hook.json`: hook wiring template

## Output Contract

- Always emit `event`, `sid`, and `ts`.
- Emit `tool` for per-call latency and `usage` for transcript-derived token and cost events.
- Emit `stop` for end-of-session flush.
- Never block the main hook flow; hook exits must stay `0`.

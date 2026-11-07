# Acceptance matrix

Which automated cases protect each user-facing capability, and what they need. "Fresh" means the case also runs against a clean `scripts/install.sh` installation; "Model" means it calls a real model and only runs with `AGENTFLOW_E2E_MODEL=1`. Results for the current release are in the [verification record](../validation.md).

| Capability | Automated coverage | Needs |
| --- | --- | --- |
| One-command install, first administrator in the browser | `e2e/public/00-first-run.spec.mjs`, CI job `first-run` | Docker · Fresh |
| Self sign-up, signup switch, password rules, duplicate names | `internal/httpapi/auth_register_test.go`, `00-first-run.spec.mjs` | — |
| Password sign-in, origin checks, session cookie, sign-out | `internal/httpapi/auth_password_test.go`, `e2e/public/platform.spec.mjs`, `e2e/api/00-smoke.test.mjs` | — |
| Getting-started checklist, missing-key notice | `00-first-run.spec.mjs`, `e2e/public/composer.spec.mjs` | Fresh |
| Expanded onboarding, persistent dismissal, capability question, optional GitHub, responsive composer | `e2e/public/welcome.spec.mjs`, `references.spec.mjs` | Model for the real capability reply |
| Main Bot executor/model selection and per-role SOP credential readiness | `e2e/public/executors.spec.mjs`, `internal/httpapi/assistant_provider_test.go` | Native CLI gateway fixtures are opt-in; no paid service |
| Tasks without GitHub: script/approval and document Agent | `e2e/public/without-github.spec.mjs` | Model for document Agent |
| Language and theme preferences | `e2e/public/platform.spec.mjs` | — |
| Every navigation view renders without runtime/CSP errors | `e2e/ui/01-views.spec.mjs` | — |
| Projects, rules, Skills, MCP servers, agents, workflows (CRUD) | `e2e/api/10-catalog.test.mjs` | — |
| Requirements: create, search, edit, comments, clarification gate, dispatch, close/reopen, delete | `e2e/api/20-issues.test.mjs`, `e2e/ui/02-interactions.spec.mjs` | — |
| AI requirement analysis | `e2e/ui/02-interactions.spec.mjs` | Model |
| SOP designer: edit nodes, save revisions | `e2e/ui/02-interactions.spec.mjs`, `e2e/api/10-catalog.test.mjs` | — |
| Run engine: approval gates, rework, retry, manual pass, pause/resume, cancel, child SOPs, event stream | `e2e/api/30-runs.test.mjs`, `e2e/public/platform.spec.mjs` | — |
| Artifacts across nodes, contract errors | `e2e/api/30-runs.test.mjs`, `e2e/api/35-runner-mode.test.mjs` | — |
| Connected execution machines (runner bind, heartbeat, tasks) | `e2e/api/35-runner-mode.test.mjs` | Runner binaries |
| Shared sandbox across nodes, Docker sandbox, static preview and session navigation | `e2e/api/36-cloud-pool.test.mjs`, `e2e/api/56-sandbox-preview.test.mjs`, `internal/preview/gateway_test.go` | Docker; actual remote pool needs configured workers; Chromium preview opt-in |
| Agent node with a real executor, AI run evaluation | `e2e/api/40-model.test.mjs` | Model |
| End-to-end delivery SOP: plan, fix, review, test, approve, summarize | `e2e/api/45-delivery-model.test.mjs` | Model |
| Workspace: git history, protected files, conflicts, shell, IDE | `e2e/api/55-workspace.test.mjs`, `e2e/ui/03-workspace.spec.mjs` | — |
| Permission boundaries between accounts | `e2e/api/58-permissions.test.mjs` | — |
| Main Bot conversations, attachments, scheduled prompts | `e2e/api/60-assistant-lifecycle.test.mjs`, `e2e/public/composer.spec.mjs` | Model (reply cases) |
| Task groups and multi-role collaboration | `e2e/api/65-task-groups.test.mjs` | Model |
| Durable group-to-SOP handoff and context retention | `e2e/api/66-sop-handoff.test.mjs`, `internal/httpapi/sop_handoff_jobs_test.go`, `internal/repo/sop_handoff_mysql_test.go` | Model; lease recovery also tested on disposable MySQL |
| Script views isolated from agent instructions | `internal/runtime/sandbox_script_test.go`, `internal/scriptview/` | Linux/Docker cases as supported by the host |
| Data persists across container recreation | `e2e/container-persistence.mjs` via `scripts/test-container.sh` | Docker |
| Rooms, scheduled dispatch, platform MCP tools, credential flags never leaking | `e2e/api/50-platform.test.mjs` | — |
| Knowledge search and review queue | `e2e/api/50-platform.test.mjs`, `e2e/ui/02-interactions.spec.mjs` | Knowledge service |
| GitHub issue import, PR discovery and merge | `internal/tracker/github_test.go`, `internal/tracker/merge_mr_test.go`, `internal/runtime/github_mcp_test.go`, live: `internal/tracker/github_live_test.go` | Live: `AGENTFLOW_GITHUB_LIVE_WRITE=1`, a disposable repository and token file |
| Repository write permissions | `internal/httpapi/repository_authz_test.go` | — |
| Invocation cleanup preserves unrelated active runs | `e2e/api/05-cleanup-scope.test.mjs` | — |
| LoopForge Local workflows, installers, dashboard | `local/tests/e2e/` | Python · CodeBuddy for live host runs |

## Not covered automatically

- Paid Codex and Claude Code executions; installed native CLIs, custom gateway protocols, MCP calls, tool restrictions and credential boundaries are exercised with local fixture services.
- WeCom notification bots and knowledge services, which need operator accounts.

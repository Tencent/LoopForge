# Acceptance matrix

Which automated cases protect each user-facing capability, and what they need. "Fresh" means the case also runs against a clean `scripts/install.sh` installation; "Model" means it calls a real model and only runs with `AGENTFLOW_E2E_MODEL=1`. Results for the current release are in the [verification record](../validation.md).

| Capability | Automated coverage | Needs |
| --- | --- | --- |
| One-command install, first administrator in the browser | `e2e/public/00-first-run.spec.mjs`, CI job `first-run` | Docker · Fresh |
| Self sign-up, signup switch, password rules, duplicate names | `internal/httpapi/auth_register_test.go`, `e2e/public/00-first-run.spec.mjs` | — |
| Password sign-in, origin checks, session cookie, sign-out | `internal/httpapi/auth_password_test.go`, `e2e/public/platform.spec.mjs`, `e2e/api/00-smoke.test.mjs` | — |
| Getting-started checklist, missing-key notice | `e2e/public/00-first-run.spec.mjs`, `e2e/public/composer.spec.mjs` | Fresh |
| Expanded onboarding, persistent dismissal, capability question, optional GitHub, responsive composer | `e2e/public/welcome.spec.mjs`, `e2e/public/references.spec.mjs` | Model for the real capability reply |
| Main Bot executor/model selection and per-role SOP credential readiness | `e2e/public/executors.spec.mjs`, `internal/httpapi/assistant_provider_test.go` | Native CLI gateway fixtures are opt-in; no paid service |
| Tasks without GitHub: script/approval and document Agent | `e2e/public/without-github.spec.mjs` | Model for document Agent |
| Language and theme preferences | `e2e/public/platform.spec.mjs` | — |
| Every navigation view renders without runtime/CSP errors | `e2e/ui/01-views.spec.mjs` | — |
| AI workbench conversations, credential wizard, notification bots, and domain admin | `e2e/ui/03-workbench-settings.spec.mjs` | No model or WeCom call |
| SkillHub market browsing | `e2e/ui/04-skillhub-market.spec.mjs` | SkillHub proxy is mocked; nothing is installed |
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
| Human takeover and compare-and-swap edits in a run workspace | `e2e/api/68-workspace-edit.test.mjs` | Repository fixture; does not push |
| Permission boundaries between accounts | `e2e/api/58-permissions.test.mjs` | — |
| Authentication boundaries and project role permissions | `e2e/api/64-auth-permissions.test.mjs` | Optional second user for the role walk |
| Main Bot conversations, attachments, scheduled prompts | `e2e/api/60-assistant-lifecycle.test.mjs`, `e2e/public/composer.spec.mjs` | Model (reply cases) |
| Assistant conversations without a model: transcript, rename, archive, branch, and sharing | `e2e/api/69-assistant-sessions.test.mjs` | — |
| Requirement spaces: CRUD, timeline, threads, artifacts, and members | `e2e/api/60-spaces.test.mjs` | — |
| Task groups and multi-role collaboration | `internal/httpapi/rooms_test.go`, `internal/repo/group_run_view_test.go`, `internal/runtime/workspace_preparation_test.go`, `webtest/tests/collaboration-groups.test.mjs` | No model |
| Group SOP handoff artifact inheritance | `internal/engine/state_helpers_cases_test.go`, `internal/engine/ops_validation_cases_test.go` | Engine `GroupSOPHandoff` only. `migrations/015_sop_handoff_jobs.sql` creates `sop_handoff_jobs` and `migrations/017_bot_sop_controls.sql` alters it, but no Go service reads or writes that table, so the job table is outside automatic coverage |
| Script views isolated from agent instructions | `internal/runtime/sandbox_script_test.go`, `internal/scriptview/` | Linux/Docker cases as supported by the host |
| Data persists across container recreation | `e2e/container-persistence.mjs` via `scripts/test-container.sh` | Docker |
| Rooms, scheduled dispatch, platform MCP tools, credential flags never leaking | `e2e/api/50-platform.test.mjs` | — |
| Project integrations: notification bots, insight sources, business domains, and WeCom identity bindings | `e2e/api/62-integrations.test.mjs` | No outbound WeCom or insight call |
| Knowledge search and review queue | `e2e/api/50-platform.test.mjs`, `e2e/ui/02-interactions.spec.mjs` | Knowledge service |
| GitHub issue import, PR discovery and merge | `internal/tracker/github_test.go`, `internal/tracker/merge_mr_test.go`, `internal/runtime/github_mcp_test.go`, live: `internal/tracker/github_live_test.go` | Live: `AGENTFLOW_GITHUB_LIVE_WRITE=1`, a disposable repository and token file |
| GitHub credential row and the separate manual path | `e2e/public/github-setup.spec.mjs` | No live GitHub write |
| Repository write permissions | `internal/httpapi/repository_authz_test.go` | — |
| Invocation cleanup preserves unrelated active runs | `e2e/api/05-cleanup-scope.test.mjs` | — |
| Deterministic route edges: validation, unknown resources, and missing credentials | `e2e/api/90-route-sweep.test.mjs` | No model, knowledge write, or external ticket |
| NLForge Local workflows, installers, dashboard | `local/tests/e2e/` | Python · CodeBuddy for live host runs |

## Not covered automatically

- Paid Codex and Claude Code executions; installed native CLIs, custom gateway protocols, MCP calls, tool restrictions and credential boundaries are exercised with local fixture services.
- WeCom notification bots and knowledge services, which need operator accounts.

# Verification record

Review resumed on 2026-10-16 against recorded upstream baseline `3a8cb2420904e3ee8ab1b39c48f54b372a9e3e04`. Source and running-preview freshness are checked independently. External-service coverage is kept separate from local protocol/adapter tests. The older results below retain their original scope.

## Hosted CI follow-up — 2026-10-16

The first hosted runs exposed three differences from the local review environment: the no-checkout E2E gate inherited a nonexistent `local/` working directory, Node 22 resolved the API directory as a module rather than discovering its test files, and Git 2.55 rejected tracked-file staging with no eligible index entries. The gate now runs in the runner's temporary directory while preserving its failure checks; API launchers name `*.test.mjs` explicitly; staging skips the update only when no eligible tracked entries exist, retaining exclusions and normal error handling. A regression covers empty/excluded-only checkouts and source additions, updates and deletions.

Node 22 API acceptance passed **100 tests, 13 skipped, 0 failed**. With Git 2.55 built locally, the original implementation reproduced the hosted pathspec failure; the corrected Git tests and five repetitions of `TestGateExecutionScopeIsExplicit` passed. The hosted fresh-install job had already passed independently. Local evidence is in ignored `.local/ci-node22-api.log`, `.local/ci-git-before.log` and `.local/ci-git255-full-go.log`. These results supplement the earlier submission checks; the current hosted commit must still pass its own jobs.

After those fixes, hosted `core`, E2E (including its required gate) and fresh-install checks passed. The API suite then reached a previously blocked Docker case: the non-root host service owned a `0700` workspace, while the root image with all capabilities dropped could not enter it. Default/root images now use the native service UID/GID when that service is non-root; explicit non-root image users are retained. A real Docker regression as UID 1001 reproduced the original `cd /workspace` failure and passed after the fix for both normal commands and isolated scripts, checking host-readable output, unchanged `0700` permissions, zero effective capabilities and `NoNewPrivs=1`. Evidence: ignored `.local/ci-docker-owner/before.log` and `after.log`. Runtime/script-view package tests and runtime vet also passed. No sandbox test was disabled.

## Final PR submission checks — 2026-10-16

The owner requested a final source review and public PR submission. The candidate was checked again, with these results:

- Source provenance: **1,718 source blobs verified**, matching the recorded master; **1,466 unchanged, 178 adapted, 74 excluded, 58 audited public additions** in the platform export scope. The public-content checker passed. No private history is imported.
- Full Go suite and `go vet -p=2 ./...`: **passed** on the corrected candidate. An earlier full run passed; a subsequent run exposed `TestReadyPreviewReleasesWorkspaceCleanup` racing asynchronous cleanup. The corrected test passed **40 repetitions**, then the full suite passed again. Logs: `.local/publication-fixed-go.log`, `.local/publication-fixed-vet.log`.
- Runner cancellation: fixed repeated cancellation handling and competing process waits. Real-process, output-drain and managed early-failure regressions passed **5 repetitions**; the cancellation/early-failure subset also passed **3 repetitions with `-race`**. Logs: `.local/publication-cancel.log`, `.local/publication-cancel-race.log`.
- Real HTTP/API suite on the independent acceptance installation: **100 passed, 13 skipped, 0 failed**. Real models, Docker/preview and external knowledge cases were not enabled for this final run; the earlier live evidence below has its own scope. Log: `.local/publication-api.log`.
- Public Chromium regression on the independent acceptance installation: **20 passed, 6 skipped, 0 failed**. The skips require an empty installation or explicit live-model credentials. Verified the served assistant, application, SOP, stylesheet and English-dictionary assets byte-for-byte against this checkout. Log: `.local/publication-browser-current.log`.
- The initial browser run targeted the older native preview on port 8091: **17 passed, 3 failed, 6 skipped**. All three executor-related failures came from the executable lacking the current executor UI; the same three cases passed on the current acceptance installation. That preview was not restarted by this review. Log: `.local/publication-browser.log`.
- Local workflow validation, observability, source installation, npm/npx installation and curl installation: **passed**. Deterministic Local E2E: **26 tests, 1 skipped**, exit code 0; the Python Playwright module is unavailable in this invocation, so this rerun does not claim fresh Local browser coverage. Earlier browser results remain below.
- Gitleaks: **passed** for the complete publication candidate and Local package. Exact comparisons found no occurrence of the three configured local `.env` secrets in candidate files. Indexed paths contain no local state, acceptance credentials or reports. Logs: `.local/publication-final-secrets.log`, `.local/publication-local-secrets.log`.
- Go dependency license bundle: **86 modules verified**. `govulncheck` reported **0 affected vulnerabilities** (one module-level advisory had no affected package/symbol reachability); E2E `npm audit --audit-level=high` reported **0 vulnerabilities**.

These ignored logs and credentials are local evidence and are excluded from the PR. GitHub-hosted CI is a separate gate on the submitted commit. The review submits source for trusted-team self-hosting; it does not merge the PR or publish a release, package or image. See [release-readiness.md](release-readiness.md#existing-pull-requests) for the disposition of PRs #46 and #47.

### Before/after verification requested by the owner

Read-only Go overlays supplied the original recorded source for comparison without replacing checkout files. The original runner failed its real exit-status regression **5 of 10** repetitions with `waitid: no child processes`; the corrected cancellation/early-failure subset passed **3 repetitions with `-race`**. The original preview-cleanup test failed **2 of 150** repetitions; the corrected test passed **150 of 150**. This confirms a process-wait defect and a separate asynchronous test race. Evidence is retained in ignored `.local/change-proof/{runner-before,runner-after,preview-before,preview-after}.log`.

The review's production-code change is confined to `internal/runner/exec.go`, with regression tests in `internal/runner/exec_cancel_test.go`. The preview correction changes only `internal/engine/preview_finalizer_test.go`. Formatting-only changes to `.gitattributes` and the preview design proposal were restored to their exact pre-review hashes. Audit/documentation changes record these results. The owner explicitly retired PR #47's separate redesign/room-crew proposal; PRs #46 and #47 were closed, with #48 retained as the replacement platform submission.

## Fresh installations and resumed acceptance — 2026-10-16

Two independent installations were created with `scripts/install.sh`: port 8095 for acceptance and port 8096 for owner experience, with separate Compose projects and databases. The existing port 8091 environment was retained. The experience installation was left at first-account setup by the morning task; subsequent owner activity is not reset by acceptance.

- Fresh-install browser account creation, onboarding and ordinary-member registration: **3 passed**.
- Public browser regression: **22 passed, 3 skipped** (empty-install account cases had already run separately). The final viewport/menu regression: **16 passed**.
- Real API suite: **111 passed, 1 failed, 1 skipped** in the initial final run. The failure was a custom MCP identifier exceeding the database length limit; after its fix, the entire catalog subset passed **12/12**. Do not describe the original full run as having zero failures. External knowledge remained skipped.
- The latest upstream synchronization initially exposed a recovery-test scheduler race. The fixture was given an explicit workflow and its recovered state is inspected under the execution lock. The focused case passed **30 repetitions**, then the resumed full Go suite passed with exit code 0. `go vet -p=2 ./...` also passed.
- Resumed Docker/static-preview acceptance: **2 passed, 0 failed, 0 skipped**, with real Docker execution, a real CodeBuddy build, archived HTML/assets, session access, Chromium SPA navigation, reload, full-page navigation, Back and preview revocation. Node `fetch` overwrites `Sec-Fetch-Mode: navigate` with `cors`; the navigation fixture now uses native HTTP rather than weakening the gateway's navigation check.
- Installed Codex and Claude CLI gateway/MCP and actor-credential cases passed against local protocol services. These are **not paid-provider model runs**.
- Real CodeBuddy delivery and model cases use the supplied IOA credentials with `deepseek-v4.1-flash`. Portable Local passed its live fixture; Classic initially failed in a one-shot command, then passed in a persistent terminal session. Deterministic Local browser/dashboard coverage passed **29 tests**. The live GitHub import/PR/merge fixture passed in the authorized disposable repository.
- The final upstream sync introduced private hostnames in a preview test and design proposal. Both were replaced with reserved public example domains; the preview unit suite and public-content checker passed afterwards. Ignored local credentials and raw reports remain outside the publication scope.

Reproduction uses the existing API/browser instructions and acceptance matrix. Full Go and preview rerun logs are local, ignored artifacts: `.local/resumed-full-go.log`, `.local/resumed-vet.log` and `.local/fresh-latest-preview-resumed.log`. A passing source suite does not assert that concurrently edited model-catalog or style files have already been rebuilt into every running preview.

**Not fully live-tested:** paid Codex/Claude services, external knowledge services, enterprise notifications, actual multi-machine cloud pools, and runtime behavior on every operating system. No new public release, branch or PR was published by this resumed work.

## Home layout review — 2026-10-16

The welcome screen keeps all setup steps expanded above the composer, without a collapse control or the redundant explanation block. GitHub connection is optional and excluded from the four-step getting-started progress count. The missing-key warning uses the original orange badge inside the composer toolbar and an orange input border. The checklist can ask Main Bot about capabilities in one click; it prepares the question and opens settings when the model key is missing, and preserves an existing draft. The no-key demo remains in the checklist. Repository setup aligns the default checkbox with its label and explicitly distinguishes saving an address from configuring a GitHub token.

- **13 browser cases passed**: four viewport/language/theme layouts (1600×1000, 1280×720, 390×844 English/dark, 320×700), draft protection and checklist dismissal, real no-key script demo → artifact → UI approval → completion, missing-model-key question setup, an actual model capability reply, repository save without a GitHub token in Chinese/desktop and English/dark/mobile, credential links, password login and theme persistence. Cases are reusable in `e2e/public/welcome.spec.mjs`. Updated the empty-install case for question → settings navigation.
- The web and engine Go tests passed, including missing Git Token rejection and responsible-user credential selection. The missing-token error now uses provider-neutral wording.
- A final UI adjustment keeps the capability action available after an earlier chat. The four viewport cases additionally verify that this action still preserves an existing draft.
- Rebuilt/restarted the preview and checked its embedded assistant, repository, stylesheet and translation assets against the local files. Provider acceptance recorded below remains separate from this focused follow-up.

- Restored the checklist above the input and the orange missing-key badge/border at the owner’s request. The six focused browser cases passed again, including expanded checklist ordering, warning visibility in light/dark mobile layouts, draft preservation and credential setup. Web Go tests and source/public-content checks also passed.

## Reference menu review — 2026-10-16

The @ menu now measures space inside both the workbench and the visual viewport. It limits its height, prefers space above the composer, uses space below or overlaps the input when needed in a short viewport, and repositions after scrolling, resizing and content changes. Search stays visible during internal scrolling. Referenced object/file names retain their original spelling in the English interface and composer.

- **9 focused browser cases passed**: four real reference-menu journeys (desktop, laptop, 1280×400 short window, English/dark mobile), the four existing welcome-layout regressions and the no-key draft/configuration journey. Tests cover typed @ and the toolbar entry, real SOP search/selection, scrolling, resize and Escape. No model calls or data writes were needed.
- Reusable cases are in `e2e/public/references.spec.mjs`. Web Go tests and source/public-content checks passed. The preview was rebuilt with the current embedded assets; the upper onboarding position and orange missing-key warning were retained.

## Expanded steps and optional GitHub review — 2026-10-16

- **15 browser/API cases passed**: all expanded checklist rows and the composer remain reachable across four viewport/language/theme combinations; the existing @ menu, missing-key, draft, demo and repository setup cases also pass.
- An account with **no model key, no Git token and no repository** completed the script → human approval → continuation workflow. The same project still receives `409 repository_required` when it tries the built-in backend code-delivery SOP.
- With only a temporary CodeBuddy model key, the same account/project ran a real document-writing Agent and a verification script. The run had no repository contexts, the saved Markdown artifact contained the required marker, and the account still had no Git token. Temporary definitions, run and model key were removed afterwards. Reproduce with `e2e/public/without-github.spec.mjs` and an explicitly provided protected key file.
- Web and HTTP API Go tests passed. Browser journeys now reuse a password session in memory within each worker to respect login rate limiting; authentication-specific tests still exercise the form. Failed login helpers clear password fields before generating failure diagnostics.
- The bilingual README now explains when GitHub is optional and when repository credentials are needed. No backend repository or write-permission gate was weakened.

## Welcome spacing review — 2026-10-16

Removed the viewport-sized blank space above expanded onboarding and tightened the title, checklist header/rows and composer gaps. The change applies when onboarding is visible; the empty conversation without onboarding keeps its existing positioning. All steps remain expanded above the input, GitHub remains optional and the orange missing-key warning is retained.

- **9 focused browser cases passed**, including a heading close to the top bar, the complete composer visible without scrolling at 1280×720 and 1600×1000, reachability on both mobile sizes, @ menu clipping/search/selection and the credential/draft journey. The web Go tests and source/public-content checks passed.

## Results

| Area | Result |
| --- | --- |
| Full Go suite, `go test -p=2 ./... -count=1 -timeout=240s` | Passed across all packages, including engine, repository, runtime, runner, HTTP API and JavaScript contract tests |
| `go vet ./...` | Passed |
| Real platform HTTP/API suite | **111 passed, 1 skipped, 0 failed** across 112 tests including subtests, with Docker and real CodeBuddy enabled after rebuilding the latest upstream export. Only external knowledge retrieval was skipped. The final viewer permission patch additionally reran the updated two-account and durable-handoff live cases: **2 passed**, plus the full HTTP handler suite |
| Public Playwright suite | **5 passed** (the three first-run cases need an empty install and run in the container check): password authentication, language/theme persistence, real script artifacts and approval, drafts before credentials, actual model chat and conversation reload |
| Broad Playwright suite | **7 passed, 1 skipped**: every reachable view in welcome and acceptance projects, requirement creation, real AI analysis, run tabs and UI approval, SOP editing, bundled IDE workspace; external knowledge search skipped |
| Production Docker image | Built successfully; verified root license/notice files, bundled CodeBuddy/Codex/Claude CLIs and Linux/macOS amd64/arm64 runner and gate binaries |
| Fresh container installation | `scripts/test-container.sh` on empty volumes after the public deployment changes: **first-run 3 passed** (including automatic demo-project membership), persistence passed, **public 4 passed**, **API 99 passed / 0 failed / 13 skipped**, **UI 6 passed / 2 skipped**; skips need paid models or external knowledge, which the clean-install suite leaves unconfigured |
| Real GitHub | `TestGitHubLiveImportAndDelivery` against the authorized disposable repository: issue import, local clarification preserved across sync, PR created and merged between disposable branches, closed-issue sync, default branch unchanged. In the browser, a new user bound the repository from the checklist, saved and verified a token, and the repository step completed |
| New-user journey from a clean checkout | `bash scripts/install.sh` on empty volumes, then **8 browser tests passed**: administrator created in the browser, onboarding checklist and missing-key notice, 6-character password rule, teammate self sign-up without admin rights, draft-then-configure composer, sign-in, preferences, welcome SOP script/approval, and a real model reply using the fresh install's default model after saving a key |
| Project write permissions | A fresh account that is not a project member received 403 for project settings, repositories, requirement create/edit/dispatch, roles, SOPs and run controls; a Go test covers members, viewers, owners, ownerless projects, clone-onto-existing-ID, workspace terminals, rules, platform Dockerfiles, sync settings and project MCP installs. API, UI, public and clean-container suites were rerun after these changes with the results above |
| Docker sandbox on a container install | The platform container reaches the host daemon (CLI 27.5 → server 26.1, buildx 0.20). The built-in frontend delivery SOP ran on a fresh install with a CodeBuddy key: the sandbox image was built, the GitHub repository checked out, and all 7 stages completed in 47 minutes after explicit human clarification/acceptance, and a feature branch was pushed to the GitHub test repository. The target was a Python fixture, so this verifies orchestration rather than frontend framework compatibility or fully autonomous delivery |
| English interface | A browser sweep in English mode across every view, the approval/model/reference/session menus, the requirement, issue, repository and project modals, the issue drawer tabs and the run detail found no untranslated interface text; remaining Chinese is user data or a literal bot command |
| Assistant process persistence | A real reply with thinking and tool calls was reloaded; the process is stored with the message and shown as a folded "Process · N reasoning steps · M tool calls" block in both languages |
| Onboarding | A new browser account joined the demo project, saw the welcome explanation, started the one-click demo SOP and stepped through the three-bubble tour (step graph, artifacts, approval) in Chinese and English; SOP cards listed API key, repository and Docker requirements |
| Local workflow structural/generator checks | Passed |
| Local installation and packaging | Python/source, npm/npx, release archive and curl installation smoke checks passed |
| Local deterministic workflow E2E, including Chromium dashboard | **29 passed, no skips** |
| Real local CodeBuddy host runs | **Classic and Portable both passed** a small Python fix, real acceptance tests, persisted workflow completion and artifacts |
| Local observability | Passed for the maintained and generated host bundles |
| Optional tcmcp tool | **56 Python tests passed** after building the web client; frontend TypeScript and production build passed |
| License packaging | npm/release installation checks passed; Python wheel inspected for the complete upstream copyright and permission notice |
| Public source/provenance checks | Passed |
| Secret checks | Gitleaks passed; the supplied model key was absent from inspected test reports/logs and decompressed trace contents |

## Final upstream-specific checks

- `python3 scripts/check-upstream.py --source <private-checkout>` checked all **1,715** recorded source hashes against the actual Git blobs and confirmed that the recorded commit matches fetched `origin/master`. Public code adaptations and private deployment exclusions remain explicit.
- `TestSOPHandoffMySQLConcurrentClaimsAndLeaseRecovery` passed against an isolated disposable MySQL database. The fixture container was removed after the check.
- Both opt-in Docker script-view cases passed: dependency removal with instruction masks and preservation of non-root execution identity. Reproduce with `AGENTFLOW_DOCKER_SCRIPT_SMOKE=1`, a local `alpine:3.20` fixture and the current gate binary.
- `e2e/api/66-sop-handoff.test.mjs` passed with real CodeBuddy/MySQL: user context persists, repeated requests reuse the active job, the summary is archived, and the selected SOP approval must still be explicitly accepted before continuation.
- The viewer patch is covered by `project_write_access_test.go`, `assistant_workbench_test.go` and the extended `e2e/api/58-permissions.test.mjs`. A viewer can read a run conversation but cannot turn that access into group dispatch, SOP control, handoff retry or workspace execution.
- The updated preview is supervised on port 8091. Its `assistant.js`, `collaboration.js` and `app/issues.js` contents matched the current embedded source. Preview freshness is not inferred solely from the JSON baseline label.
- Go vulnerability checks reported **zero reachable vulnerabilities** and zero vulnerabilities in imported packages. The dependency graph still includes an unused OpenPGP package with a database advisory and no fixed version; that package is not imported by this application. npm audit and the Go dependency notice check passed.

Earlier Local/host/GitHub journey results below are retained from the same day's adaptation review; no additional paid Codex/Claude run is implied.

## Real model coverage

The requested `deepseek-v4.1-flash` model was exercised with the acceptance account's own CodeBuddy credentials and matching authentication environment. No credentials are stored in source or test fixtures.

- An Agent node wrote a declared artifact; a later script checked the file's contents. The suite checked token usage, telemetry and artifact download.
- A complete delivery SOP planned a change, fixed Python code, independently reviewed it, passed three actual tests, waited for human approval and generated a delivery summary. A final script reran the tests and checked that delivery had not changed the accepted code hash. All four model roles recorded real token usage.
- AI evaluation completed for the finished run.
- The assistant answered a read-only project question and streamed a reply over SSE.
- The browser submitted a real home-workbench message, observed a completed reply, reloaded and checked the server-side conversation.
- Requirement analysis completed through the real browser UI.
- Classic and Portable local workflows each ran inside a real CodeBuddy process in a disposable project, fixed the fixture, passed its tests and saved completion state and delivery artifacts.

These tests cover the supplied CodeBuddy model. Codex and Claude executors have command/protocol and credential-boundary tests; their paid services were not exercised with separate provider credentials.

## Fixes covered by the checks

The home composer now accepts drafts before model setup, links to the credential field and updates sending availability after credentials are saved. Returning from settings retains the draft. Credential-related browser tests disable traces and screenshots.

Avatar fallbacks and Bot refresh use external JavaScript handlers compatible with the existing Content Security Policy. Whole-view browser checks reject runtime and CSP errors. An explicitly configured assistant model is displayed by its actual model ID.

Fresh MySQL installs explicitly apply the upstream observability generated-column baseline before catalog installation. Both initial and repeat installation were verified; application startup still does not run data migrations.

Third-party notices are maintained at the repository root. Package creation generates the local distribution notice rather than maintaining a second source copy. The local component's entry documentation and shared project policies remain consolidated.

A first visit to an installation without accounts creates the administrator in the browser; afterwards the login page offers self sign-up unless `AGENTFLOW_ALLOW_SIGNUP=false`. The home page shows a dismissible getting-started checklist that tracks the model key, project, first conversation, first run and repository connection, and the composer shows a prominent notice while no key is configured. The welcome project seed no longer requires an `admin` account, and the installer waits for the API before applying seeds (a fresh install previously failed with a missing `run_telemetry` table). Without a photo service, avatars are rendered locally instead of returning 502.

The repository modal opened by the checklist had no submit handler, so saving reloaded the page; it now saves. Repository writes were accepted from any signed-in user; they now require the project owner or a platform administrator.

## Reproduce

- [Platform API/UI instructions](../e2e/README.md)
- [Public browser regression instructions](../e2e/public/README.md)
- [Local deterministic and real-host tests](../local/tests/e2e/README.md)
- `bash scripts/test-container.sh` for an isolated clean container installation
- [Contribution checks](../CONTRIBUTING.md)

Model tests require explicit opt-in and consume model credits. The API suite uses a supplied API token; UI tests use a real password session with the same account. Local test credentials, authentication state, browser reports and raw logs are ignored by Git and must not be published.

## Boundaries

External knowledge retrieval/write-back and live enterprise notifications were not executed. Local adapter/HTTP fixtures, permission tests and CRUD checks cover their application boundaries. These results do not substitute for testing an operator's external deployment. No public PR or branch has been pushed.

The native review preview is independently supervised and keeps its database and account configuration. Clean container acceptance uses a separate Compose project and removes only that project's containers and volumes after testing.

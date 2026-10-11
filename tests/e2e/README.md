# E2E tests

`bash scripts/e2e.sh` runs the end-to-end suite. It needs Python 3.8+, Node.js 20+ and npm, and no network access, API key or real coding Agent.

What it does:

1. Builds the npm tarball once (`npm pack`), installs it into a temporary prefix, and uses that `loopforge` binary, which is the same path users take with `npx loopforge-cli`.
2. Installs every supported host and edition into throw-away git projects, then checks the entrypoint files, `status`, `doctor`, `update` and a clean `uninstall` (`test_e2e_install.py`).
3. Drives the installed Portable workflow end to end with [`fake_agent.py`](fake_agent.py), a deterministic stand-in for the host Agent. It follows the Skill's `resume`-driven protocol (`init`, `prepare --emit-prompt`, `assign`, `start`, `finish`, `approve`, `decide-knowledge`), but writes the stage outputs recorded in [`fixtures/scenario-slugify.json`](fixtures/scenario-slugify.json) instead of calling an LLM. The developer stage really changes [`fixtures/sample_project`](fixtures/sample_project/slugify.py), and the test stage really runs its tests. The suite covers every host, resume after a crash and after an interruption, the requirement confirmation gate, review rejection, unfilled artifacts and repeated failures (`test_e2e_portable_workflow.py`).
4. Covers the other Portable routes and modes (`test_e2e_workflow_modes.py`): small/SOLO, SOLO overflow escalating to medium, large with a knowledge stage, `manual` approval gates, and design-implementation tasks that need design context and visual evidence.
5. Runs the Classic CodeBuddy hooks from an installed project: auto-dispatch routing between roles, the fallbacks to the main Agent, and a recorded session flowing through the observability hooks into the local dashboard, which is served over HTTP (`test_e2e_classic_runtime.py`).

6. Renders the dashboard in Chromium with Playwright (`test_e2e_dashboard_browser.py`). It checks the data badge, KPIs, session table, Devflow run card and auto-dispatch health, plus the empty and load-failure states, and blocks every request outside 127.0.0.1. The test is skipped when `playwright` is not installed. CI sets `LOOPFORGE_E2E_BROWSER=1`, so there a missing browser fails the job instead, and the screenshots are uploaded as the `dashboard-screenshots` artifact.

Useful options:

```bash
LOOPFORGE_E2E_SOURCE=1 bash scripts/e2e.sh          # run the CLI from src/ and skip npm pack
bash scripts/e2e.sh -k resume                        # run only tests matching a pattern
python3 tests/e2e/fake_agent.py --help               # drive a project by hand
pip install playwright && python3 -m playwright install chromium
LOOPFORGE_E2E_BROWSER=1 bash scripts/e2e.sh -k DashboardBrowserE2E
```

When a test fails, the assertion message includes the fake Agent transcript (`.fake-agent/transcript.jsonl` in the project) that shows each action and the command that was rejected.

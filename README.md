# LoopForge

English | [简体中文](README.zh-CN.md)

**Turn Codex, Claude Code, Cursor, and CodeBuddy into a resumable AI development team.**

Plan → Build → Review → Test. LoopForge gives coding agents a delivery workflow you can confirm, inspect, and resume—not just another prompt.

![LoopForge local dashboard showing agent activity, token cost, tool usage, and workflow stages](docs/assets/agent-observability-dashboard.png)

_Dashboard example using synthetic demonstration data._

<p align="center">
  <strong>Local-first</strong> · <strong>Vendor-neutral</strong> · <strong>Resumable</strong> · <strong>Evidence-driven</strong>
</p>

## Why LoopForge

Long coding tasks fail in predictable ways: agents start before the goal is clear, drift outside the requested scope, review their own work, lose context between sessions, and stop with a diff but no evidence that it works.

LoopForge turns one request into an inspectable delivery record:

- **Confirm before coding** — clarify the goal, scope, exclusions, and acceptance criteria.
- **Separate responsibilities** — use independent roles for design, implementation, review, and testing.
- **Resume after interruption** — persist workflow state and continue from the first unfinished stage.
- **Keep the evidence** — save requirements, decisions, review findings, test results, and the final summary.
- **See how agents worked** — inspect local session activity, Token/cost trends, tools, Skills, Rules, and stage attribution.

## From one request to a verifiable result

1. **Understand the problem first**: the agent inspects the project and clarifies the goal, scope, and acceptance criteria instead of immediately writing code.
2. **Confirm the boundaries before implementation**: medium and large tasks move into design and implementation only after you confirm the goal, scope, exclusions, key decisions, and acceptance criteria.
3. **Separate delivery responsibilities**: design, implementation, review, and testing are performed in stages by agents with independent responsibilities, reducing self-review bias in a single context.
4. **Show evidence, not just a diff**: requirements, plans, changes, review findings, and test results are saved for inspection, handoff, and retrospectives.
5. **Continue after interruption**: workflow state is persisted so the next session can resume at the unfinished stage instead of starting over.

Small changes take a streamlined single-agent path. The full multi-agent workflow is reserved for medium and large tasks that benefit from it.

```text
Your request
   │
   ├─ Small change ───────> Understand → implement → verify → deliver
   │
   └─ Medium/large task ──> Clarify and confirm → design → implement
                                                   → independent review → test → deliver
                              │
                              └── State and artifacts are saved throughout
```

## Quick start

Requires Node.js 20+ and Python 3.8+.

Install the default Classic workflow in your repository. This example uses Codex; `claude`, `cursor`, and `codebuddy` are also supported:

```bash
npx -y loopforge-cli@latest install codex
```

Start the workflow from your coding agent:

| Host | Start | Resume |
|---|---|---|
| Codex | `$devflow-codex "your task"` | `$devflow-codex resume <task_slug>` |
| Claude Code | `/start-devflow "your task"` | `/resume-devflow <task_slug>` |
| Cursor | `/start-devflow "your task"` | `/resume-devflow <task_slug>` |
| CodeBuddy | `/start-devflow "your task"` | `/resume-devflow <task_slug>` |

If installation or execution fails, diagnose the environment:

```bash
npx -y loopforge-cli@latest doctor
```

After a CodeBuddy Classic run, open the local observability dashboard with one command:

```bash
npx -y loopforge-cli@latest dashboard
```

LoopForge binds the dashboard to `127.0.0.1`, keeps telemetry local, and opens it in your browser. Use `--no-open`, `--port`, or `--build-only` when scripting.

## What you get

At the end of a task, you get the code changes together with the confirmed requirement, technical design, independent review findings, test results, and delivery summary. LoopForge saves this history under `artifacts/{task_slug}/` by default for inspection, handoff, and retrospectives.

<details>
<summary>View the default artifact layout</summary>

Some directory names differ between editions.

```text
01-requirement/   Confirmed goals, scope, and acceptance criteria
02-design/        Technical design and execution plan
03-code/          Code changes and independent review report
04-e2e/           Test results
05-knowledge/     Reusable project knowledge
workflow-summary.md
```

</details>

Resume an interrupted Classic workflow with `/resume-devflow {task_slug}` on CodeBuddy, Cursor, or Claude Code, and with `$devflow-codex resume {task_slug}` on Codex. Each installed host bundle documents its status, abort, and individual-stage commands.

## See how agents work

The Classic Edition for CodeBuddy includes a local observability dashboard. It turns workflow evidence into a view of session activity, Token and cost trends, Skill/Rule usage, multi-agent dispatch, and Devflow stage attribution—useful for finding expensive runs, checking whether automation is actually being used, and reviewing how work moved between agents.

The dashboard is generated and served locally; it does not require an external telemetry service. Runtime logs can contain tool arguments and output excerpts, so LoopForge excludes them from Git by default and they should not be shared without review.

<details>
<summary>Open the local dashboard</summary>

After running at least one CodeBuddy Classic session from the project root:

```bash
npx -y loopforge-cli@latest dashboard
```

See the [observability quickstart](.codebuddy/skills/agent-observability/references/quickstart.md) for data sources, custom pricing, and troubleshooting.

</details>

## When LoopForge is useful

LoopForge is especially useful when:

- the request is still ambiguous and the goal or boundaries need clarification;
- a change crosses multiple modules and should be designed before implementation;
- implementation, review, and testing should be performed by separate roles;
- work may span multiple sessions and needs resumable state;
- the team wants an inspectable delivery record for handoff or retrospectives.

For one-off code completion or a tiny, fully specified edit, using a coding agent directly is often enough. Simple tasks that do enter LoopForge use its streamlined path.

## Use the Portable Edition

The quick start above installs the default Classic Edition, which is the right choice for most users. If you only want the portable Agent Skills workflow, choose the command for your coding agent below. To prevent one coding agent from loading two workflows and mixing their state or artifacts, LoopForge enables only one edition per coding agent in each project. To switch editions, run the new install command with `--force`.

### CodeBuddy

```bash
npx -y loopforge-cli@latest skills install codebuddy
```

Then start a workflow:

```text
/devflow "add status filtering to the order list and cover the API with tests"
```

### Codex

```bash
npx -y loopforge-cli@latest skills install codex
```

Then start a workflow:

```text
$devflow "add status filtering to the order list and cover the API with tests"
```

### Cursor

```bash
npx -y loopforge-cli@latest skills install cursor
```

Then start a workflow:

```text
/devflow "add status filtering to the order list and cover the API with tests"
```

### Claude Code

```bash
npx -y loopforge-cli@latest skills install claude
```

Then start a workflow:

```text
/devflow "add status filtering to the order list and cover the API with tests"
```

### Pi

```bash
npx -y loopforge-cli@latest skills install pi
pi install npm:pi-subagents
```

Then start a workflow:

```text
/skill:devflow "add status filtering to the order list and cover the API with tests"
```

See [`EDITIONS.md`](EDITIONS.md) for the complete boundaries and directory layout.

## Update and uninstall

For ongoing use, install the CLI globally:

```bash
npm install --global loopforge-cli
```

Maintenance commands for the default Classic Edition:

```bash
# CodeBuddy
loopforge update codebuddy
loopforge status codebuddy
loopforge uninstall codebuddy

# Codex
loopforge update codex
loopforge status codex
loopforge uninstall codex

# Cursor
loopforge update cursor
loopforge status cursor
loopforge uninstall cursor

# Claude Code
loopforge update claude
loopforge status claude
loopforge uninstall claude
```

Maintenance commands for the Portable Edition:

```bash
# CodeBuddy
loopforge skills update codebuddy
loopforge skills status codebuddy
loopforge skills uninstall codebuddy

# Codex
loopforge skills update codex
loopforge skills status codex
loopforge skills uninstall codex

# Cursor
loopforge skills update cursor
loopforge skills status cursor
loopforge skills uninstall cursor

# Claude Code
loopforge skills update claude
loopforge skills status claude
loopforge skills uninstall claude

# Pi
loopforge skills update pi
loopforge skills status pi
loopforge skills uninstall pi
```

Installation state is stored in `.devflow/install-state.json`. An update stops instead of overwriting user-modified or conflicting files. Use `--force` only when you intend to replace conflicts or switch editions, preferably after inspecting `loopforge plan`.

If you have cloned this repository, install the LoopForge CLI from the current source tree by running this in the repository root:

```bash
pipx install .
```

## Optional tools

`tools/tcmcp` is a Tencent Cloud read-only MCP Initializr (Redis, CDB, TDSQL-C, TKE, CLS, COS). It is not part of `loopforge install` or the npm CLI package. From a clone:

```bash
cd tools/tcmcp
```

Then follow [`tools/tcmcp/README.md`](tools/tcmcp/README.md). Cloud API keys are not written into generated zips or the container image; they stay on the machine that lists resources or installs the MCP client. LoopForge `vX.Y.Z` releases publish `ghcr.io/tencent/loopforge-tcmcp:<version>`.

## Permissions and security

Some Classic host configurations enable automatic execution or high-permission agent modes. Use LoopForge only in trusted projects. Before installation, inspect planned files with `loopforge plan codebuddy`, `loopforge plan codex`, `loopforge plan cursor`, or `loopforge plan claude`. Do not put credentials, private endpoints, or production operations configuration in workflow templates.

See [`SECURITY.md`](SECURITY.md) and report vulnerabilities privately through GitHub Security Advisories.

## Community

Scan the WeCom QR code below to join the LoopForge community group.

<img src="docs/community-wecom-qr.png" alt="LoopForge WeCom community group QR code" width="220" />

## Contributing

Issues and pull requests are welcome. See the public [`ROADMAP.md`](ROADMAP.md), read [`CONTRIBUTING.md`](CONTRIBUTING.md), and review the repository `AGENTS.md` before changing workflow behavior. See [`CHANGELOG.md`](CHANGELOG.md) for release history.

## License

MIT. See [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) for third-party notices.

# LoopForge

English | [简体中文](README.zh-CN.md)

**Make coding agents deliver through a workflow you can confirm, review, and resume—not just produce code.**

Giving a coding agent a request is easy. The hard part is making sure it understands the goal before coding, stays within scope during implementation, and completes independent review and testing before it stops. Longer tasks also lose context, get interrupted, and leave behind a diff with little explanation of how it was produced.

LoopForge turns those concerns into a complete software delivery workflow. You describe the problem to solve; LoopForge chooses a path based on task complexity and guides agents through requirement clarification, solution design, implementation, independent review, and testing while preserving the important decisions and verification results.

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

The commands below install the default Classic Edition. Choose this edition if you are unsure.

### CodeBuddy

```bash
npx -y loopforge-cli@latest install codebuddy
npx -y loopforge-cli@latest status codebuddy
```

Then start a workflow:

```text
/start-devflow "add status filtering to the order list and cover the API with tests"
```

### Codex

```bash
npx -y loopforge-cli@latest install codex
npx -y loopforge-cli@latest status codex
```

Then start a workflow:

```text
$devflow-codex "add status filtering to the order list and cover the API with tests"
```

### Cursor

```bash
npx -y loopforge-cli@latest install cursor
npx -y loopforge-cli@latest status cursor
```

Then start a workflow:

```text
/start-devflow "add status filtering to the order list and cover the API with tests"
```

### Claude Code

```bash
npx -y loopforge-cli@latest install claude
npx -y loopforge-cli@latest status claude
```

Then start a workflow:

```text
/start-devflow "add status filtering to the order list and cover the API with tests"
```

If installation or execution fails, diagnose the environment:

```bash
npx -y loopforge-cli@latest doctor
```

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

## Contributing

Issues and pull requests are welcome. Read [`CONTRIBUTING.md`](CONTRIBUTING.md) and the repository `AGENTS.md` before changing workflow behavior. See [`CHANGELOG.md`](CHANGELOG.md) for release history.

## License

MIT. See [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) for third-party notices.

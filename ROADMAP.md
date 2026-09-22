# LoopForge Roadmap

LoopForge is building a vendor-neutral delivery layer for coding agents: one
workflow for planning, implementation, independent review, testing, evidence,
and recovery across long-running software tasks.

The roadmap is outcome-oriented rather than a promise of dates. Priorities may
change as users test the workflows in real repositories.

## Now: make every run inspectable

- [x] Resumable Classic workflows for CodeBuddy, Codex, Cursor, and Claude Code.
- [x] Portable Agent Skills for supported hosts.
- [x] Persisted requirements, design, review, test, and delivery artifacts.
- [x] Local observability for CodeBuddy sessions, tools, Tokens, cost, and workflow stages.
- [x] One-command local dashboard with `loopforge dashboard`.
- [ ] Validate and enable end-to-end telemetry collection for Claude Code and Cursor.
- [ ] More end-to-end examples based on public repositories and real issues.
- [ ] Sanitized Markdown reports that can be attached directly to pull requests.

## Next: resume across agents

- [ ] Define a host-neutral workflow-state compatibility contract.
- [ ] Start a task in one coding agent and resume it in another.
- [ ] Record which host and model completed each stage.
- [ ] Detect capability differences and explain safe fallback behavior.
- [ ] Add Gemini CLI and OpenCode adapters where their host contracts allow it.

The target experience is intentionally concrete:

```text
Claude plans → Codex implements → another agent reviews → tests provide evidence
```

## Later: connect delivery to GitHub

- [ ] Start a workflow from a labeled GitHub Issue.
- [ ] Create a Draft PR with scope, decisions, review findings, and test results.
- [ ] Keep human approval before merge or other high-impact actions.
- [ ] Compare direct prompting with structured delivery on reproducible tasks.
- [ ] Audit Agent Skills, MCP servers, permissions, and possible secret exposure.

## Non-goals

- Replacing the coding agents users already prefer.
- Hiding failures behind a single opaque quality score.
- Automatically merging unreviewed changes.
- Requiring a hosted telemetry service for local workflow inspection.
- Splitting every Skill or adapter into a separate repository.

## Help shape the roadmap

Feature requests and implementation proposals are welcome in
[GitHub Issues](https://github.com/Tencent/LoopForge/issues). For behavior
changes, describe the target host, task size, expected artifact or state
transition, and how the result can be verified.

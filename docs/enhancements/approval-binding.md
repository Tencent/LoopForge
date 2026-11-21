# Approval binding and engine-enforced verification gates

This change upgrades two conventions into engine guarantees:

1. A human approval can be bound to the exact artifact the operator reviewed, so an approval aimed at a stale run state is rejected instead of silently approving something else.
2. A verification node cannot be bypassed with `POST /api/runs/{id}/pass`; an explicit, audited override is required. `POST /api/runs/{id}/approve` on such a node (parked by a pause or the rework-budget human gate) remains an explicit human decision: it still writes `gate.approved` and the `decision` artifact, but it is not the `/pass` bypass and is out of P4's scope.

It follows the enhancement plan in `plan.md` (P2–P4). P5 (sidecar integration with AegisOS) is not part of this change.

## Baseline facts (P1, read from source)

- A run freezes its workflow at start: `internal/engine/run_start.go:156` stores `WorkflowSnapshot`, and a later publish does not change it. `RefreshRunSOP` (`internal/engine/run_ops.go`) is the only explicit snapshot update.
- Every artifact, including engine-written `decision.md`, gets a SHA-256 in `newArtifact` (`internal/engine/artifacts.go`), so binding needs no new table.
- Before this change, `/pass` accepted any failed script/command node on a failed run (or a script parked by a pause) and had no type-level restriction, so a verification script could be bypassed.

The live smoke test (`./start.sh dev`, first admin, minimal SOP run) was not run in the development session that produced this change: the dev image build failed inside Docker at `npm --prefix chatbot ci` with a network error. That is an environment failure, not a regression from this change.

## P2 — binding an approval to an artifact

`engine.NodeExpectation` and the HTTP `expected` object gained:

- `artifactName`: logical artifact name, e.g. `verify`;
- `artifactSha256`: expected content hash.

`checkNodeExpectation` compares against the **newest** artifact with that name (newest attempt, then newest creation time). Approving against an older attempt is exactly the stale decision this check exists to reject.

On mismatch the engine returns an `apperr.Conflict` (HTTP `409`), appends `gate.approval_rejected` with the expected and actual values, and leaves the node in `waiting_approval`. Status/attempt/rework mismatches use the same `409` now, since they mean the same thing: the card the operator acted on is stale.

```json
{
  "nodeId": "approval",
  "expected": {
    "status": "waiting_approval",
    "attempt": 2,
    "artifactName": "verify",
    "artifactSha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
  }
}
```

The lookup is run-global by artifact name: if two nodes publish the same name, the newest creation wins for both binding and `requires`. Artifact names are only unique within a node by validation, so a workflow that needs two distinct `verify` artifacts must give them distinct names.

Omitting `expected` keeps the old behavior.

## P3 — node preconditions with `requires`

`WorkflowNode.requires` accepts:

- `artifact:<name>@<sha256>`: the run must carry that artifact with that hash;
- `verdict:<PASS|FAIL>@<diffHash>`: the newest verdict artifact (named `verdict`, or whose path basename is `verify.json`) must be JSON with `verdict` and `diffHash` matching (case-insensitive).

Malformed values are rejected at workflow validation, which runs when a run starts (and on `RefreshRunSOP`); the save path does not call it, so a typo is reported at start. At execution the scheduler checks the preconditions before spawning the node. An unmet precondition fails the node with the expected and actual values; the failure bypasses `onFailure`, `MaxReworks` and the retry policy because a deterministic precondition will not change on retry. If a sibling node is still in flight, the check is deferred until it settles — a parallel producer must still be wired as an upstream edge, otherwise the consumer can be failed before that producer becomes ready.

```json
{
  "id": "ship",
  "type": "agent",
  "agentId": "releaser",
  "requires": ["verdict:PASS@c0ffee"]
}
```

## P4 — closing the `/pass` bypass for verification nodes

`WorkflowNode.verificationStep: true` marks a verification step. The JSON name avoids `verification` because that is the removed legacy gate-array field the API strips on save.

- `POST /api/runs/{id}/pass` without `override` returns `409`, leaves the node failed, and appends `node.pass_rejected`.
- `{"nodeId":"...","override":true}` passes the node and appends `node.passed_override` naming the operator (the signed-in user; anonymous requests are recorded as `未记录操作者`).

Non-verification script nodes keep the previous behavior, so the environment-problem workflow that `/pass` exists for still works.

## Tests

- `internal/engine/gate_binding_test.go`: matching hash approves, mismatched/missing artifact rejects and audits, half-filled binding returns an error, the newest artifact by creation time wins over a higher attempt number on an older artifact, omitted `expected` unchanged.
- `internal/engine/node_requires_test.go`: artifact hash mismatch stops the consumer with expected+actual, matching hash runs it, verdict mismatch names both, malformed verdict JSON names the artifact, malformed `requires` fails validation.
- `internal/engine/pass_override_test.go`: verification node rejects `/pass` and audits — on a failed run and on a paused script — override passes and records the actor; plain script node unchanged.
- `internal/httpapi/gate_binding_http_test.go`: HTTP `409` for a stale approval and for `/pass` without override, `200` with override.

Run with `GOFLAGS=-buildvcs=false go test ./internal/engine ./internal/httpapi` when the checkout sits under a directory tree that also contains an `.svn` directory (Go's VCS stamping refuses the nested build otherwise).

## P5 — not implemented

The sidecar interface (verify / critique / delivery-check) and its SOP integration are the next step. The deployment shape (same-host process, MCP, or container) must be confirmed before writing that adapter.

# Release review

Review resumed on 2026-10-16 against recorded upstream baseline `3a8cb2420904e3ee8ab1b39c48f54b372a9e3e04`. See the [verification record](validation.md#fresh-installations-and-resumed-acceptance--2026-10-16) and [acceptance matrix](testing/acceptance-matrix.md) for current results, corrected failure history and untested external-service boundaries. Historical audit counts below describe the earlier review, not the updated export.

## Conclusion

Suitable for submission as an initial public, self-hosted source release for trusted teams. The repository owner requested the final review and authorized opening a public PR on 2026-10-16. This is a source-release readiness conclusion, not certification of every provider, operating system or production topology; merging and publishing release artifacts remain separate steps.

The supported access model is explicit: signed-in users can read project data across the instance. Project writes require an administrator, owner or permitted member; viewer membership does not grant agent execution rights. The default installer binds to localhost. Disable self-registration (`AGENTFLOW_ALLOW_SIGNUP=false`) before allowing access from outside the trusted team. The default container setup controls the host Docker daemon and should only be operated by trusted administrators. See [deployment boundaries](deployment.md#accounts).

## Final source and deployment review

- Independently checked all **1,718 recorded source blob hashes** against the recorded master. The final candidate has 1,466 unchanged upstream files, 178 reviewed adaptations and 74 recorded exclusions, plus 58 audited public additions in the platform export scope. Private deployment files outside the export scope remain excluded. No private Git history is imported.
- Latest upstream features include durable group-to-SOP handoff and isolated script views. Their source and tests are included. MySQL concurrent claims/lease recovery and Docker script views (including non-root identity) were explicitly exercised.
- The preview had still been running an older executable after the source sync. Rebuilt the service and runner/gate helpers, restarted the preview, and compared its key JavaScript assets byte-for-byte with the current source. The handoff acceptance case now exercises the current endpoint behavior.
- Fixed a remaining viewer permission gap in group creation, role dispatch, SOP control and handoff retry, and blocked run-bound assistant execution for read-only members. Tests cover denial for viewers/non-members and continued access for owners; viewer reads remain available. This generic permission fix should be ported back to internal upstream.
- Updated the source audit record and the checker: `--source` now verifies the actual source blobs as well as the remote commit label. No unrelated source hashes were reset.

## Publication checks

| Area | Evidence |
| --- | --- |
| Source scope | Source manifest/override checks and public-content checks |
| Credentials | Gitleaks and exact checks for the supplied credentials in indexed blobs and inspected reports; ignored local secrets are not exported |
| License packaging | Root MIT license, editor notices, complete Go dependency notice bundle and generated Local distribution notices |
| Application | Full Go/vet checks, real HTTP/API suite, browser regressions and clean container acceptance; detailed counts in the verification record |
| New user | Browser administrator creation, teammate registration, onboarding and missing-key guidance; no-key script/approval demo and repository connection |
| GitHub | Real issue import, preservation of local clarification, PR discovery/merge between disposable branches and closed-issue synchronization in the authorized test repository |
| Documentation | Bilingual README, deployment/access boundaries, Local positioning, contribution/security policies and upstream sync procedure |
| Repository presentation | Description, topics and issue labels prepared in `.github/repository-metadata.json`; apply only after publication approval |

## Limits of the evidence

- External knowledge retrieval/write-back, enterprise notifications and paid Codex/Claude services lack live credentials. Their protocol/adapter tests do not count as live service verification.
- Real model results use the supplied CodeBuddy environment. The four-role Python delivery case checks actual tests, artifacts, approval and code hashes; it does not guarantee success for arbitrary tasks.
- The earlier 47-minute run used the frontend SOP template on a Python test repository and required explicit human clarification/acceptance. It demonstrates orchestration and recovery, not verification of every frontend framework or fully autonomous delivery. No PR was created by that run.
- Linux container/native acceptance is the primary deployed environment checked here. Cross-compilation of macOS/arm64 helpers is not runtime testing on every supported host.
- GitHub-hosted CI must validate the submitted PR. A package, image, tag or GitHub Release is not published by this review.

## Final submission review — 2026-10-16

The final review fixed the runner cancellation issue also identified in the review of PR #46: cancellation now disables the completed context channel, a single goroutine drains the output streams and owns `cmd.Wait`, and early control failures reap the command before returning. Focused process/managed-CLI regressions passed five repetitions; cancellation and early-failure cases also passed three repetitions with the race detector.

An additional full-suite run exposed a preview-test race: the preview becomes ready before its asynchronous workspace cleanup finishes. The test now waits for both the persisted cleanup marker and directory removal before closing its store; it passed 40 repetitions. The first full suite and the corrected final suite are recorded separately in [validation.md](validation.md#final-pr-submission-checks--2026-10-16).

### Existing pull requests

- [PR #46](https://github.com/Tencent/LoopForge/pull/46) is an earlier platform import targeting `main`. This submission replaces that integration direction with the audited master export, public adapters, MIT packaging and the existing workflow under `local/`. It can be closed as superseded once the replacement PR is available; closing it does not imply its commits were merged.
- [PR #47](https://github.com/Tencent/LoopForge/pull/47) is stacked on #46 and adds a separate workspace redesign and room-crew implementation. Only its language/theme mechanism is explicitly reused. The current group/SOP orchestration does not establish parity with its room mentions, quoted replies, crew planning and crew-to-draft-SOP features. Keep it open for a feature disposition/porting decision; it cannot be closed on the claim that this submission fully includes it. If pursued, rebuild the intended changes on the replacement platform rather than merging its old base wholesale. Its original platform E2E check also failed.

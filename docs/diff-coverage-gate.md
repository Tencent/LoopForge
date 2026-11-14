# Diff-coverage gates

A `command` gate answers “did these commands exit 0?”. It cannot answer “does the
code this change wrote have tests?”, because the project's existing test suite
usually stays green while new branches and error paths go untested.

A `diff_coverage` gate adds that second question without asking for whole-repo
coverage: it judges **only the executable lines the change added or modified**,
relative to the merge-base with the repository's base branch. Legacy code with
low coverage never blocks a run.

## Configuration

```json
{
  "id": "coverage",
  "name": "改动行覆盖率",
  "mode": "diff_coverage",
  "required": true,
  "onFailure": "loop",
  "executionScope": "repositories",
  "commands": ["go test -coverprofile=coverage.out ./..."],
  "coverage": {
    "reportPath": "coverage.out",
    "reportFormat": "go_coverprofile",
    "thresholdPct": 80,
    "minChangedLines": 5,
    "include": [],
    "exclude": []
  }
}
```

| Field | Meaning |
| --- | --- |
| `commands` | Produce the coverage report. A non-zero exit fails the gate as a normal command gate would; the coverage judgement then never runs, so a stale report is never parsed. |
| `coverage.reportPath` | Relative to the repository root, even when the commands `cd` into a subdirectory (for example `cd web && npm test` writing `web/coverage/lcov.info`). |
| `coverage.reportFormat` | `go_coverprofile` (`go test -coverprofile`), `lcov` (`lcov.info`, `--coverage.reporter=lcov`), `cobertura` (`coverage.xml`, `pytest --cov-report=xml`, JaCoCo Cobertura output). |
| `coverage.thresholdPct` | Minimum coverage of changed executable lines, 0–100. Default 80. |
| `coverage.minChangedLines` | Below this many changed executable lines the gate is “not applicable” and passes without spending a self-correction round. Default 5. |
| `coverage.baseBranch` | Overrides the repository target's base branch used for the merge-base. |
| `coverage.include` / `exclude` | Path globs (`**` supported). Exclude wins; the built-in defaults (`*_test.go`, `*.test.*`, `*.spec.*`, `__tests__/`, `test_*.py`, `*_test.py`, `testdata/`, `*_gen.go`, `*.pb.go`, `zz_generated*`, `vendor/`, `node_modules/`, build output) cannot be re-included, so tests the agent adds never count as uncovered code. |

`executionScope` must be `repositories`: the gate diffs a checkout, and on a
multi-repository run each repository is judged and reported separately.

## How a verdict is reached

1. The commands run in the repository checkout (sandbox or execution machine,
   same as other gates) with `AGENTFLOW_BASE_BRANCH` exported.
2. After the commands finish, the gate returns to the repository root, so
   every path below is repository-relative no matter where the commands
   `cd`-ed. The platform reads `git diff -U0 $(git merge-base <base> HEAD)` for the
   changed line ranges, plus untracked files the node created (diffed against
   `/dev/null`, honouring `.gitignore`; the index is not touched). Deleted
   files and pure deletions contribute nothing; renames keep the new path. The
   merge-base is taken against `origin/<base>` first, then the local branch;
   if neither exists the gate fails with “找不到基线分支 <base>” instead of a
   bare exit status. Git runs with `core.quotepath=off`, so non-ASCII file
   names are judged like any other.
3. It parses the report into *file → executable line → hit count*.
4. Changed lines that the report does not mark executable (comments, blanks,
   declarations) are ignored. Changed lines in files the report never mentions
   count as uncovered — that is the “tests never ran for this file” case — but
   only when the file is written in the report's language: `*.go` for
   `go_coverprofile`, and for `lcov`/`cobertura` the extensions of the files
   the report lists. `CHANGELOG.md`, `*.yaml`, `go.mod` and similar files are
   therefore never judged.
5. If the covered fraction is below the threshold, the node fails with the
   gate's `onFailure` policy and the agent receives the file names and line
   ranges, for example:

   ```
   改动行覆盖率 45%，低于阈值 80%（改动可执行行 11，已被测试覆盖 5）。
   请为以下未覆盖的改动行补充测试： calc.go L14-L15,L19-L22；
   不要通过删除代码、扩大 exclude 或给这些行加忽略标记来规避。
   ```

Only the diff's file and hunk headers travel back from the executor, and the
report is reduced to the changed files: `go_coverprofile` keeps their profile
blocks, `lcov` keeps their `SF:`…`end_of_record` records plus one minimal record
per other file extension (so the report's languages are still known). A CLI
Runner returns at most the last 32 KiB of a command's output; if the payload
still does not fit, the gate fails with a message saying the output was
truncated rather than misreading a partial report.

**Limitation:** `cobertura` XML is returned whole. On a CLI Runner a cobertura
report larger than about 32 KiB therefore fails the gate as truncated; generate
the report only for the packages the change touches, or use `lcov` /
`go_coverprofile` there.

An empty diff (nothing changed since the base) and an unparsable or missing
report are treated differently on purpose: empty diff passes as “not
applicable”, while a missing or malformed report fails the gate as an
infrastructure problem — never a silent pass.

## Reporting

`gate.coverage.judged` telemetry events carry `coveragePct`, `thresholdPct`,
`changedLines`, `coveredLines` and `applicable` per gate and iteration, which is
enough to see how often a role's first attempt missed the bar and whether the
feedback led to tests on the next round.

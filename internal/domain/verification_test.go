package domain

import (
	"strings"
	"testing"
)

func TestValidateVerificationGateRequiresScopeForCommands(t *testing.T) {
	err := ValidateVerificationGate(VerificationGate{ID: "plan", Commands: []string{`test -f fix-plan.md`}})
	if err == nil || !strings.Contains(err.Error(), "executionScope") {
		t.Fatalf("empty command gate must be rejected, got %v", err)
	}
	if err := ValidateVerificationGate(VerificationGate{ID: "plan", Commands: []string{`test -f fix-plan.md`}, ExecutionScope: ExecutionScopeRun}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateVerificationGate(VerificationGate{ID: "human", Mode: "manual", Required: true}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateVerificationGate(VerificationGate{ID: "verdict", Mode: "submit", Required: true}); err == nil {
		t.Fatal("submit is an engine-owned terminal protocol, not a configurable gate")
	}
}

func TestSplitVerificationGatesSeparatesGitFromDeliverables(t *testing.T) {
	gates := SplitVerificationGates([]VerificationGate{{
		ID: "fix-gate", Name: "修复证据门禁", Mode: "command", Required: true, OnFailure: "loop",
		Commands: []string{
			`test "$(wc -l < fix-report.md)" -ge 10`,
			`grep -Eqi 'build|构建' fix-report.md`,
			`git diff --quiet -- . ':!fix-report.md'`,
			`test "$(git rev-parse HEAD)" != "$(cat .git/agentflow-baseline)"`,
			`if test -f .agentflow/inputs/mr/mr-body.md || test -f .agentflow/inputs/checkers/checkers-report.md; then git push -u origin HEAD && grep -Eqi "PUSHED_SHA=$(git rev-parse HEAD)" fix-report.md; fi`,
		},
	}})
	if len(gates) != 2 {
		t.Fatalf("gates = %#v", gates)
	}
	if gates[0].ExecutionScope != ExecutionScopeRun || strings.Join(gates[0].Commands, "\n") != "test \"$(wc -l < fix-report.md)\" -ge 10\ngrep -Eqi 'build|构建' fix-report.md" {
		t.Fatalf("run gate = %#v", gates[0])
	}
	if gates[1].ID != "fix-gate-git" || gates[1].Name != "修复证据门禁 · Git 检查" || gates[1].ExecutionScope != ExecutionScopeRepositories {
		t.Fatalf("git gate = %#v", gates[1])
	}
	joined := strings.Join(gates[1].Commands, "\n")
	if !strings.Contains(joined, "git diff --quiet") || !strings.Contains(joined, ".git/agentflow-baseline") {
		t.Fatalf("git commands = %s", joined)
	}
	if !strings.Contains(joined, `$AGENTFLOW_WORKSPACE/.agentflow/inputs/mr/mr-body.md`) {
		t.Fatalf("run-root inputs must keep using AGENTFLOW_WORKSPACE: %s", joined)
	}
	if !strings.Contains(joined, `$AGENTFLOW_WORKSPACE/fix-report.md`) {
		t.Fatalf("run-root report must keep using AGENTFLOW_WORKSPACE: %s", joined)
	}
}

func TestSplitVerificationGatesLeavesExplicitScopeAlone(t *testing.T) {
	in := []VerificationGate{{
		ID: "php-lint-gate", ExecutionScope: ExecutionScopeRepositories,
		Commands: []string{`files=$(git diff --name-only); php -l $files`},
	}}
	got := SplitVerificationGates(in)
	if len(got) != 1 || got[0].ID != "php-lint-gate" || got[0].ExecutionScope != ExecutionScopeRepositories {
		t.Fatalf("explicit gate was rewritten: %#v", got)
	}
}

func TestCommandNeedsRepository(t *testing.T) {
	if CommandNeedsRepository(`test "$(wc -l < fix-plan.md)" -ge 15`) {
		t.Fatal("deliverable check must stay at run scope")
	}
	if !CommandNeedsRepository(`git diff --quiet -- . ':!fix-plan.md'`) {
		t.Fatal("git prefix must be repository-scoped")
	}
	if !CommandNeedsRepository(`test "$(git rev-parse HEAD)" != "$(cat .git/agentflow-baseline)"`) {
		t.Fatal("git metadata check must be repository-scoped")
	}
	if !CommandNeedsRepository(`! git ls-files --others --exclude-standard | grep -Eq '_e2e_test\.go$'`) {
		t.Fatal("negated git commands must be repository-scoped")
	}
}

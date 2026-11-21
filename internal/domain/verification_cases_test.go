package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidateVerificationGateCases(t *testing.T) {
	tests := []struct {
		name    string
		gate    VerificationGate
		wantErr string
	}{
		{name: "empty gate", gate: VerificationGate{}},
		{name: "manual gate without scope", gate: VerificationGate{ID: "m", Mode: " Manual "}},
		{name: "manual gate ignores commands", gate: VerificationGate{ID: "m", Mode: "manual", Commands: []string{"git status"}}},
		{name: "command mode without commands", gate: VerificationGate{ID: "c", Mode: "command"}},
		{name: "blank commands need no scope", gate: VerificationGate{ID: "c", Commands: []string{"", "  \t"}}},
		{name: "repositories scope", gate: VerificationGate{ID: "c", Commands: []string{"git diff --quiet"}, ExecutionScope: " repositories "}},
		{name: "uppercase command mode", gate: VerificationGate{ID: "c", Mode: "COMMAND", Commands: []string{"true"}, ExecutionScope: ExecutionScopeRun}},
		{name: "invalid mode", gate: VerificationGate{ID: "g1", Mode: "script"}, wantErr: `verification gate g1 has invalid mode "script"`},
		{name: "invalid scope", gate: VerificationGate{ID: "g2", ExecutionScope: "workspace"}, wantErr: `verification gate g2 has invalid executionScope "workspace"`},
		{name: "invalid scope on manual gate", gate: VerificationGate{ID: "g3", Mode: "manual", ExecutionScope: "all"}, wantErr: "invalid executionScope"},
		{name: "missing scope names gate by id", gate: VerificationGate{ID: " g4 ", Name: "门禁", Commands: []string{"true"}}, wantErr: "verification gate g4 requires executionScope"},
		{name: "missing scope falls back to name", gate: VerificationGate{Name: " 构建检查 ", Commands: []string{"true"}}, wantErr: "verification gate 构建检查 requires executionScope"},
		{name: "missing scope falls back to unnamed", gate: VerificationGate{Commands: []string{"true"}}, wantErr: "verification gate unnamed requires executionScope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateVerificationGate(tt.gate)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateVerificationGatesStopsAtFirstError(t *testing.T) {
	if err := ValidateVerificationGates(nil); err != nil {
		t.Fatalf("nil gates: %v", err)
	}
	ok := VerificationGate{ID: "ok", Commands: []string{"true"}, ExecutionScope: ExecutionScopeRun}
	if err := ValidateVerificationGates([]VerificationGate{ok, {ID: "human", Mode: "manual"}}); err != nil {
		t.Fatalf("valid gates: %v", err)
	}
	err := ValidateVerificationGates([]VerificationGate{
		ok,
		{ID: "first", Mode: "bogus"},
		{ID: "second", ExecutionScope: "bogus"},
	})
	if err == nil || !strings.Contains(err.Error(), "first") || strings.Contains(err.Error(), "second") {
		t.Fatalf("error = %v, want the first invalid gate", err)
	}
}

func TestCommandNeedsRepositoryCases(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"", false},
		{"   ", false},
		{"set -e", false},
		{"set -e\n  ", false},
		{"set -e\ngit status", true},
		{"git\tstatus", true},
		{"cat .git/HEAD", true},
		{`echo $(git rev-parse HEAD)`, true},
		{"gitleaks detect", false},
		{"test -f report.md", false},
	}
	for _, tt := range tests {
		if got := CommandNeedsRepository(tt.command); got != tt.want {
			t.Errorf("CommandNeedsRepository(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}

func TestRewriteRepositoryWorkspaceRefs(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{
			name:    "agentflow dir is anchored at the run root",
			command: "test -f .agentflow/inputs/a.md",
			want:    "test -f $AGENTFLOW_WORKSPACE/.agentflow/inputs/a.md",
		},
		{
			name:    "already anchored agentflow dir is kept",
			command: "test -f $AGENTFLOW_WORKSPACE/.agentflow/a.md && test -f .agentflow/b.md",
			want:    "test -f $AGENTFLOW_WORKSPACE/.agentflow/a.md && test -f .agentflow/b.md",
		},
		{
			name:    "braced anchor is kept",
			command: "test -f ${AGENTFLOW_WORKSPACE}/.agentflow/a.md",
			want:    "test -f ${AGENTFLOW_WORKSPACE}/.agentflow/a.md",
		},
		{
			name:    "reports are rewritten only in git commands",
			command: "test -s change-report.md",
			want:    "test -s change-report.md",
		},
		{
			name:    "every run-root report is anchored",
			command: "git log -1 && grep x change-report.md fix-report.md e2e-cases.md",
			want:    "git log -1 && grep x $AGENTFLOW_WORKSPACE/change-report.md $AGENTFLOW_WORKSPACE/fix-report.md $AGENTFLOW_WORKSPACE/e2e-cases.md",
		},
		{
			name:    "pathspec exclusions are not rewritten",
			command: "git diff --quiet -- . ':!fix-report.md' ':(exclude)e2e-cases.md'",
			want:    "git diff --quiet -- . ':!fix-report.md' ':(exclude)e2e-cases.md'",
		},
		{
			name:    "already anchored report is kept",
			command: "git status && test -s $AGENTFLOW_WORKSPACE/fix-report.md",
			want:    "git status && test -s $AGENTFLOW_WORKSPACE/fix-report.md",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rewriteRepositoryWorkspaceRefs(tt.command); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUniqueGateID(t *testing.T) {
	tests := []struct {
		name string
		base string
		used map[string]bool
		want string
	}{
		{name: "unused id", base: " plan ", used: map[string]bool{}, want: "plan"},
		{name: "blank base", base: "  ", used: map[string]bool{}, want: "gate-git"},
		{name: "blank base taken", base: "", used: map[string]bool{"gate-git": true}, want: "gate-git-2"},
		{name: "skips taken suffixes", base: "a", used: map[string]bool{"a": true, "a-2": true, "a-3": true}, want: "a-4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := uniqueGateID(tt.base, tt.used); got != tt.want {
				t.Fatalf("uniqueGateID(%q) = %q, want %q", tt.base, got, tt.want)
			}
		})
	}
}

func TestSplitVerificationGatesCases(t *testing.T) {
	tests := []struct {
		name string
		in   []VerificationGate
		want []VerificationGate
	}{
		{name: "nil input", in: nil, want: []VerificationGate{}},
		{
			name: "manual gate without commands is kept",
			in:   []VerificationGate{{ID: "human", Mode: "manual"}},
			want: []VerificationGate{{ID: "human", Mode: "manual"}},
		},
		{
			name: "blank commands are kept untouched",
			in:   []VerificationGate{{ID: "blank", Commands: []string{"", " "}}},
			want: []VerificationGate{{ID: "blank", Commands: []string{"", " "}}},
		},
		{
			name: "deliverable-only gate becomes run scoped",
			in:   []VerificationGate{{ID: "doc", Commands: []string{"", "test -f plan.md"}}},
			want: []VerificationGate{{ID: "doc", Commands: []string{"test -f plan.md"}, ExecutionScope: ExecutionScopeRun}},
		},
		{
			name: "git-only gate becomes repositories scoped in place",
			in:   []VerificationGate{{ID: "diff", Commands: []string{"git diff --quiet -- . ':!fix-report.md'", "git status && test -s fix-report.md"}}},
			want: []VerificationGate{{
				ID:             "diff",
				Commands:       []string{"git diff --quiet -- . ':!fix-report.md'", "git status && test -s $AGENTFLOW_WORKSPACE/fix-report.md"},
				ExecutionScope: ExecutionScopeRepositories,
			}},
		},
		{
			name: "mixed gate id avoids ids used by other gates",
			in: []VerificationGate{
				{ID: "mixed", Commands: []string{"test -f a.md", "git status"}},
				{ID: "mixed-git", Commands: []string{"true"}, ExecutionScope: ExecutionScopeRun},
			},
			want: []VerificationGate{
				{ID: "mixed", Commands: []string{"test -f a.md"}, ExecutionScope: ExecutionScopeRun},
				{ID: "mixed-git-2", Name: "Git 检查", Commands: []string{"git status"}, ExecutionScope: ExecutionScopeRepositories},
				{ID: "mixed-git", Commands: []string{"true"}, ExecutionScope: ExecutionScopeRun},
			},
		},
		{
			name: "two unnamed mixed gates get distinct git ids",
			in: []VerificationGate{
				{Commands: []string{"test -f a.md", "git status"}},
				{Commands: []string{"test -f b.md", "git log -1"}},
			},
			want: []VerificationGate{
				{Commands: []string{"test -f a.md"}, ExecutionScope: ExecutionScopeRun},
				{ID: "gate-git", Name: "Git 检查", Commands: []string{"git status"}, ExecutionScope: ExecutionScopeRepositories},
				{Commands: []string{"test -f b.md"}, ExecutionScope: ExecutionScopeRun},
				{ID: "gate-git-2", Name: "Git 检查", Commands: []string{"git log -1"}, ExecutionScope: ExecutionScopeRepositories},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitVerificationGates(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got  %#v\nwant %#v", got, tt.want)
			}
			if err := ValidateVerificationGates(got); err != nil {
				t.Fatalf("split gates must validate: %v", err)
			}
		})
	}
}

package domain

import (
	"reflect"
	"testing"
	"time"
)

func TestSafeWorkspaceRelPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"report.md", true},
		{"docs/report.md", true},
		{".agentflow/inputs/a.md", true},
		{"..hidden/a", true},
		{"", false},
		{".", false},
		{"..", false},
		{"../a", false},
		{"a/../../b", false},
		{"/etc/passwd", false},
		{`a\b`, false},
		{"a//b", false},
		{"a/./b", false},
		{"a/", false},
		{"a/b/..", false},
	}
	for _, tt := range tests {
		if got := SafeWorkspaceRelPath(tt.path); got != tt.want {
			t.Errorf("SafeWorkspaceRelPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestRunnerInvocationGitSSHRemotes(t *testing.T) {
	sshRepo := &RunnerRepository{CloneURL: "git@example.com:legacy.git", UseSSH: true}
	httpsRepo := &RunnerRepository{CloneURL: "https://example.com/legacy.git"}
	tests := []struct {
		name string
		inv  *RunnerInvocation
		want []string
	}{
		{name: "nil invocation", inv: nil, want: nil},
		{name: "no repositories", inv: &RunnerInvocation{}, want: nil},
		{name: "legacy ssh repository", inv: &RunnerInvocation{Repository: sshRepo}, want: []string{"git@example.com:legacy.git"}},
		{name: "legacy https repository", inv: &RunnerInvocation{Repository: httpsRepo}, want: nil},
		{
			name: "list keeps only ssh repositories in order",
			inv: &RunnerInvocation{Repositories: []RunnerRepository{
				{CloneURL: "git@example.com:a.git", UseSSH: true},
				{CloneURL: "https://example.com/b.git"},
				{CloneURL: "git@example.com:c.git", UseSSH: true},
			}},
			want: []string{"git@example.com:a.git", "git@example.com:c.git"},
		},
		{
			name: "list takes precedence over legacy field",
			inv: &RunnerInvocation{
				Repository:   sshRepo,
				Repositories: []RunnerRepository{{CloneURL: "https://example.com/b.git"}},
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.inv.GitSSHRemotes(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("GitSSHRemotes() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestPublicRunnersStripSessionMaterial(t *testing.T) {
	expires := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	in := []Runner{
		{ID: "r1", Name: "dev", RunnerVersion: "0.9.3", SessionTokenHash: "hash-1", SessionExpiresAt: &expires},
		{ID: "r2", SessionTokenHash: "hash-2"},
	}
	got := PublicRunners(in)
	if len(got) != 2 {
		t.Fatalf("runners = %#v", got)
	}
	for i, r := range got {
		if r.SessionTokenHash != "" || r.SessionExpiresAt != nil {
			t.Errorf("runners[%d] leaks session material: %#v", i, r)
		}
		if r.ID != in[i].ID {
			t.Errorf("runners[%d].ID = %q, want %q", i, r.ID, in[i].ID)
		}
	}
	if got[0].Name != "dev" || got[0].RunnerVersion != "0.9.3" {
		t.Errorf("public fields changed: %#v", got[0])
	}
	if in[0].SessionTokenHash != "hash-1" || in[0].SessionExpiresAt == nil {
		t.Fatal("PublicRunners must not mutate the stored runners")
	}
	if empty := PublicRunners(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("PublicRunners(nil) = %#v, want empty non-nil slice", empty)
	}
}

func TestRunnerCapabilityVersionGates(t *testing.T) {
	gates := []struct {
		name     string
		minimum  string
		supports func(Runner) bool
	}{
		{"WorkBranchSync", RunnerVersionWorkBranchSync, Runner.SupportsWorkBranchSync},
		{"ReportedOutputs", RunnerVersionReportedOutputs, Runner.SupportsReportedOutputs},
		{"Integrate", RunnerVersionIntegrate, Runner.SupportsIntegrate},
		{"AlignBaseline", RunnerVersionAlignBaseline, Runner.SupportsAlignBaseline},
		{"WorkspacePreparation", RunnerVersionWorkspacePreparation, Runner.SupportsWorkspacePreparation},
		{"ExistingWorkBranch", RunnerVersionExistingWorkBranch, Runner.SupportsExistingWorkBranch},
		{"WorkspaceOwnerPreparation", RunnerVersionWorkspaceOwnerPreparation, Runner.SupportsWorkspaceOwnerPreparation},
		{"ConversationWorkspace", RunnerVersionConversationWorkspace, Runner.SupportsConversationWorkspace},
		{"SharedPush", RunnerVersionSharedPush, Runner.SupportsSharedPush},
		{"CreateNewWorkBranch", RunnerVersionCreateNewWorkBranch, Runner.SupportsCreateNewWorkBranch},
		{"GitSSH", RunnerVersionGitSSH, Runner.SupportsGitSSH},
		{"ConversationCLI", RunnerVersionConversationCLI, Runner.SupportsConversationCLI},
	}
	versions := []struct {
		version string
		below   bool
	}{
		{"0.1.0", true},
		{"", true},
		{"dev", true},
		{"99.0.0", false},
		{"v99.0", false},
	}
	for _, g := range gates {
		t.Run(g.name, func(t *testing.T) {
			if !g.supports(Runner{RunnerVersion: g.minimum}) {
				t.Errorf("minimum version %s must be supported", g.minimum)
			}
			if !g.supports(Runner{RunnerVersion: " v" + g.minimum + " "}) {
				t.Errorf("padded v-prefixed %s must be supported", g.minimum)
			}
			for _, v := range versions {
				if got := g.supports(Runner{RunnerVersion: v.version}); got == v.below {
					t.Errorf("version %q supported = %v, want %v", v.version, got, !v.below)
				}
			}
		})
	}
}

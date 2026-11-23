package domain

import (
	"strings"
	"testing"
)

func TestValidateLocalSandbox(t *testing.T) {
	valid := SandboxProfile{ID: "mine", Driver: SandboxDriverLocal, RunnerID: "runner-1", BaseDir: "~/work", RepositoryPaths: map[string]string{"repo-1": "/srv/service"}}
	if err := ValidateLocalSandbox(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*SandboxProfile){
		"unbound":          func(p *SandboxProfile) { p.RunnerID = " " },
		"shared":           func(p *SandboxProfile) { p.Shared = true },
		"relative base":    func(p *SandboxProfile) { p.BaseDir = "work" },
		"home base":        func(p *SandboxProfile) { p.BaseDir = "~/" },
		"root path":        func(p *SandboxProfile) { p.RepositoryPaths = map[string]string{"repo-1": "/"} },
		"relative path":    func(p *SandboxProfile) { p.RepositoryPaths = map[string]string{"repo-1": "service"} },
		"newline in path":  func(p *SandboxProfile) { p.RepositoryPaths = map[string]string{"repo-1": "/srv/a\nb"} },
		"missing repo ID":  func(p *SandboxProfile) { p.RepositoryPaths = map[string]string{" ": "/srv/service"} },
		"tilde other user": func(p *SandboxProfile) { p.BaseDir = "~other/work" },
	} {
		profile := valid
		profile.RepositoryPaths = map[string]string{"repo-1": "/srv/service"}
		mutate(&profile)
		if err := ValidateLocalSandbox(profile); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestLocalRepositoryPath(t *testing.T) {
	profile := SandboxProfile{RepositoryPaths: map[string]string{"repo-1": " /srv/service "}}
	for _, tc := range []struct {
		profile  SandboxProfile
		id, name string
		want     string
	}{
		{profile, "repo-1", "service", "/srv/service"},
		{profile, "repo-2", "group/web", "~/.loopforge/local/web"},
		{SandboxProfile{BaseDir: "/data/src"}, "repo-2", "web", "/data/src/web"},
		{SandboxProfile{}, "repo-3", ".hidden", "~/.loopforge/local/repo-3"},
		{SandboxProfile{}, "..", "..", ""},
	} {
		if got := tc.profile.LocalRepositoryPath(tc.id, tc.name); got != tc.want {
			t.Errorf("LocalRepositoryPath(%q, %q) = %q, want %q", tc.id, tc.name, got, tc.want)
		}
	}
}

func TestWorkflowSandboxRejectsOnlyLocal(t *testing.T) {
	for _, driver := range []string{SandboxDriverDocker, SandboxDriverNative, SandboxDriverSeatbelt, SandboxDriverBwrap} {
		if err := ValidateWorkflowSandbox(SandboxProfile{ID: "p", Driver: driver}); err != nil {
			t.Errorf("%s: %v", driver, err)
		}
	}
	if err := ValidateWorkflowSandbox(SandboxProfile{ID: "mine", Driver: " local "}); err == nil || !strings.Contains(err.Error(), "SOP 不能使用 local") {
		t.Fatalf("local SOP sandbox: %v", err)
	}
}

func TestLocalWorkspaceAndRunnerVersion(t *testing.T) {
	local := &RunnerSandboxSpec{Profile: SandboxProfile{Driver: SandboxDriverLocal}}
	native := &RunnerSandboxSpec{Profile: SandboxProfile{Driver: SandboxDriverNative}}
	for _, tc := range []struct {
		run  Run
		want bool
	}{
		{Run{Kind: RunKindWorkspace, WorkflowSnapshot: WorkflowDefinition{SandboxSnapshot: local}}, true},
		{Run{Kind: RunKindWorkspace, WorkflowSnapshot: WorkflowDefinition{SandboxSnapshot: native}}, false},
		{Run{Kind: RunKindWorkspace}, false},
		{Run{WorkflowSnapshot: WorkflowDefinition{SandboxSnapshot: local}}, false},
	} {
		if got := tc.run.IsLocalWorkspace(); got != tc.want {
			t.Errorf("IsLocalWorkspace(%+v) = %v", tc.run, got)
		}
	}
	if (Runner{RunnerVersion: RunnerVersionGitSSHKnownHosts}).SupportsLocalWorktree() || !(Runner{RunnerVersion: RunnerVersionLocalWorktree}).SupportsLocalWorktree() {
		t.Fatal("local worktree version gate")
	}
}

package domain

import "testing"

// TestPublicRunnerTaskScrubsMultiRepositoryTokens verifies that management
// responses strip GitToken from every repository entry (legacy single field
// and the multi-repository list) and never mutate the caller's task.
func TestPublicRunnerTaskScrubsMultiRepositoryTokens(t *testing.T) {
	task := RunnerTask{
		ID: "task-1",
		Invocation: &RunnerInvocation{
			CodeBuddyAPIKey: "cb-secret",
			Repository: &RunnerRepository{
				CloneURL: "git@example.com:a.git",
				GitToken: "single-token",
			},
			Repositories: []RunnerRepository{
				{CloneURL: "git@example.com:a.git", GitToken: "token-a"},
				{CloneURL: "git@example.com:b.git", GitToken: "token-b"},
			},
		},
	}
	public := PublicRunnerTask(task)
	inv := public.Invocation
	if inv.CodeBuddyAPIKey != "" {
		t.Fatal("CodeBuddyAPIKey must be scrubbed")
	}
	if inv.Repository == nil || inv.Repository.GitToken != "" {
		t.Fatal("legacy repository GitToken must be scrubbed")
	}
	if len(inv.Repositories) != 2 {
		t.Fatalf("repositories length = %d", len(inv.Repositories))
	}
	for i, repo := range inv.Repositories {
		if repo.GitToken != "" {
			t.Fatalf("repositories[%d].GitToken must be scrubbed", i)
		}
		if repo.CloneURL == "" {
			t.Fatalf("repositories[%d].CloneURL must be preserved", i)
		}
	}
	// The original task must keep its credentials: the runner claim endpoint
	// still needs them after a management response was rendered.
	if task.Invocation.Repositories[0].GitToken != "token-a" || task.Invocation.Repositories[1].GitToken != "token-b" {
		t.Fatal("PublicRunnerTask must not mutate the source task repositories")
	}
	if task.Invocation.Repository.GitToken != "single-token" {
		t.Fatal("PublicRunnerTask must not mutate the source task repository")
	}
}

func TestRunnerSupportsWorkBranchSync(t *testing.T) {
	for version, want := range map[string]bool{
		"": false, "dev": false, "0.1.0": false, "0.1.9": false,
		"0.2.0": true, "v0.2.0": true, "0.2": true, "0.10.0": true, "1.0.0": true,
	} {
		if got := (Runner{RunnerVersion: version}).SupportsWorkBranchSync(); got != want {
			t.Errorf("SupportsWorkBranchSync(%q) = %v, want %v", version, got, want)
		}
	}
}

func TestRunnerSupportsExistingWorkBranch(t *testing.T) {
	for version, want := range map[string]bool{
		"": false, "dev": false, "0.6.0": false, "0.6.99": false,
		"0.7.0": true, "v0.7.0": true, "0.7": true, "0.10.0": true, "1.0.0": true,
	} {
		if got := (Runner{RunnerVersion: version}).SupportsExistingWorkBranch(); got != want {
			t.Errorf("SupportsExistingWorkBranch(%q) = %v, want %v", version, got, want)
		}
	}
}

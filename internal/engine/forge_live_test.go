package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/forge"
	"agentflow/internal/gitwork"
)

// Run only against a disposable repository. This test pushes and merges a
// branch into the repository's existing agentflow/integration branch.
func TestForgejoScheduledIntegrationLive(t *testing.T) {
	web, sshURL := os.Getenv("LOOPFORGE_TEST_FORGEJO_WEB_URL"), os.Getenv("LOOPFORGE_TEST_FORGEJO_SSH_URL")
	tokenPath, keyPath := os.Getenv("LOOPFORGE_TEST_FORGEJO_TOKEN_FILE"), os.Getenv("LOOPFORGE_TEST_FORGEJO_KEY_FILE")
	if web == "" || sshURL == "" || tokenPath == "" || keyPath == "" {
		t.Skip("provide a disposable Forgejo repository, token and SSH key")
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	ref, remote, err := forge.BindRepository(web, sshURL)
	if err != nil {
		t.Fatal(err)
	}
	e, store := cvEEngine(t)
	if err := store.PutUser(domain.User{LoginName: "forgejo-scheduled-actor"}); err != nil {
		t.Fatal(err)
	}
	actor, _ := store.UserByLoginName("forgejo-scheduled-actor")
	if err := store.SaveForgeToken(actor.ID, ref.Instance, strings.TrimSpace(string(token))); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveGitSSHKey(actor.ID, "", remote.Endpoint(), "Forgejo", string(key)); err != nil {
		t.Fatal(err)
	}
	const baseline = "agentflow/integration"
	branch := fmt.Sprintf("feature/forgejo-sop-%d", time.Now().UnixNano())
	rc := domain.RepositoryContext{
		RepositoryID: "forgejo-test", Provider: domain.RepositoryProviderForgejo, Name: ref.Name,
		GongfengProjectID: ref.Project(), WebURL: ref.WebURL(), CloneURL: remote.URL,
		DefaultBranch: "main", BaseBranch: baseline, IntegrateBranch: baseline, WorkBranch: branch,
		CreateNewWorkBranch: true,
	}
	run := domain.Run{ID: "forgejo-scheduled", Repository: rc, ExecutionContext: domain.TelemetryContext{
		ResponsibleUserID: actor.ID, ScheduleJobID: "forgejo-acceptance",
	}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	env := &runEnvironment{sandbox: stubWorkspaceSandbox{workspace: t.TempDir()}, credentials: e.resolveRunCredentials(run)}
	e.runEnvs[run.ID] = env
	ctx := gitwork.WithSSHRemote(gitwork.WithSSHKeys(t.Context(), env.credentials.GitSSHKeys), remote.URL)
	if err := prepareRepository(ctx, env.Workspace(), rc); err != nil {
		t.Fatal(err)
	}
	mainBefore, err := gitwork.C(ctx, env.Workspace(), "ls-remote", "origin", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.Workspace(), "scheduled-acceptance.txt"), []byte(branch+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rework, deliveries, err := e.integrateScheduledRun(run.ID)
	if err != nil || rework || len(deliveries) != 1 {
		t.Fatalf("integration = %v, %+v, %v", rework, deliveries, err)
	}
	mr := deliveries[0]
	if mr.ForgeUserID != actor.ID || mr.TargetBranch != baseline || mr.State != "merged" {
		t.Fatalf("delivery = %+v", mr)
	}
	mainAfter, err := gitwork.C(ctx, env.Workspace(), "ls-remote", "origin", "refs/heads/main")
	if err != nil || mainBefore == "" || mainAfter != mainBefore {
		t.Fatalf("default branch changed: %q → %q, %v", mainBefore, mainAfter, err)
	}
}

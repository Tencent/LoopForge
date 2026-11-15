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

// Run only against a disposable repository. Its protected agentflow/integration
// branch should initially lack a commit from main to exercise baseline alignment.
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
	tip := func(branch string) string {
		t.Helper()
		out, err := gitwork.C(ctx, env.Workspace(), "ls-remote", rc.CloneURL, "refs/heads/"+branch)
		fields := strings.Fields(out)
		if err != nil || len(fields) != 2 {
			t.Fatalf("read %s: %q, %v", branch, out, err)
		}
		return fields[0]
	}
	mainBefore, baselineBefore := tip("main"), tip(baseline)
	if err := e.AlignBaselineForUser(rc, actor.ID); err != nil {
		t.Fatalf("scheduled baseline alignment: %v", err)
	}
	baselineAfter := tip(baseline)
	if baselineAfter == baselineBefore {
		t.Fatal("integration branch did not advance; seed a new default branch commit before running this test")
	}
	if err := prepareRepository(ctx, env.Workspace(), rc); err != nil {
		t.Fatal(err)
	}
	if _, err := gitwork.C(ctx, env.Workspace(), "merge-base", "--is-ancestor", mainBefore, "HEAD"); err != nil {
		t.Fatalf("aligned workspace is missing the default branch commit: %v", err)
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
	integrated := tip(baseline)
	if integrated == baselineAfter {
		t.Fatal("scheduled delivery did not advance the integration branch")
	}
	if err := e.AlignBaselineForUser(rc, actor.ID); err != nil || tip(baseline) != integrated {
		t.Fatalf("retry of aligned baseline changed the integration branch: %v", err)
	}
	if mainAfter := tip("main"); mainAfter != mainBefore {
		t.Fatalf("default branch changed: %q → %q", mainBefore, mainAfter)
	}
}

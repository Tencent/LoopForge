package tracker

// Opt-in real GitHub acceptance. Uses two throwaway branches; never merges
// into the default branch. The caller must explicitly authorize the repository.
import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGitHubLiveImportAndDelivery(t *testing.T) {
	if os.Getenv("AGENTFLOW_GITHUB_LIVE_WRITE") != "1" {
		t.Skip("opt in with AGENTFLOW_GITHUB_LIVE_WRITE=1 and a disposable repository/token file")
	}
	path := os.Getenv("AGENTFLOW_GITHUB_LIVE_REPO")
	info, ok := domain.ParseRepositoryURL("https://github.com/" + path)
	if !ok || path == "Tencent/LoopForge" || info.Project != "github:"+path {
		t.Fatal("an explicitly authorized test repository is required")
	}
	raw, err := os.ReadFile(os.Getenv("AGENTFLOW_GITHUB_LIVE_TOKEN_FILE"))
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		t.Fatal("a protected GitHub token file is required")
	}
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutUser(domain.User{LoginName: "live-owner", GitToken: strings.TrimSpace(string(raw))}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("live-owner")
	if err := st.PutProject(domain.Project{ID: "live-github", Name: "GitHub live test", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	r := domain.Repository{ID: "live-repository", ProjectID: "live-github", Provider: "github", Name: info.Name, GongfengProjectID: info.Project, WebURL: info.WebURL, DefaultBranch: "main"}
	if err := st.PutRepository(r); err != nil {
		t.Fatal(err)
	}
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	g, err := s.githubClient(info.Project)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, suffix string, in, out any) {
		t.Helper()
		if err := g.request(method, suffix, in, out); err != nil {
			t.Fatalf("%s %s: %v", method, suffix, err)
		}
	}
	var remote struct {
		DefaultBranch string `json:"default_branch"`
	}
	call("GET", "", nil, &remote)
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	call("GET", "/git/ref/heads/"+url.PathEscape(remote.DefaultBranch), nil, &ref)
	stamp := fmt.Sprintf("loopforge-e2e/%d", time.Now().UnixNano())
	base, head := stamp+"-base", stamp+"-change"
	for _, branch := range []string{base, head} {
		call("POST", "/git/refs", map[string]any{"ref": "refs/heads/" + branch, "sha": ref.Object.SHA}, nil)
		defer func(branch string) {
			if err := g.request("DELETE", "/git/refs/heads/"+url.PathEscape(branch), nil, nil); err != nil {
				t.Errorf("cleanup branch: %v", err)
			}
		}(branch)
	}
	var issue struct {
		Number int `json:"number"`
	}
	call("POST", "/issues", map[string]any{"title": "LoopForge live acceptance " + stamp, "body": "Disposable integration fixture; automatically closed."}, &issue)
	defer func() {
		if err := g.request("PATCH", fmt.Sprintf("/issues/%d", issue.Number), map[string]string{"state": "closed"}, nil); err != nil {
			t.Errorf("cleanup issue: %v", err)
		}
	}()
	id := fmt.Sprintf("gh-%s-%d", r.ID, issue.Number)
	// GitHub's issue listing can lag a successful create/close response.
	awaitIssue := func(status string) domain.Issue {
		t.Helper()
		for attempt := 0; attempt < 25; attempt++ {
			if _, err := s.pullGitHubIssues(st, r); err != nil {
				t.Fatal(err)
			}
			local, found := st.Issue(id)
			if found && local.Source == "github" && local.Status == status {
				return local
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("GitHub listing did not synchronize %s to %s", id, status)
		return domain.Issue{}
	}
	local := awaitIssue("todo")
	local.Description = "Locally clarified brief"
	if err := st.PutIssue(local); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pullGitHubIssues(st, r); err != nil {
		t.Fatal(err)
	}
	local, _ = st.Issue(id)
	if local.Description != "Locally clarified brief" {
		t.Fatal("sync overwrote local clarification")
	}
	filename := "loopforge-e2e-" + strings.TrimPrefix(stamp, "loopforge-e2e/") + ".txt"
	call("PUT", "/contents/"+filename, map[string]string{"message": "test: isolated LoopForge delivery", "branch": head, "content": base64.StdEncoding.EncodeToString([]byte("live-delivery-ok\n"))}, nil)
	pr, err := s.EnsureAndMergeBranchMR(info.Project, head, base, "LoopForge isolated delivery acceptance", "Only merges disposable test branches. Default branch remains unchanged.")
	if err != nil {
		t.Fatal(err)
	}
	delivery, found, err := s.FindBranchMR(info.Project, head)
	if err != nil || !found || delivery.State != "merged" {
		t.Fatalf("delivery state: %+v found=%v err=%v", delivery, found, err)
	}
	t.Log("Created acceptance PR:", delivery.URL)
	if again, err := s.EnsureAndMergeBranchMR(info.Project, head, base, "", ""); err != nil || again != pr {
		t.Fatalf("idempotent merge: %s %v", again, err)
	}
	var content struct {
		Content string `json:"content"`
	}
	call("GET", "/contents/"+filename+"?ref="+url.QueryEscape(base), nil, &content)
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(content.Content, "\n", ""))
	if err != nil || string(decoded) != "live-delivery-ok\n" {
		t.Fatal("merged content mismatch")
	}
	call("PATCH", fmt.Sprintf("/issues/%d", issue.Number), map[string]string{"state": "closed"}, nil)
	awaitIssue("closed")
	var unchanged struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	call("GET", "/git/ref/heads/"+url.PathEscape(remote.DefaultBranch), nil, &unchanged)
	if unchanged.Object.SHA != ref.Object.SHA {
		t.Fatal("default branch changed during acceptance")
	}
}

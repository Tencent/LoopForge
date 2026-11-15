package tracker

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

type githubClient struct {
	base, path, token string
	http              *http.Client
}

func (s *Service) githubClient(project string) (*githubClient, error) {
	path := strings.TrimPrefix(project, "github:")
	info, ok := domain.ParseRepositoryURL("https://github.com/" + path)
	if !ok || info.Project != project {
		return nil, fmt.Errorf("invalid GitHub repository")
	}
	var token string
	if s.projectAuth != nil {
		for _, repo := range s.projectAuth.Repositories() {
			if repo.Provider != "github" || repo.GongfengProjectID != project {
				continue
			}
			if p, found := s.projectAuth.Project(repo.ProjectID); found {
				if u, exists := s.projectAuth.User(p.OwnerID); exists {
					token = u.GitToken
				}
			}
			break
		}
	}
	base := s.githubAPIBase
	if base == "" {
		base = "https://api.github.com"
	}
	return &githubClient{base: base, path: path, token: token, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// githubClientForRun authenticates as the run did: its push token when set,
// else the Owner of the run's project when that project binds the repository.
// It also names the identity it chose.
func (s *Service) githubClientForRun(project, localProjectID, gitToken string) (*githubClient, string, error) {
	g, err := s.githubClient(project)
	if err != nil {
		return nil, "", err
	}
	if token := strings.TrimSpace(gitToken); token != "" {
		g.token = token
		return g, "本轮责任人的 Git Token", nil
	}
	local := strings.TrimSpace(localProjectID)
	if s.projectAuth == nil || local == "" {
		return g, "首个绑定该仓库的项目 Owner 的 Git Token", nil
	}
	for _, repo := range s.projectAuth.Repositories() {
		if repo.Provider != "github" || repo.GongfengProjectID != project || repo.ProjectID != local {
			continue
		}
		g.token = ""
		if p, found := s.projectAuth.Project(local); found {
			if u, exists := s.projectAuth.User(p.OwnerID); exists {
				g.token = u.GitToken
			}
		}
		return g, "项目 " + local + " Owner 的 Git Token", nil
	}
	return g, "首个绑定该仓库的项目 Owner 的 Git Token", nil
}

func (g *githubClient) request(method, suffix string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequest(method, g.base+"/repos/"+g.path+suffix, body)
	if err != nil {
		return err
	}
	r.Header.Set("Accept", "application/vnd.github+json")
	r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	r.Header.Set("Content-Type", "application/json")
	if g.token != "" {
		r.Header.Set("Authorization", "Bearer "+g.token)
	}
	res, err := g.http.Do(r)
	if err != nil {
		return fmt.Errorf("GitHub request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitHub returned HTTP %d; check the project owner's Git token and repository permissions", res.StatusCode)
	}
	if output == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(output)
}

type githubPull struct {
	Number   int        `json:"number"`
	URL      string     `json:"html_url"`
	State    string     `json:"state"`
	Merged   bool       `json:"merged"`
	MergedAt *time.Time `json:"merged_at"`
	Head     struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// merged reports whether GitHub considers the pull request merged; either
// flag is enough because list responses may omit "merged".
func (p githubPull) merged() bool { return p.Merged || p.MergedAt != nil }

func (p githubPull) hosted() hostedPull {
	h := hostedPull{Number: p.Number, State: "opened", TargetBranch: p.Base.Ref, HeadSHA: p.Head.SHA}
	if p.merged() {
		h.State = "merged"
		if p.MergedAt != nil {
			h.MergedAt = p.MergedAt.UTC()
		}
	} else if p.State == "closed" {
		h.State = "closed"
	}
	return h
}

func (p githubPull) ref() string { return p.hosted().ref() }

func (p githubPull) delivery(project string) domain.DeliveryMR { return p.hosted().delivery(project) }

func (g *githubClient) findPull(branch, target string) (hostedPull, bool, error) {
	list, err := g.listPulls(branch, target)
	if err != nil {
		return hostedPull{}, false, err
	}
	for _, p := range list {
		if p.Head.Ref == branch && (target == "" || p.Base.Ref == target) {
			return p.hosted(), true, nil
		}
	}
	return hostedPull{}, false, nil
}

// listPulls returns the pulls from branch (and into target when set), most
// recently updated first.
func (g *githubClient) listPulls(branch, target string) ([]githubPull, error) {
	q := url.Values{"state": {"all"}, "head": {strings.Split(g.path, "/")[0] + ":" + branch}, "per_page": {"100"}, "sort": {"updated"}, "direction": {"desc"}}
	if target != "" {
		q.Set("base", target)
	}
	var list []githubPull
	if err := g.request("GET", "/pulls?"+q.Encode(), nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func githubPullsInto(pulls []githubPull, project, branch, target string) []domain.DeliveryMR {
	var out []domain.DeliveryMR
	for _, p := range pulls {
		if p.Head.Ref != branch || p.Base.Ref != target {
			continue
		}
		mr := p.hosted().delivery(project)
		mr.HeadSHA = strings.TrimSpace(p.Head.SHA)
		out = append(out, mr)
	}
	return out
}

func (g *githubClient) createPull(branch, target, title, body string) (hostedPull, error) {
	var p githubPull
	if err := g.request("POST", "/pulls", map[string]any{"head": branch, "base": target, "title": title, "body": body}, &p); err != nil {
		return hostedPull{}, err
	}
	return p.hosted(), nil
}

func (g *githubClient) mergePull(p hostedPull) error {
	var result struct {
		Merged bool `json:"merged"`
	}
	if err := g.request("PUT", "/pulls/"+strconv.Itoa(p.Number)+"/merge", map[string]any{"merge_method": "merge", "sha": p.HeadSHA}, &result); err != nil {
		return err
	}
	if !result.Merged {
		return fmt.Errorf("GitHub did not merge pull request #%d", p.Number)
	}
	return nil
}

func (g *githubClient) pull(number int) (hostedPull, error) {
	var p githubPull
	if err := g.request("GET", "/pulls/"+strconv.Itoa(number), nil, &p); err != nil {
		return hostedPull{}, err
	}
	return p.hosted(), nil
}

func (s *Service) pullGitHubIssues(store IssueStoreAdapter, repo domain.Repository) (int, error) {
	g, err := s.githubClient(repo.GongfengProjectID)
	if err != nil {
		return 0, err
	}
	count := 0
	for page := 1; page <= 100; page++ {
		var list []struct {
			ID          int64           `json:"id"`
			Number      int             `json:"number"`
			Title       string          `json:"title"`
			Body        string          `json:"body"`
			State       string          `json:"state"`
			URL         string          `json:"html_url"`
			CreatedAt   time.Time       `json:"created_at"`
			PullRequest json.RawMessage `json:"pull_request"`
		}
		if err := g.request("GET", "/issues?state=all&per_page=100&page="+strconv.Itoa(page), nil, &list); err != nil {
			return count, err
		}
		for _, item := range list {
			if len(item.PullRequest) > 0 {
				continue
			}
			id := fmt.Sprintf("gh-%s-%d", repo.ID, item.Number)
			issue := domain.Issue{ID: id, Number: item.Number, ProjectID: repo.ProjectID, RepositoryID: repo.ID, BaseBranch: repo.DefaultBranch, Kind: "issue", Title: item.Title, Description: item.Body, Priority: "medium", Source: "github", Status: gongfengStateToStatus(item.State), CreatedAt: item.CreatedAt, UpdatedAt: time.Now().UTC(), ExternalRef: &domain.ExternalRef{Source: "github", ExternalID: strconv.FormatInt(item.ID, 10), ExternalIID: item.Number, ExternalURL: item.URL, ExternalState: item.State, ExternalType: "issue", SyncedAt: time.Now().UTC()}}
			issue.ExternalRef.SourceContentHash = githubBodyHash(item.Body)
			if item.State == "open" {
				issue.Status = "todo"
			}
			if item.State == "closed" {
				issue.CloseSource = "external"
			}
			if old, ok := store.Issue(id); ok {
				applyExistingIssue(&issue, old)
				if old.Clarification.Status != "" || (old.ExternalRef != nil && githubBodyHash(old.Description) != old.ExternalRef.SourceContentHash) {
					issue.Description = old.Description
				}
			}
			if err := store.PutIssue(issue); err != nil {
				return count, err
			}
			count++
		}
		if len(list) < 100 {
			return count, nil
		}
	}
	return count, fmt.Errorf("GitHub import exceeded 100 pages; narrow the repository scope")
}

func githubBodyHash(body string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(body))) }

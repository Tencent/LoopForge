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

func (p githubPull) delivery(project string) domain.DeliveryMR {
	state := "opened"
	if p.Merged || p.MergedAt != nil {
		state = "merged"
	} else if p.State == "closed" {
		state = "closed"
	}
	return domain.DeliveryMR{Project: project, IID: p.Number, URL: domain.GongfengMRURL(project, p.Number), State: state, GongfengState: state, TargetBranch: p.Base.Ref}
}

func (g *githubClient) findPull(branch, target string) (githubPull, bool, error) {
	q := url.Values{"state": {"all"}, "head": {strings.Split(g.path, "/")[0] + ":" + branch}, "per_page": {"100"}, "sort": {"updated"}, "direction": {"desc"}}
	if target != "" {
		q.Set("base", target)
	}
	var list []githubPull
	if err := g.request("GET", "/pulls?"+q.Encode(), nil, &list); err != nil {
		return githubPull{}, false, err
	}
	for _, p := range list {
		if p.Head.Ref == branch && (target == "" || p.Base.Ref == target) {
			return p, true, nil
		}
	}
	return githubPull{}, false, nil
}

func (s *Service) githubFindBranch(project, branch string) (domain.DeliveryMR, bool, error) {
	g, err := s.githubClient(project)
	if err != nil {
		return domain.DeliveryMR{}, false, err
	}
	p, ok, err := g.findPull(branch, "")
	if err != nil || !ok {
		return domain.DeliveryMR{}, ok, err
	}
	return p.delivery(project), true, nil
}

func (s *Service) githubMergeBranch(project, branch, target, title, description string) (string, error) {
	if branch == "" {
		return "", fmt.Errorf("source branch is required")
	}
	if branch == target {
		return "", nil
	}
	g, err := s.githubClient(project)
	if err != nil {
		return "", err
	}
	p, found, err := g.findPull(branch, target)
	if err != nil {
		return "", err
	}
	if found && (p.Merged || p.MergedAt != nil) {
		return "#" + strconv.Itoa(p.Number), nil
	}
	if !found || p.State == "closed" {
		if target == "" {
			return "", ErrNoOpenMR
		}
		if title == "" {
			title = "LoopForge: " + branch + " → " + target
		}
		if err := g.request("POST", "/pulls", map[string]any{"head": branch, "base": target, "title": title, "body": description}, &p); err != nil {
			return "", err
		}
	}
	var result struct {
		Merged bool `json:"merged"`
	}
	if err := g.request("PUT", "/pulls/"+strconv.Itoa(p.Number)+"/merge", map[string]any{"merge_method": "merge", "sha": p.Head.SHA}, &result); err != nil {
		return "", err
	}
	if !result.Merged {
		return "", fmt.Errorf("GitHub did not merge pull request #%d", p.Number)
	}
	return "#" + strconv.Itoa(p.Number), nil
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

func (s *Service) refreshGitHubDelivery(store MRDeliveryStore, project string, number int) error {
	g, err := s.githubClient(project)
	if err != nil {
		return err
	}
	var p githubPull
	if err := g.request("GET", "/pulls/"+strconv.Itoa(number), nil, &p); err != nil {
		return err
	}
	d := p.delivery(project)
	return store.UpdateMRDeliveryState(project, number, d.GongfengState, d.TargetBranch, time.Now().UTC())
}

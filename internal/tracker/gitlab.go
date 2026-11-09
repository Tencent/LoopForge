package tracker

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type gitLabClient struct {
	base, path, token string
	http              *http.Client
	projectID         int
	ctx               context.Context
}
type gitLabMR struct {
	IID             int        `json:"iid"`
	State           string     `json:"state"`
	WebURL          string     `json:"web_url"`
	SourceBranch    string     `json:"source_branch"`
	TargetBranch    string     `json:"target_branch"`
	SHA             string     `json:"sha"`
	SourceProjectID int        `json:"source_project_id"`
	TargetProjectID int        `json:"target_project_id"`
	MergedAt        *time.Time `json:"merged_at"`
}

func (s *Service) gitLabClient(project string) (*gitLabClient, error) {
	if s.projectAuth == nil {
		return nil, fmt.Errorf("GitLab 项目配置不可用")
	}
	for _, r := range s.projectAuth.Repositories() {
		if r.Provider != "gitlab" || r.GongfengProjectID != project {
			continue
		}
		p, ok := s.projectAuth.Project(r.ProjectID)
		if !ok {
			break
		}
		u, ok := s.projectAuth.User(p.OwnerID)
		if !ok || u.GitLabToken == "" {
			return nil, fmt.Errorf("项目负责人尚未配置 GitLab Token")
		}
		info, ok := domain.ParseGitLabRepositoryURL(project, u.GitLabBaseURL)
		if !ok || info.Project != project {
			return nil, fmt.Errorf("仓库地址与项目负责人的 GitLab 地址不一致")
		}
		base := strings.TrimRight(u.GitLabBaseURL, "/")
		path := strings.TrimPrefix(strings.TrimPrefix(project, "gitlab:"), base+"/")
		return &gitLabClient{base: base, path: path, token: u.GitLabToken, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
	}
	return nil, fmt.Errorf("GitLab 仓库未配置")
}
func (g *gitLabClient) request(method, suffix string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	ctx := g.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+"/api/v4/projects/"+url.PathEscape(g.path)+suffix, body)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接 GitLab")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitLab 返回 HTTP %d，请检查权限与仓库状态", res.StatusCode)
	}
	if output == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(output)
}

// Resolve the configured path before trusting project IDs in an MR response.
func (g *gitLabClient) resolveProject() error {
	if g.projectID > 0 {
		return nil
	}
	var project struct {
		ID   int    `json:"id"`
		Path string `json:"path_with_namespace"`
	}
	if err := g.request("GET", "", nil, &project); err != nil {
		return err
	}
	if project.ID <= 0 || project.Path != g.path {
		return fmt.Errorf("GitLab did not confirm the configured project identity")
	}
	g.projectID = project.ID
	return nil
}

func (g *gitLabClient) matches(m gitLabMR, branch, target string) bool {
	return g.projectID > 0 && m.IID > 0 && m.SourceProjectID == g.projectID &&
		m.TargetProjectID == g.projectID && m.SourceBranch == branch &&
		m.TargetBranch != "" && (target == "" || m.TargetBranch == target)
}

// Check current branch tips rather than treating a historical MR as delivery of
// new commits. Immutable IDs keep the ancestry check tied to the observed tips.
func (g *gitLabClient) delivered(branch, target string) (bool, error) {
	tips := make([]string, 0, 2)
	for _, ref := range []string{branch, target} {
		var b struct {
			Commit struct {
				ID string `json:"id"`
			} `json:"commit"`
		}
		if err := g.request("GET", "/repository/branches/"+url.PathEscape(ref), nil, &b); err != nil {
			return false, err
		}
		if strings.TrimSpace(b.Commit.ID) == "" {
			return false, fmt.Errorf("GitLab did not return the branch HEAD")
		}
		tips = append(tips, b.Commit.ID)
	}
	if tips[0] == tips[1] {
		return true, nil
	}
	var base struct {
		ID string `json:"id"`
	}
	q := url.Values{"refs[]": tips}
	if err := g.request("GET", "/repository/merge_base?"+q.Encode(), nil, &base); err != nil {
		return false, err
	}
	if strings.TrimSpace(base.ID) == "" {
		return false, fmt.Errorf("GitLab did not confirm branch ancestry")
	}
	return base.ID == tips[0], nil
}

func (g *gitLabClient) find(branch, target string) (gitLabMR, bool, error) {
	if err := g.resolveProject(); err != nil {
		return gitLabMR{}, false, err
	}
	// Query opened separately so a full page of recently updated history cannot
	// hide the active MR. Do not rely on the server to filter identity or state.
	for _, state := range []string{"opened", "merged"} {
		for page := 1; page <= 100; page++ {
			q := url.Values{"scope": {"all"}, "state": {state}, "source_branch": {branch}, "per_page": {"100"}, "page": {strconv.Itoa(page)}, "order_by": {"updated_at"}, "sort": {"desc"}}
			if target != "" {
				q.Set("target_branch", target)
			}
			var rows []gitLabMR
			if err := g.request("GET", "/merge_requests?"+q.Encode(), nil, &rows); err != nil {
				return gitLabMR{}, false, err
			}
			for _, row := range rows {
				if row.State != state || !g.matches(row, branch, target) {
					continue
				}
				if state == "merged" {
					delivered, err := g.delivered(branch, row.TargetBranch)
					if err != nil {
						return gitLabMR{}, false, err
					}
					if !delivered {
						continue
					}
				}
				return row, true, nil
			}
			if len(rows) < 100 {
				break
			}
			if page == 100 {
				return gitLabMR{}, false, fmt.Errorf("GitLab MR lookup exceeded 100 pages")
			}
		}
	}
	return gitLabMR{}, false, nil
}
func (m gitLabMR) delivery(project string) domain.DeliveryMR {
	return domain.DeliveryMR{Project: project, IID: m.IID, URL: m.WebURL, State: m.State, GongfengState: m.State, TargetBranch: m.TargetBranch}
}
func (s *Service) gitLabFindBranch(project, branch string) (domain.DeliveryMR, bool, error) {
	g, err := s.gitLabClient(project)
	if err != nil {
		return domain.DeliveryMR{}, false, err
	}
	m, ok, err := g.find(branch, "")
	return m.delivery(project), ok, err
}
func (s *Service) gitLabMergeBranch(project, branch, target, title, description string) (string, error) {
	if strings.TrimSpace(branch) == "" {
		return "", fmt.Errorf("source branch is required")
	}
	if branch == target {
		return "", nil
	}
	g, err := s.gitLabClient(project)
	if err != nil {
		return "", err
	}
	m, found, err := g.find(branch, target)
	if err != nil {
		return "", err
	}
	if found && m.State == "merged" {
		return "!" + strconv.Itoa(m.IID), nil
	}
	if !found || m.State == "closed" {
		if target == "" {
			return "", ErrNoOpenMR
		}
		if title == "" {
			title = "LoopForge: " + branch + " → " + target
		}
		if err := g.request("POST", "/merge_requests", map[string]any{"source_branch": branch, "target_branch": target, "title": title, "description": description}, &m); err != nil {
			return "", err
		}
	}
	if !g.matches(m, branch, target) || m.State != "opened" {
		return "", fmt.Errorf("GitLab did not confirm the requested merge request identity")
	}
	if strings.TrimSpace(m.SHA) == "" {
		return "", fmt.Errorf("GitLab 未返回源分支提交，拒绝合并")
	}
	var merged gitLabMR
	if err := g.request("PUT", "/merge_requests/"+strconv.Itoa(m.IID)+"/merge", map[string]any{"sha": m.SHA, "should_remove_source_branch": false}, &merged); err != nil {
		return "", err
	}
	if merged.State != "merged" || merged.IID != m.IID || !g.matches(merged, branch, m.TargetBranch) || merged.SHA != m.SHA {
		return "", fmt.Errorf("GitLab 未确认合并 !%d", m.IID)
	}
	return "!" + strconv.Itoa(m.IID), nil
}
func (s *Service) refreshGitLabDelivery(store MRDeliveryStore, project string, number int) error {
	g, err := s.gitLabClient(project)
	if err != nil {
		return err
	}
	var m gitLabMR
	if err := g.request("GET", "/merge_requests/"+strconv.Itoa(number), nil, &m); err != nil {
		return err
	}
	if err := g.resolveProject(); err != nil {
		return err
	}
	if m.IID != number || m.SourceBranch == "" || !g.matches(m, m.SourceBranch, m.TargetBranch) {
		return fmt.Errorf("GitLab did not confirm the requested merge request identity")
	}
	var mergedAt time.Time
	if m.MergedAt != nil {
		mergedAt = m.MergedAt.UTC()
	}
	return store.UpdateMRDeliveryState(project, number, m.State, m.TargetBranch, mergedAt, time.Now().UTC())
}

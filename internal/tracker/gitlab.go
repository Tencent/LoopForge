package tracker

import (
	"agentflow/internal/domain"
	"bytes"
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
}
type gitLabMR struct {
	IID          int    `json:"iid"`
	State        string `json:"state"`
	WebURL       string `json:"web_url"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	SHA          string `json:"sha"`
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
	req, err := http.NewRequest(method, g.base+"/api/v4/projects/"+url.PathEscape(g.path)+suffix, body)
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
func (g *gitLabClient) find(branch, target string) (gitLabMR, bool, error) {
	q := url.Values{"source_branch": {branch}, "per_page": {"100"}, "order_by": {"updated_at"}, "sort": {"desc"}}
	if target != "" {
		q.Set("target_branch", target)
	}
	var rows []gitLabMR
	if err := g.request("GET", "/merge_requests?"+q.Encode(), nil, &rows); err != nil {
		return gitLabMR{}, false, err
	}
	for _, row := range rows {
		if row.SourceBranch == branch && (target == "" || row.TargetBranch == target) {
			return row, true, nil
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
	if strings.TrimSpace(m.SHA) == "" {
		return "", fmt.Errorf("GitLab 未返回源分支提交，拒绝合并")
	}
	var merged gitLabMR
	if err := g.request("PUT", "/merge_requests/"+strconv.Itoa(m.IID)+"/merge", map[string]any{"sha": m.SHA, "should_remove_source_branch": false}, &merged); err != nil {
		return "", err
	}
	if merged.State != "merged" {
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
	return store.UpdateMRDeliveryState(project, number, m.State, m.TargetBranch, time.Now().UTC())
}

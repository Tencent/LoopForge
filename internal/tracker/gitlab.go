package tracker

import (
	"bytes"
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

// gitlabClient 调自托管 GitLab（GitLab CE/EE 兼容，14.10+）。
// 一个客户端对应一个 "gitlab:<host>/<group>/<repo>" 标识；多实例由 host 区分。
type gitlabClient struct {
	base    string // 站点根，如 http://172.32.149.204
	project string // URL 编码后的项目路径，如 jsrd_chatai%2Fweknora
	token   string
	http    *http.Client
}

// gitlabProject 拆分 "gitlab:<host>/<group>/<repo>" 标识。
func gitlabProjectParts(id string) (host, projectPath string, ok bool) {
	rest, found := strings.CutPrefix(id, "gitlab:")
	if !found {
		return "", "", false
	}
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return "", "", false
	}
	host, projectPath = rest[:slash], strings.TrimSuffix(rest[slash+1:], ".git")
	if host == "" || projectPath == "" {
		return "", "", false
	}
	return host, projectPath, true
}

func (s *Service) gitlabClient(project string) (*gitlabClient, error) {
	host, projectPath, ok := gitlabProjectParts(project)
	if !ok {
		return nil, fmt.Errorf("invalid GitLab repository")
	}
	var token string
	if s.projectAuth != nil {
		for _, repo := range s.projectAuth.Repositories() {
			if repo.Provider != "gitlab" || repo.GongfengProjectID != project {
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
	return &gitlabClient{
		base:    "http://" + host,
		project: url.PathEscape(projectPath),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (g *gitlabClient) request(method, suffix string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequest(method, g.base+"/api/v4/projects/"+g.project+suffix, body)
	if err != nil {
		return err
	}
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Content-Type", "application/json")
	// 自托管 GitLab 接受 PRIVATE-TOKEN（我们在 dsh-autoflow 中验证过）。
	if g.token != "" {
		r.Header.Set("PRIVATE-TOKEN", g.token)
	}
	res, err := g.http.Do(r)
	if err != nil {
		return fmt.Errorf("GitLab request failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("GitLab returned HTTP %d; check the project owner's Git token and repository permissions: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	if output == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(output)
}

type gitlabMergeRequest struct {
	IID          int    `json:"iid"`
	WebURL       string `json:"web_url"`
	State        string `json:"state"`
	MergedAt     string `json:"merged_at,omitempty"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
}

func (m gitlabMergeRequest) delivery(project string) domain.DeliveryMR {
	state := "opened"
	if m.State == "merged" {
		state = "merged"
	} else if m.State == "closed" {
		state = "closed"
	}
	return domain.DeliveryMR{
		RepositoryID: project,
		Project:      project,
		IID:          m.IID,
		URL:          m.WebURL,
		State:        state,
		TargetBranch: m.TargetBranch,
	}
}

func (m gitlabMergeRequest) mergedAt() time.Time {
	if m.MergedAt == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, m.MergedAt)
	return t
}

func (g *gitlabClient) findMR(branch, target string) (gitlabMergeRequest, bool, error) {
	q := url.Values{"state": {"all"}, "per_page": {"100"}, "order_by": {"updated_at"}, "sort": {"desc"}}
	var list []gitlabMergeRequest
	if err := g.request("GET", "/merge_requests?"+q.Encode(), nil, &list); err != nil {
		return gitlabMergeRequest{}, false, err
	}
	for _, m := range list {
		if m.SourceBranch == branch && (target == "" || m.TargetBranch == target) {
			return m, true, nil
		}
	}
	return gitlabMergeRequest{}, false, nil
}

// gitlabFindBranch 解析 source 分支当前的 MR（用于状态同步）。
func (s *Service) gitlabFindBranch(project, branch string) (domain.DeliveryMR, bool, error) {
	g, err := s.gitlabClient(project)
	if err != nil {
		return domain.DeliveryMR{}, false, err
	}
	m, ok, err := g.findMR(branch, "")
	if err != nil || !ok {
		return domain.DeliveryMR{}, ok, err
	}
	return m.delivery(project), true, nil
}

// gitlabMergeBranch 找到或创建 source→target 的 MR 并合并（对齐 githubMergeBranch 语义）。
// 与上游不同：这里只创建/复用 MR，合并交由人工在 GitLab 评审后执行——质量门已保证
// 测试通过，但团队保留 merge 按钮作为最终把关（dsh-autoflow 也是开 MR 不自动合并）。
func (s *Service) gitlabMergeBranch(project, branch, target, title, description string) (string, error) {
	if branch == "" {
		return "", fmt.Errorf("source branch is required")
	}
	if branch == target {
		return "", nil
	}
	g, err := s.gitlabClient(project)
	if err != nil {
		return "", err
	}
	m, found, err := g.findMR(branch, target)
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
		var created gitlabMergeRequest
		if err := g.request("POST", "/merge_requests", map[string]any{
			"source_branch": branch,
			"target_branch": target,
			"title":         title,
			"description":   description,
		}, &created); err != nil {
			// 并发下另一个实例可能已创建：再查一次，复用胜者。
			if m2, ok2, _ := g.findMR(branch, target); ok2 {
				return "!" + strconv.Itoa(m2.IID), nil
			}
			return "", err
		}
		m = created
	}
	return "!" + strconv.Itoa(m.IID), nil
}

// refreshGitLabDelivery 拉取单个 MR 的实时状态，供 MR 状态同步调用。
func (s *Service) refreshGitLabDelivery(store MRDeliveryStore, project string, iid int) error {
	g, err := s.gitlabClient(project)
	if err != nil {
		return err
	}
	var m gitlabMergeRequest
	if err := g.request("GET", "/merge_requests/"+strconv.Itoa(iid), nil, &m); err != nil {
		return err
	}
	d := m.delivery(project)
	var mergedAt time.Time
	if m.MergedAt != "" {
		mergedAt, _ = time.Parse(time.RFC3339, m.MergedAt)
	}
	return store.UpdateMRDeliveryState(project, iid, d.State, d.TargetBranch, mergedAt, time.Now().UTC())
}

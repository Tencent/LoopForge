/**
 * 自托管 GitLab（GitLab CE/EE 兼容，14.10+）的 MR 交付适配器。
 * 一个客户端对应一个 "gitlab:<host>/<group>/<repo>" 标识，多实例由 host 区分；
 * 认证复用平台的每主机 HTTPS Git 凭据（GitHTTPCredentialForHost），
 * 凭据只发往管理员在 AGENTFLOW_GITLAB_ALLOWED_HOSTS 中显式声明的实例。
 * --- @ty.aicoding@1795418206570 ---
 */
package tracker

import (
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

	"agentflow/internal/domain"
	"agentflow/internal/gitegress"
)

type gitlabClient struct {
	id      string // "gitlab:<host>/<group>/<repo>" 标识，用于拼交付链接
	base    string // 站点根，如 http://gitlab.example.test
	project string // URL 编码后的项目路径，如 group%2Frepository
	token   string
	http    *http.Client
}

// gitlabProjectParts 拆分 "gitlab:<host>/<group>/<repo>" 标识。
// --- @ty.aicoding@1795418206570 ---
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

// gitlabClient 按仓库绑定记录解析项目 Owner 的每主机 Git 凭据并构造客户端。
// 凭据只发往管理员显式声明的实例（AGENTFLOW_GITLAB_ALLOWED_HOSTS）：成员只要
// 能写 POST /api/repositories 就能绑定任意 gitlab:<host>，若按"前缀即可信"
// 放行，Owner 的 Git 令牌会被当 PRIVATE-TOKEN 发到攻击者主机。未声明则直接
// 拒绝构造客户端，不携带任何凭据。
// --- @ty.aicoding@1795418765976 ---
func (s *Service) gitlabClient(project string) (*gitlabClient, error) {
	host, projectPath, ok := gitlabProjectParts(project)
	if !ok {
		return nil, fmt.Errorf("invalid GitLab repository")
	}
	if !domain.GitLabHostAllowed(host) {
		return nil, fmt.Errorf("GitLab 实例 %s 未在管理员允许列表（%s）中：已拒绝发送凭据，请让管理员在部署配置中声明该实例", host, domain.GitLabAllowedHostsEnv)
	}
	var token string
	var siteURLs []string
	if s.projectAuth != nil {
		for _, repo := range s.projectAuth.Repositories() {
			if repo.Provider != "gitlab" || repo.GongfengProjectID != project {
				continue
			}
			if p, found := s.projectAuth.Project(repo.ProjectID); found {
				if credentialStore, ok := s.projectAuth.(GitHTTPCredentialStore); ok {
					if cred, found := credentialStore.GitHTTPCredentialForHost(context.Background(), p.OwnerID, host); found && cred.Token != "" {
						if cred.Platform != domain.GitPlatformGitLab {
							return nil, fmt.Errorf("主机 %s 的凭据平台是 %q，不是 GitLab", host, cred.Platform)
						}
						token = cred.Token
					}
				}
			}
			// 实例 scheme 以仓库记录的 URL 为准（https 默认），不在 ID 里硬编码。
			siteURLs = []string{repo.WebURL, repo.CloneURL}
			break
		}
	}
	base := domain.GitLabInstanceScheme(host, siteURLs...) + "://" + host
	// gitegress 客户端自带出网地址策略与"不跟随重定向"；测试注入
	// gitHostAPIBase 时只替换目标地址，保留重定向拒绝语义，避免
	// PRIVATE-TOKEN 在 302 场景被带到别的 origin。
	client := gitegress.Client()
	if s.gitHostAPIBase != "" {
		base = s.gitHostAPIBase
		client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &gitlabClient{
		id:      project,
		base:    base,
		project: url.PathEscape(projectPath),
		token:   token,
		// 与 GitHub client 一致：不跟随重定向。PRIVATE-TOKEN 是凭证，
		// 302 到别的 origin 绝不能把 token 带过去。
		http: client,
	}, nil
}

// request 发起一次 GitLab v4 API 调用，输入输出均为 JSON。
// --- @ty.aicoding@1795418206570 ---
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
	// 自托管 GitLab（CE/EE）以 PRIVATE-TOKEN 头认证（v4 REST 标准方式）。
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
	SHA          string `json:"sha,omitempty"`
	DiffRefs     struct {
		HeadSHA string `json:"head_sha"`
	} `json:"diff_refs,omitempty"`
}

// headSHA 是 MR 源分支的当前提交；列表响应缺 sha 时退回 diff_refs。
// --- @ty.aicoding@1795419744565 ---
func (m gitlabMergeRequest) headSHA() string {
	if sha := strings.TrimSpace(m.SHA); sha != "" {
		return sha
	}
	return strings.TrimSpace(m.DiffRefs.HeadSHA)
}

// delivery 把 GitLab MR 记录转成交付领域对象，URL 走统一的标识反解规则。
// --- @ty.aicoding@1795418206570 ---
func (m gitlabMergeRequest) delivery(project string) domain.DeliveryMR {
	state := "opened"
	if m.State == "merged" {
		state = "merged"
	} else if m.State == "closed" || m.State == "locked" {
		state = "closed"
	}
	return domain.DeliveryMR{
		Project:       project,
		IID:           m.IID,
		URL:           domain.GongfengMRURL(project, m.IID),
		State:         state,
		GongfengState: state,
		TargetBranch:  m.TargetBranch,
	}
}

// mergedAt 解析 GitLab 的 RFC3339 合并时间，零值表示尚未合并。
// --- @ty.aicoding@1795418206570 ---
func (m gitlabMergeRequest) mergedAt() time.Time {
	if m.MergedAt == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, m.MergedAt)
	return t
}

// findMR 列出项目 MR 并匹配 source 分支（给定时还要匹配 target）。
// --- @ty.aicoding@1795418206570 ---
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

// listMRs 返回 source→target 的全部 MR（对齐 github listPulls 语义），
// 供存在性/合并状态判断保留多条记录。
// --- @ty.aicoding@1795419744565 ---
func (g *gitlabClient) listMRs(source, target string) ([]domain.DeliveryMR, error) {
	q := url.Values{"state": {"all"}, "per_page": {"100"}, "order_by": {"updated_at"}, "sort": {"desc"}}
	if source != "" {
		q.Set("source_branch", source)
	}
	if target != "" {
		q.Set("target_branch", target)
	}
	var list []gitlabMergeRequest
	if err := g.request("GET", "/merge_requests?"+q.Encode(), nil, &list); err != nil {
		return nil, err
	}
	out := make([]domain.DeliveryMR, 0, len(list))
	for _, m := range list {
		mr := m.delivery(g.id)
		mr.HeadSHA = m.headSHA()
		out = append(out, mr)
	}
	return out, nil
}

// gitlabFindBranch 解析 source 分支当前的 MR（用于交付物反查）。
// --- @ty.aicoding@1795418206570 ---
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

// gitlabMergeBranch 找到或创建 source→target 的 MR 并真正合并（对齐
// githubMergeBranch 语义）：调用 merge 端点后校验返回的 MR 状态必须是
// merged，否则报错——未合并绝不能返回成功，否则交付会虚进。
// --- @ty.aicoding@1795418206570 ---
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
			// 并发下另一个实例可能已创建：再查一次，复用胜者继续走合并。
			rematch, ok2, err2 := g.findMR(branch, target)
			if err2 != nil || !ok2 {
				return "", err
			}
			m = rematch
		} else {
			m = created
		}
	}
	var merged gitlabMergeRequest
	if err := g.request("PUT", "/merge_requests/"+strconv.Itoa(m.IID)+"/merge", map[string]any{"merge_method": "merge"}, &merged); err != nil {
		return "", err
	}
	if merged.State != "merged" {
		return "", fmt.Errorf("GitLab did not merge merge request !%d (state %q)", m.IID, merged.State)
	}
	return "!" + strconv.Itoa(m.IID), nil
}

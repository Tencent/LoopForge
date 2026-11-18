package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// turnDelivery 是一次 turn 结束后发现、需要登记为需求交付的 MR。
// 同时服务 GitHub PR 与 GitLab MR，Project/IID 由发现方直接给出，
// 不再依赖从 URL 反解。
type turnDelivery struct {
	URL          string
	Title        string
	Body         string
	TargetBranch string
	Project      string // github:<owner>/<repo> 或 gitlab:<host>/<group>/<repo>
	IID          int
}

// workspaceGitLabMR 是 GitLab v4 MR 中本流程用到的字段。
type workspaceGitLabMR struct {
	IID          int        `json:"iid"`
	WebURL       string     `json:"web_url"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	State        string     `json:"state"`
	MergedAt     *time.Time `json:"merged_at"`
	CreatedAt    time.Time  `json:"created_at"`
	SHA          string     `json:"sha"`
	TargetBranch string     `json:"target_branch"`
	DiffRefs     struct {
		HeadSHA string `json:"head_sha"`
	} `json:"diff_refs"`
}

// gitlabTurnMRs 列出某个 GitLab 项目里 token 用户本人创建、head 仍在工作区、
// 且在 workspace 创建之后打开/合并的 MR。实例必须由管理员显式声明
// （AGENTFLOW_GITLAB_ALLOWED_HOSTS），否则在发请求前就拒绝——凭据绝不发往
// 未声明的 host。
func gitlabTurnMRs(ctx context.Context, client *http.Client, token, project, base string, heads map[string]bool, since time.Time) ([]turnDelivery, error) {
	host, ok := domain.GitLabInstanceHost(project)
	if !ok {
		return nil, fmt.Errorf("无效的 GitLab 仓库标识 %q", project)
	}
	if !domain.GitLabHostAllowed(host) {
		return nil, fmt.Errorf("GitLab 实例 %s 未在管理员允许列表（%s）中：已拒绝发送凭据", host, domain.GitLabAllowedHostsEnv)
	}
	rest := strings.TrimPrefix(project, "gitlab:")
	path := rest[strings.Index(rest, "/")+1:]
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("无效的 GitLab 仓库标识 %q", project)
	}
	apiURL := base + "/api/v4/projects/" + url.PathEscape(path) +
		"/merge_requests?state=all&scope=created_by_me&order_by=created_at&sort=desc&per_page=" + strconv.Itoa(turnDeliveryPullsMax)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接 GitLab %s：%v", base, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, fmt.Errorf("GitLab 返回 HTTP %d：%s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	var list []workspaceGitLabMR
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&list); err != nil {
		return nil, err
	}
	out := []turnDelivery{}
	for _, mr := range list {
		if mr.IID <= 0 || strings.TrimSpace(mr.WebURL) == "" {
			continue
		}
		if mr.CreatedAt.Before(since) {
			continue
		}
		if mr.State != "opened" && mr.State != "merged" {
			continue
		}
		head := strings.TrimSpace(mr.SHA)
		if !heads[head] {
			head = strings.TrimSpace(mr.DiffRefs.HeadSHA)
			if !heads[head] {
				continue
			}
		}
		out = append(out, turnDelivery{
			URL:          mr.WebURL,
			Title:        mr.Title,
			Body:         mr.Description,
			TargetBranch: mr.TargetBranch,
			Project:      project,
			IID:          mr.IID,
		})
		if len(out) >= turnDeliveryPullsMax {
			break
		}
	}
	return out, nil
}

// gitlabWorkspaceDeliveries 解析工作区里的 GitLab 远端，逐个已声明实例查询
// 本 turn 的 MR。实例 scheme 以仓库记录为准（默认 https）。
func (h *assistantHandlers) gitlabWorkspaceDeliveries(ctx context.Context, token string, run domain.Run, projects []string, heads map[string]bool) []turnDelivery {
	if len(projects) == 0 {
		return nil
	}
	repos := h.repo.Repositories()
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	out := []turnDelivery{}
	for _, project := range projects {
		host, ok := domain.GitLabInstanceHost(project)
		if !ok || !domain.GitLabHostAllowed(host) {
			continue
		}
		base := domain.GitLabInstanceScheme(host, repositoryURLsForProject(repos, project)...) + "://" + host
		mrs, err := gitlabTurnMRs(ctx, client, token, project, base, heads, run.CreatedAt)
		if err != nil {
			log.Printf("find conversation workspace %s GitLab merge requests: %v", run.ID, err)
			continue
		}
		out = append(out, mrs...)
		if len(out) >= turnDeliveryPullsMax {
			break
		}
	}
	return out
}

func repositoryURLsForProject(repos []domain.Repository, project string) []string {
	urls := []string{}
	for _, r := range repos {
		if r.GongfengProjectID == project {
			urls = append(urls, r.WebURL, r.CloneURL)
		}
	}
	return urls
}

// turnDeliveryLabel 是回复里给用户看的 MR 标签。
func turnDeliveryLabel(d turnDelivery) string {
	if strings.HasPrefix(d.Project, "gitlab:") {
		return fmt.Sprintf("MR !%d", d.IID)
	}
	return fmt.Sprintf("PR #%d", d.IID)
}

// gitlabBoundProjects 把工作区远端映射到本项目已绑定的 gitlab: 仓库。
// 远端可能带 token（调用方已剥离）、.git 后缀或 ssh 形态，统一按
// host+仓库路径比较；只有已绑定且随后通过 allowlist 校验的实例才会被查询。
func gitlabBoundProjects(remotes []string, repos []domain.Repository, projectID string) []string {
	out := []string{}
	for _, repo := range repos {
		project := strings.TrimSpace(repo.GongfengProjectID)
		if !strings.HasPrefix(project, "gitlab:") {
			continue
		}
		if projectID != "" && repo.ProjectID != projectID {
			continue
		}
		for _, remote := range remotes {
			if sameGitRemote(remote, repo.CloneURL) || sameGitRemote(remote, repo.WebURL) {
				if !slices.Contains(out, project) && len(out) < turnDeliveryRepoMax {
					out = append(out, project)
				}
				break
			}
		}
	}
	return out
}

func sameGitRemote(a, b string) bool {
	ah, ap, ok := splitGitRemote(a)
	if !ok {
		return false
	}
	bh, bp, ok := splitGitRemote(b)
	if !ok {
		return false
	}
	return strings.EqualFold(ah, bh) && strings.EqualFold(ap, bp)
}

// splitGitRemote 归一化 https / ssh / scp 形态的仓库地址为 host + 仓库路径。
func splitGitRemote(raw string) (host, path string, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", false
	}
	if !strings.Contains(s, "://") {
		if at := strings.Index(s, "@"); at >= 0 {
			rest := s[at+1:]
			if colon := strings.Index(rest, ":"); colon > 0 {
				host = strings.ToLower(rest[:colon])
				path = strings.TrimSuffix(strings.Trim(rest[colon+1:], "/"), ".git")
				return host, path, host != "" && path != ""
			}
		}
		return "", "", false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	path = strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if path == "" {
		return "", "", false
	}
	return strings.ToLower(u.Host), path, true
}

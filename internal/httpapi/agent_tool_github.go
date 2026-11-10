package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// Main Bot 的轻量 GitHub 工具：列 Issue/PR、查 PR 状态、加评论、加标签。
//
// 设计目标是不要求先 workbench_workspace_open 打开工作区（那条路径是为
// "真的要改代码、推分支、开 PR" 准备的重路径），同时仍然复用
// workbench_workspace_submit_mr 已验证的安全模型：服务端用请求人自己配置的
// GitHub Git Token 直连 GitHub，token 只在服务端出现，不进模型上下文、不落库。
//
// 这组工具的 Kind 是 "github"（不是 "agentflow" 或 "workspace"），在
// agentToolGateway.dispatch 里直接执行，不经过平台写入工具的审批模式
// （AutoWrite/ReviewWrites）校验——和 workbench_workspace_submit_mr 的处理方式
// 一致。这是刻意的取舍：操作范围被限定在当前项目已绑定的 GitHub 仓库，且
// 必须是请求人自己配置并校验过的 Git Token，风险和"直接执行"的其他工作区工具
// 相当，所以不再额外要求逐项确认。
const (
	mainBotGitHubIssuesList = "workbench_github_issues_list"
	mainBotGitHubPullsList  = "workbench_github_pulls_list"
	mainBotGitHubPRStatus   = "workbench_github_pr_status"
	mainBotGitHubCommentAdd = "workbench_github_comment_add"
	mainBotGitHubLabelsAdd  = "workbench_github_labels_add"
)

// mainBotGitHubTools 只在普通 Main Bot 会话（AgentToolAssistant）且已知项目时
// 暴露：这组工具按项目绑定的仓库解析 repository 参数，脱离项目没有意义。
func mainBotGitHubTools(s *AgentToolSession) []agentToolDescriptor {
	if s == nil || s.Mode != AgentToolAssistant || strings.TrimSpace(s.ProjectID) == "" {
		return nil
	}
	const repoProp = `"repository":{"type":"string","description":"项目里已绑定的仓库名（不是 owner/repo；参见 workbench_workspace_open 的仓库列表），必须是 GitHub 仓库"}`
	const numberProp = `"number":{"type":"integer","minimum":1,"description":"Issue 或 PR 编号"}`
	return []agentToolDescriptor{
		{
			Name:        mainBotGitHubIssuesList,
			Description: "列出项目绑定的 GitHub 仓库里的 Issue（不包含 PR）。只读，不需要打开工作区，用请求人自己的 Git Token 直连 GitHub。",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` + repoProp + `,"state":{"type":"string","enum":["open","closed","all"],"description":"默认 open"},"labels":{"type":"string","description":"按标签过滤，逗号分隔的标签名"},"limit":{"type":"integer","minimum":1,"maximum":50,"description":"默认 20"}},"required":["repository"],"additionalProperties":false}`),
			Kind:        "github",
		},
		{
			Name:        mainBotGitHubPullsList,
			Description: "列出项目绑定的 GitHub 仓库里的 Pull Request。只读，不需要打开工作区，用请求人自己的 Git Token 直连 GitHub。",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` + repoProp + `,"state":{"type":"string","enum":["open","closed","all"],"description":"默认 open"},"limit":{"type":"integer","minimum":1,"maximum":50,"description":"默认 20"}},"required":["repository"],"additionalProperties":false}`),
			Kind:        "github",
		},
		{
			Name:        mainBotGitHubPRStatus,
			Description: "查看一个 Pull Request 的详情（状态、是否可合并）和检查结果（CI checks / commit status）。只读，不需要打开工作区。",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` + repoProp + `,` + numberProp + `},"required":["repository","number"],"additionalProperties":false}`),
			Kind:        "github",
		},
		{
			Name:        mainBotGitHubCommentAdd,
			Description: "给一个 Issue 或 PR 加评论。用请求人自己的 Git Token 直接提交，不经过草稿确认；调用前请把内容和目标 number 向用户确认清楚。",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` + repoProp + `,` + numberProp + `,"body":{"type":"string","minLength":1,"maxLength":10000}},"required":["repository","number","body"],"additionalProperties":false}`),
			Kind:        "github",
		},
		{
			Name:        mainBotGitHubLabelsAdd,
			Description: "给一个 Issue 或 PR 加标签（标签必须在仓库里已存在，GitHub 不会自动创建）。用请求人自己的 Git Token 直接提交，不经过草稿确认。",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` + repoProp + `,` + numberProp + `,"labels":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":20}},"required":["repository","number","labels"],"additionalProperties":false}`),
			Kind:        "github",
		},
	}
}

// callMainBotGitHub 是这组工具的统一入口：解析请求人的 Git Token 和项目里的
// 目标仓库，再按工具名分发到具体操作。错误和结果都经过 redactAgentToolResult
// 过滤，避免 token 意外出现在模型可见的文本里。
func (h *agentToolGateway) callMainBotGitHub(ctx context.Context, session *AgentToolSession, name string, args map[string]any) (string, error) {
	requester, ok := h.repo.User(session.User.ID)
	if !ok {
		return "", errAgentToolForbidden
	}
	token, err := h.githubUserToken(ctx, session.User.ID)
	if err != nil {
		return "", err
	}
	repository, _ := args["repository"].(string)
	path, err := h.resolveProjectGitHubRepo(session.ProjectID, repository)
	if err != nil {
		return "", err
	}
	var result string
	switch name {
	case mainBotGitHubIssuesList:
		result, err = githubIssuesList(ctx, token, path, args)
	case mainBotGitHubPullsList:
		result, err = githubPullsList(ctx, token, path, args)
	case mainBotGitHubPRStatus:
		result, err = githubPRStatus(ctx, token, path, args)
	case mainBotGitHubCommentAdd:
		result, err = githubIssueCommentAdd(ctx, token, path, args)
	case mainBotGitHubLabelsAdd:
		result, err = githubIssueLabelsAdd(ctx, token, path, args)
	default:
		return "", errAgentToolUnknown
	}
	if err != nil {
		return "", errors.New(redactAgentToolResult(err.Error(), requester))
	}
	return redactAgentToolResult(result, requester), nil
}

// githubUserToken 复用 workbench_workspace_submit_mr 的令牌来源：请求人在
// 「个人配置 → 个人凭证」里保存的 Git Token。未配置时给出同样的引导文案。
func (h *agentToolGateway) githubUserToken(ctx context.Context, userID int64) (string, error) {
	raw := strings.TrimSpace(h.repo.GitCredential(ctx, userID))
	_, token := gitwork.SplitToken(raw)
	if token == "" {
		return "", fmt.Errorf("你还没有配置 GitHub Git Token，无法操作 GitHub。请到「个人配置 → 个人凭证」配置：打开 https://github.com/settings/tokens 新建令牌并授予目标仓库的权限，复制后粘贴并点校验")
	}
	return token, nil
}

// resolveProjectGitHubRepo 只允许操作当前项目已绑定、且是 GitHub 的仓库：
// repository 填的是项目里的仓库名，不是裸 owner/repo，防止模型用这组工具
// 操作项目以外、碰巧也在同一个 Git Token 权限范围内的仓库。
func (h *agentToolGateway) resolveProjectGitHubRepo(projectID, repository string) (string, error) {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return "", fmt.Errorf("repository is required：请填项目里已绑定的仓库名")
	}
	var names []string
	for _, r := range h.repo.Repositories() {
		if r.ProjectID != projectID {
			continue
		}
		names = append(names, r.Name)
		if !strings.EqualFold(r.Name, repository) && r.ID != repository {
			continue
		}
		path := githubRepoPath(domain.RepositoryContext{CloneURL: r.CloneURL, WebURL: r.WebURL, GongfengProjectID: r.GongfengProjectID})
		if path == "" {
			return "", fmt.Errorf("仓库 %s 不是 GitHub 仓库，这组工具目前只支持 GitHub", r.Name)
		}
		return path, nil
	}
	if len(names) == 0 {
		return "", fmt.Errorf("当前项目还没有绑定仓库")
	}
	return "", fmt.Errorf("找不到仓库 %q，当前项目已绑定：%s", repository, strings.Join(names, "、"))
}

func githubArgInt(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n
		}
	}
	return def
}

func githubArgState(args map[string]any) (string, error) {
	state := "open"
	if raw, ok := args["state"].(string); ok && strings.TrimSpace(raw) != "" {
		state = strings.ToLower(strings.TrimSpace(raw))
	}
	if state != "open" && state != "closed" && state != "all" {
		return "", fmt.Errorf("state 只能是 open、closed 或 all")
	}
	return state, nil
}

func githubArgLimit(args map[string]any) int {
	limit := githubArgInt(args, "limit", 20)
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	return limit
}

func githubHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}

type githubIssueOut struct {
	Number  int64    `json:"number"`
	Title   string   `json:"title"`
	State   string   `json:"state"`
	URL     string   `json:"url"`
	Author  string   `json:"author"`
	Labels  []string `json:"labels,omitempty"`
	Created string   `json:"createdAt"`
	Updated string   `json:"updatedAt"`
}

// githubIssuesList 列 Issue。GitHub 的 /issues 端点也会混入 PR（PR 也是一种
// Issue），这里按返回体里是否带 pull_request 字段过滤掉，只留纯 Issue。
func githubIssuesList(ctx context.Context, token, path string, args map[string]any) (string, error) {
	state, err := githubArgState(args)
	if err != nil {
		return "", err
	}
	q := url.Values{"state": {state}, "per_page": {strconv.Itoa(githubArgLimit(args))}}
	if labels, _ := args["labels"].(string); strings.TrimSpace(labels) != "" {
		q.Set("labels", strings.TrimSpace(labels))
	}
	client := githubHTTPClient()
	resp, raw, err := githubRequest(ctx, client, http.MethodGet, githubAPIBase+"/repos/"+path+"/issues?"+q.Encode(), token, nil)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", githubPRError(resp.StatusCode, raw)
	}
	var items []struct {
		Number  int64  `json:"number"`
		Title   string `json:"title"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		CreatedAt   string          `json:"created_at"`
		UpdatedAt   string          `json:"updated_at"`
		PullRequest json.RawMessage `json:"pull_request"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return "", fmt.Errorf("GitHub 返回的 Issue 列表无法解析")
	}
	out := make([]githubIssueOut, 0, len(items))
	for _, it := range items {
		if len(it.PullRequest) > 0 {
			continue
		}
		labels := make([]string, 0, len(it.Labels))
		for _, l := range it.Labels {
			labels = append(labels, l.Name)
		}
		out = append(out, githubIssueOut{Number: it.Number, Title: it.Title, State: it.State, URL: it.HTMLURL, Author: it.User.Login, Labels: labels, Created: it.CreatedAt, Updated: it.UpdatedAt})
	}
	body, _ := json.Marshal(map[string]any{"repository": path, "issues": out, "count": len(out)})
	return string(body), nil
}

type githubPullOut struct {
	Number  int64  `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	URL     string `json:"url"`
	Author  string `json:"author"`
	Head    string `json:"head"`
	Base    string `json:"base"`
	Created string `json:"createdAt"`
	Updated string `json:"updatedAt"`
}

func githubPullsList(ctx context.Context, token, path string, args map[string]any) (string, error) {
	state, err := githubArgState(args)
	if err != nil {
		return "", err
	}
	q := url.Values{"state": {state}, "per_page": {strconv.Itoa(githubArgLimit(args))}}
	client := githubHTTPClient()
	resp, raw, err := githubRequest(ctx, client, http.MethodGet, githubAPIBase+"/repos/"+path+"/pulls?"+q.Encode(), token, nil)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", githubPRError(resp.StatusCode, raw)
	}
	var items []struct {
		Number  int64  `json:"number"`
		Title   string `json:"title"`
		State   string `json:"state"`
		Draft   bool   `json:"draft"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return "", fmt.Errorf("GitHub 返回的 PR 列表无法解析")
	}
	out := make([]githubPullOut, 0, len(items))
	for _, it := range items {
		out = append(out, githubPullOut{Number: it.Number, Title: it.Title, State: it.State, Draft: it.Draft, URL: it.HTMLURL, Author: it.User.Login, Head: it.Head.Ref, Base: it.Base.Ref, Created: it.CreatedAt, Updated: it.UpdatedAt})
	}
	body, _ := json.Marshal(map[string]any{"repository": path, "pulls": out, "count": len(out)})
	return string(body), nil
}

type githubCheckSummary struct {
	Name  string `json:"name"`
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}

// githubCombinedChecks 合并两套 GitHub 检查来源：旧的 commit status API（很多
// 第三方 CI 还在用）和新的 check-runs API（GitHub Actions）。任一来源失败就
// 整体标 failure；没有失败但有未完成的标 pending；都成功才是 success。
func githubCombinedChecks(ctx context.Context, client *http.Client, token, path, sha string) ([]githubCheckSummary, string) {
	if strings.TrimSpace(sha) == "" {
		return nil, "none"
	}
	var out []githubCheckSummary
	if resp, raw, err := githubRequest(ctx, client, http.MethodGet, fmt.Sprintf("%s/repos/%s/commits/%s/status", githubAPIBase, path, sha), token, nil); err == nil && resp.StatusCode == http.StatusOK {
		var combined struct {
			Statuses []struct {
				Context   string `json:"context"`
				State     string `json:"state"`
				TargetURL string `json:"target_url"`
			} `json:"statuses"`
		}
		if json.Unmarshal(raw, &combined) == nil {
			for _, s := range combined.Statuses {
				out = append(out, githubCheckSummary{Name: s.Context, State: s.State, URL: s.TargetURL})
			}
		}
	}
	if resp, raw, err := githubRequest(ctx, client, http.MethodGet, fmt.Sprintf("%s/repos/%s/commits/%s/check-runs", githubAPIBase, path, sha), token, nil); err == nil && resp.StatusCode == http.StatusOK {
		var runs struct {
			CheckRuns []struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				HTMLURL    string `json:"html_url"`
			} `json:"check_runs"`
		}
		if json.Unmarshal(raw, &runs) == nil {
			for _, r := range runs.CheckRuns {
				state := r.Status
				if strings.TrimSpace(r.Conclusion) != "" {
					state = r.Conclusion
				}
				out = append(out, githubCheckSummary{Name: r.Name, State: state, URL: r.HTMLURL})
			}
		}
	}
	overall := "none"
	if len(out) > 0 {
		overall = "success"
		for _, c := range out {
			switch strings.ToLower(c.State) {
			case "failure", "failed", "error", "cancelled", "timed_out":
				overall = "failure"
			case "pending", "queued", "in_progress", "":
				if overall != "failure" {
					overall = "pending"
				}
			}
		}
	}
	return out, overall
}

func githubPRStatus(ctx context.Context, token, path string, args map[string]any) (string, error) {
	number := githubArgInt(args, "number", 0)
	if number <= 0 {
		return "", fmt.Errorf("number is required and must be a positive integer")
	}
	client := githubHTTPClient()
	resp, raw, err := githubRequest(ctx, client, http.MethodGet, fmt.Sprintf("%s/repos/%s/pulls/%d", githubAPIBase, path, number), token, nil)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", githubPRError(resp.StatusCode, raw)
	}
	var pr struct {
		Number         int64  `json:"number"`
		Title          string `json:"title"`
		State          string `json:"state"`
		Draft          bool   `json:"draft"`
		Merged         bool   `json:"merged"`
		MergeableState string `json:"mergeable_state"`
		HTMLURL        string `json:"html_url"`
		Head           struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if json.Unmarshal(raw, &pr) != nil || pr.Number == 0 {
		return "", fmt.Errorf("GitHub 返回的 PR 信息无法解析")
	}
	checks, checksState := githubCombinedChecks(ctx, client, token, path, pr.Head.SHA)
	out, _ := json.Marshal(map[string]any{
		"repository":     path,
		"number":         pr.Number,
		"title":          pr.Title,
		"state":          pr.State,
		"draft":          pr.Draft,
		"merged":         pr.Merged,
		"mergeableState": pr.MergeableState,
		"url":            pr.HTMLURL,
		"head":           pr.Head.Ref,
		"base":           pr.Base.Ref,
		"checksState":    checksState,
		"checks":         checks,
	})
	return string(out), nil
}

func githubIssueCommentAdd(ctx context.Context, token, path string, args map[string]any) (string, error) {
	number := githubArgInt(args, "number", 0)
	if number <= 0 {
		return "", fmt.Errorf("number is required and must be a positive integer")
	}
	body, _ := args["body"].(string)
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("body is required")
	}
	if len([]rune(body)) > 10000 {
		return "", fmt.Errorf("body must be at most 10000 characters")
	}
	client := githubHTTPClient()
	payload, _ := json.Marshal(map[string]string{"body": body})
	resp, raw, err := githubRequest(ctx, client, http.MethodPost, fmt.Sprintf("%s/repos/%s/issues/%d/comments", githubAPIBase, path, number), token, payload)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return "", githubPRError(resp.StatusCode, raw)
	}
	var comment struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	_ = json.Unmarshal(raw, &comment)
	out, _ := json.Marshal(map[string]any{"repository": path, "number": number, "commentId": comment.ID, "url": comment.HTMLURL})
	return string(out), nil
}

func githubIssueLabelsAdd(ctx context.Context, token, path string, args map[string]any) (string, error) {
	number := githubArgInt(args, "number", 0)
	if number <= 0 {
		return "", fmt.Errorf("number is required and must be a positive integer")
	}
	raw, ok := args["labels"].([]any)
	if !ok || len(raw) == 0 {
		return "", fmt.Errorf("labels is required and must be a non-empty array of strings")
	}
	labels := make([]string, 0, len(raw))
	for _, v := range raw {
		s := strings.TrimSpace(fmt.Sprint(v))
		if s != "" {
			labels = append(labels, s)
		}
	}
	if len(labels) == 0 {
		return "", fmt.Errorf("labels is required and must be a non-empty array of strings")
	}
	client := githubHTTPClient()
	payload, _ := json.Marshal(map[string][]string{"labels": labels})
	resp, body, err := githubRequest(ctx, client, http.MethodPost, fmt.Sprintf("%s/repos/%s/issues/%d/labels", githubAPIBase, path, number), token, payload)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", githubPRError(resp.StatusCode, body)
	}
	var result []struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(body, &result)
	names := make([]string, 0, len(result))
	for _, l := range result {
		names = append(names, l.Name)
	}
	out, _ := json.Marshal(map[string]any{"repository": path, "number": number, "labels": names})
	return string(out), nil
}

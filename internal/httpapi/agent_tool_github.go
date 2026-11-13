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
// agentToolGateway.dispatch 里直接执行，不走平台写入工具的 drafts 确认流程，
// 和 workbench_workspace_* 一致。同样和 workspace 工具一致的是：会话开启
// 「帮我批准」（ExecMode.ReviewWrites）时，评论、加标签这两个写操作在调用
// GitHub 之前先交给独立审查器逐项审查——它们对外公开可见，而列表工具会把
// 不可信的 Issue/PR 标题带进上下文，正是最需要审查器把关的地方。
// 操作范围限定在当前项目已绑定的 GitHub 仓库，写操作只开放评论和加标签，
// 不提供关闭、合并、删除。
const (
	mainBotGitHubIssuesList = "workbench_github_issues_list"
	mainBotGitHubPullsList  = "workbench_github_pulls_list"
	mainBotGitHubPRStatus   = "workbench_github_pr_status"
	mainBotGitHubCommentAdd = "workbench_github_comment_add"
	mainBotGitHubLabelsAdd  = "workbench_github_labels_add"
)

const (
	// githubListPageSize 是 GitHub 列表接口单页上限。
	githubListPageSize = 100
	// githubIssuesMaxPages 限制 issues_list 为凑够 limit 最多翻的页数：
	// /issues 端点混有 PR，PR 多的仓库要往后翻才能凑够纯 Issue。
	githubIssuesMaxPages = 5
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
			Description: "列出项目绑定的 GitHub 仓库里的 Issue（不包含 PR）。只读，不需要打开工作区，用请求人自己的 Git Token 直连 GitHub。结果里 hasMore=true 表示还有符合条件但没列出的 Issue，不能把本次结果当成完整列表。",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` + repoProp + `,"state":{"type":"string","enum":["open","closed","all"],"description":"默认 open"},"labels":{"type":"string","description":"按标签过滤，逗号分隔的标签名"},"limit":{"type":"integer","minimum":1,"maximum":50,"description":"默认 20"}},"required":["repository"],"additionalProperties":false}`),
			Kind:        "github",
		},
		{
			Name:        mainBotGitHubPullsList,
			Description: "列出项目绑定的 GitHub 仓库里的 Pull Request。只读，不需要打开工作区，用请求人自己的 Git Token 直连 GitHub。结果里 hasMore=true 表示还有没列出的 PR。",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` + repoProp + `,"state":{"type":"string","enum":["open","closed","all"],"description":"默认 open"},"limit":{"type":"integer","minimum":1,"maximum":50,"description":"默认 20"}},"required":["repository"],"additionalProperties":false}`),
			Kind:        "github",
		},
		{
			Name:        mainBotGitHubPRStatus,
			Description: "查看一个 Pull Request 的详情（状态、是否可合并）和检查结果（CI checks / commit status）。只读，不需要打开工作区。checksState 为 unknown 时表示部分检查结果读取失败，原因见 checksWarnings。",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` + repoProp + `,` + numberProp + `},"required":["repository","number"],"additionalProperties":false}`),
			Kind:        "github",
		},
		{
			Name:        mainBotGitHubCommentAdd,
			Description: "给一个 Issue 或 PR 加评论，对外公开可见。用请求人自己的 Git Token 直接提交，不经过草稿确认（「帮我批准」模式下会先经自动审批）；调用前请把内容和目标 number 向用户确认清楚。",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` + repoProp + `,` + numberProp + `,"body":{"type":"string","minLength":1,"maxLength":10000}},"required":["repository","number","body"],"additionalProperties":false}`),
			Kind:        "github",
		},
		{
			Name:        mainBotGitHubLabelsAdd,
			Description: "给一个 Issue 或 PR 加标签（标签必须在仓库里已存在，GitHub 不会自动创建）。用请求人自己的 Git Token 直接提交，不经过草稿确认（「帮我批准」模式下会先经自动审批）。",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` + repoProp + `,` + numberProp + `,"labels":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":20}},"required":["repository","number","labels"],"additionalProperties":false}`),
			Kind:        "github",
		},
	}
}

// callMainBotGitHub 是这组工具的统一入口：解析请求人的 Git Token 和项目里的
// 目标仓库，再按工具名分发到具体操作。写操作先校验参数、再过审查、最后才
// 调 GitHub。错误和结果都经过 redactAgentToolResult 过滤，避免 token 意外
// 出现在模型可见的文本里。
func (h *agentToolGateway) callMainBotGitHub(ctx context.Context, session *AgentToolSession, name string, args map[string]any) (string, error) {
	if len(mainBotGitHubTools(session)) == 0 {
		return "", errAgentToolForbidden
	}
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
		number, body, verr := githubCommentArgs(args)
		if verr != nil {
			return "", verr
		}
		if rerr := reviewGitHubWrite(ctx, session, name, path, args); rerr != nil {
			return "", rerr
		}
		result, err = githubIssueCommentAdd(ctx, token, path, number, body)
	case mainBotGitHubLabelsAdd:
		number, labels, verr := githubLabelsArgs(args)
		if verr != nil {
			return "", verr
		}
		if rerr := reviewGitHubWrite(ctx, session, name, path, args); rerr != nil {
			return "", rerr
		}
		result, err = githubIssueLabelsAdd(ctx, token, path, number, labels)
	default:
		return "", errAgentToolUnknown
	}
	if err != nil {
		return "", errors.New(redactAgentToolResult(err.Error(), requester))
	}
	return redactAgentToolResult(result, requester), nil
}

// reviewGitHubWrite 和 callMainBotWorkspace 对写操作的处理一致：会话开启
// 「帮我批准」时，先把这一项具体操作交给独立审查器，未获批准不调用 GitHub。
// 没有可用的审查器时 reviewAssistantAction 返回 unavailable，同样拒绝执行。
func reviewGitHubWrite(ctx context.Context, s *AgentToolSession, name, path string, args map[string]any) error {
	if !s.ExecMode.ReviewWrites {
		return nil
	}
	ctx = withAssistantApprovalReviewer(ctx, s.approvalReviewer)
	review := reviewAssistantAction(ctx, name, "用请求人自己的 Git Token 向项目绑定的 GitHub 仓库写入评论或标签，对外公开可见，执行后只能到 GitHub 上手动撤销", map[string]any{"projectId": s.ProjectID, "repository": path, "arguments": args})
	if !review.allows() {
		return assistantReviewRefusal(name, review)
	}
	return nil
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

func githubArgNumber(args map[string]any) (int, error) {
	number := githubArgInt(args, "number", 0)
	if number <= 0 {
		return 0, fmt.Errorf("number is required and must be a positive integer")
	}
	return number, nil
}

func githubCommentArgs(args map[string]any) (int, string, error) {
	number, err := githubArgNumber(args)
	if err != nil {
		return 0, "", err
	}
	body, _ := args["body"].(string)
	body = strings.TrimSpace(body)
	if body == "" {
		return 0, "", fmt.Errorf("body is required")
	}
	if len([]rune(body)) > 10000 {
		return 0, "", fmt.Errorf("body must be at most 10000 characters")
	}
	return number, body, nil
}

func githubLabelsArgs(args map[string]any) (int, []string, error) {
	number, err := githubArgNumber(args)
	if err != nil {
		return 0, nil, err
	}
	raw, ok := args["labels"].([]any)
	if !ok || len(raw) == 0 {
		return 0, nil, fmt.Errorf("labels is required and must be a non-empty array of strings")
	}
	labels := make([]string, 0, len(raw))
	for _, v := range raw {
		s := strings.TrimSpace(fmt.Sprint(v))
		if s != "" {
			labels = append(labels, s)
		}
	}
	if len(labels) == 0 {
		return 0, nil, fmt.Errorf("labels is required and must be a non-empty array of strings")
	}
	return number, labels, nil
}

func githubHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}

// githubToolError 按具体操作给出报错。githubPRError 是为提交 PR 写的，403
// 一律提示 Pull requests 权限；这组工具各自需要的权限不同，单独说明。
func githubToolError(action, permission string, status int, raw []byte) error {
	var body struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(raw, &body)
	detail := strings.TrimSpace(body.Message)
	for _, e := range body.Errors {
		if e.Message != "" {
			detail = strings.TrimSpace(detail + " " + e.Message)
		}
	}
	if detail == "" {
		detail = strings.TrimSpace(truncateRunes(string(raw), 300))
	}
	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("GitHub Git Token 无效或已过期（HTTP 401）：请到「个人配置 → 个人凭证」重新配置")
	case http.StatusForbidden:
		return fmt.Errorf("GitHub 拒绝%s（HTTP 403）：Git Token 需要目标仓库的 %s 权限。%s", action, permission, detail)
	case http.StatusNotFound:
		return fmt.Errorf("GitHub %s失败（HTTP 404）：仓库或编号不存在，或 Git Token 无权访问该仓库（私有仓库对无权限的令牌也返回 404）。%s", action, detail)
	}
	return fmt.Errorf("GitHub %s失败（HTTP %d）：%s", action, status, detail)
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

type githubIssueItem struct {
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

// githubIssuesList 列 Issue。GitHub 的 /issues 端点也会混入 PR（PR 也是一种
// Issue），按返回体里是否带 pull_request 字段过滤掉。因为过滤发生在取回之后，
// 不能把 per_page 设成 limit：那样 PR 多的仓库会返回远少于 limit 的 Issue，
// 看起来却像完整结果。这里按满页取、过滤后截到 limit，不够就翻页（最多
// githubIssuesMaxPages 页），并用 hasMore 标明是否还有没列出的 Issue。
func githubIssuesList(ctx context.Context, token, path string, args map[string]any) (string, error) {
	state, err := githubArgState(args)
	if err != nil {
		return "", err
	}
	limit := githubArgLimit(args)
	q := url.Values{"state": {state}, "per_page": {strconv.Itoa(githubListPageSize)}}
	if labels, _ := args["labels"].(string); strings.TrimSpace(labels) != "" {
		q.Set("labels", strings.TrimSpace(labels))
	}
	client := githubHTTPClient()
	out := make([]githubIssueOut, 0, limit)
	hasMore := false
	for page := 1; page <= githubIssuesMaxPages; page++ {
		q.Set("page", strconv.Itoa(page))
		resp, raw, err := githubRequest(ctx, client, http.MethodGet, githubAPIBase+"/repos/"+path+"/issues?"+q.Encode(), token, nil)
		if err != nil {
			return "", fmt.Errorf("无法连接 GitHub：%v", err)
		}
		if resp.StatusCode != http.StatusOK {
			return "", githubToolError("读取 Issue 列表", "Issues 读取", resp.StatusCode, raw)
		}
		var items []githubIssueItem
		if json.Unmarshal(raw, &items) != nil {
			return "", fmt.Errorf("GitHub 返回的 Issue 列表无法解析")
		}
		for _, it := range items {
			if len(it.PullRequest) > 0 {
				continue
			}
			if len(out) == limit {
				hasMore = true
				break
			}
			labels := make([]string, 0, len(it.Labels))
			for _, l := range it.Labels {
				labels = append(labels, l.Name)
			}
			out = append(out, githubIssueOut{Number: it.Number, Title: it.Title, State: it.State, URL: it.HTMLURL, Author: it.User.Login, Labels: labels, Created: it.CreatedAt, Updated: it.UpdatedAt})
		}
		if hasMore || len(items) < githubListPageSize {
			break
		}
		if page == githubIssuesMaxPages {
			// 翻页上限内没扫完，后面可能还有 Issue。
			hasMore = true
		}
	}
	body, _ := json.Marshal(map[string]any{"repository": path, "issues": out, "count": len(out), "hasMore": hasMore})
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

// githubPullsList 列 PR。/pulls 不需要事后过滤，多取一条即可判断 hasMore。
func githubPullsList(ctx context.Context, token, path string, args map[string]any) (string, error) {
	state, err := githubArgState(args)
	if err != nil {
		return "", err
	}
	limit := githubArgLimit(args)
	q := url.Values{"state": {state}, "per_page": {strconv.Itoa(limit + 1)}}
	client := githubHTTPClient()
	resp, raw, err := githubRequest(ctx, client, http.MethodGet, githubAPIBase+"/repos/"+path+"/pulls?"+q.Encode(), token, nil)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", githubToolError("读取 PR 列表", "Pull requests 读取", resp.StatusCode, raw)
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
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	out := make([]githubPullOut, 0, len(items))
	for _, it := range items {
		out = append(out, githubPullOut{Number: it.Number, Title: it.Title, State: it.State, Draft: it.Draft, URL: it.HTMLURL, Author: it.User.Login, Head: it.Head.Ref, Base: it.Base.Ref, Created: it.CreatedAt, Updated: it.UpdatedAt})
	}
	body, _ := json.Marshal(map[string]any{"repository": path, "pulls": out, "count": len(out), "hasMore": hasMore})
	return string(body), nil
}

type githubCheckSummary struct {
	Name  string `json:"name"`
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}

// githubCheckOutcome 把 commit status 的 state 和 check run 的
// status/conclusion 归到 success / pending / failure 三类。只有明确通过的才算
// success：action_required、startup_failure、stale 以及未知取值都需要人处理，
// 归为 failure，不能让 PR 看起来"全绿"。
func githubCheckOutcome(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "success", "neutral", "skipped":
		return "success"
	case "pending", "queued", "in_progress", "waiting", "requested", "expected", "":
		return "pending"
	}
	return "failure"
}

func githubChecksWarning(source, permission string, status int) string {
	switch status {
	case http.StatusForbidden, http.StatusNotFound:
		return fmt.Sprintf("%s 读取失败（HTTP %d）：Git Token 可能缺少 %s 读取权限，这部分检查结果未计入", source, status, permission)
	}
	return fmt.Sprintf("%s 读取失败（HTTP %d），这部分检查结果未计入", source, status)
}

// githubCombinedChecks 合并两套 GitHub 检查来源：旧的 commit status API（很多
// 第三方 CI 还在用）和新的 check-runs API（GitHub Actions）。任一项失败就
// 整体标 failure；没有失败但有未完成的标 pending；都成功才是 success。
// 任一来源读取失败（比如令牌没有 Checks 权限返回 403）时写进 warnings，
// 并且不再给出 none/success 这种"看起来没问题"的结论，而是 unknown。
func githubCombinedChecks(ctx context.Context, client *http.Client, token, path, sha string) ([]githubCheckSummary, string, []string) {
	if strings.TrimSpace(sha) == "" {
		return nil, "none", nil
	}
	out, warnings := githubCommitStatuses(ctx, client, token, path, sha)
	runs, runWarnings := githubCheckRuns(ctx, client, token, path, sha)
	out = append(out, runs...)
	warnings = append(warnings, runWarnings...)
	return out, githubChecksOverall(out, warnings), warnings
}

// githubCommitStatuses 读取旧的 commit status API，返回各检查项和读取告警。
func githubCommitStatuses(ctx context.Context, client *http.Client, token, path, sha string) ([]githubCheckSummary, []string) {
	resp, raw, err := githubRequest(ctx, client, http.MethodGet, fmt.Sprintf("%s/repos/%s/commits/%s/status?per_page=%d", githubAPIBase, path, sha, githubListPageSize), token, nil)
	if err != nil {
		return nil, []string{fmt.Sprintf("commit status 读取失败：%v", err)}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, []string{githubChecksWarning("commit status", "Commit statuses", resp.StatusCode)}
	}
	var combined struct {
		TotalCount int `json:"total_count"`
		Statuses   []struct {
			Context   string `json:"context"`
			State     string `json:"state"`
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	if json.Unmarshal(raw, &combined) != nil {
		return nil, []string{"commit status 返回无法解析，这部分检查结果未计入"}
	}
	var out []githubCheckSummary
	var warnings []string
	for _, s := range combined.Statuses {
		out = append(out, githubCheckSummary{Name: s.Context, State: s.State, URL: s.TargetURL})
	}
	if combined.TotalCount > len(combined.Statuses) {
		warnings = append(warnings, fmt.Sprintf("commit status 共 %d 项，只统计了前 %d 项", combined.TotalCount, len(combined.Statuses)))
	}
	return out, warnings
}

// githubCheckRuns 读取 check-runs API（GitHub Actions），有 conclusion 时以它为准。
func githubCheckRuns(ctx context.Context, client *http.Client, token, path, sha string) ([]githubCheckSummary, []string) {
	resp, raw, err := githubRequest(ctx, client, http.MethodGet, fmt.Sprintf("%s/repos/%s/commits/%s/check-runs?per_page=%d", githubAPIBase, path, sha, githubListPageSize), token, nil)
	if err != nil {
		return nil, []string{fmt.Sprintf("check runs 读取失败：%v", err)}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, []string{githubChecksWarning("check runs", "Checks", resp.StatusCode)}
	}
	var runs struct {
		TotalCount int `json:"total_count"`
		CheckRuns  []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"check_runs"`
	}
	if json.Unmarshal(raw, &runs) != nil {
		return nil, []string{"check runs 返回无法解析，这部分检查结果未计入"}
	}
	var out []githubCheckSummary
	var warnings []string
	for _, r := range runs.CheckRuns {
		state := r.Status
		if strings.TrimSpace(r.Conclusion) != "" {
			state = r.Conclusion
		}
		out = append(out, githubCheckSummary{Name: r.Name, State: state, URL: r.HTMLURL})
	}
	if runs.TotalCount > len(runs.CheckRuns) {
		warnings = append(warnings, fmt.Sprintf("check runs 共 %d 项，只统计了前 %d 项", runs.TotalCount, len(runs.CheckRuns)))
	}
	return out, warnings
}

// githubChecksOverall 汇总整体状态：任一失败即 failure，否则有未完成即
// pending，全部成功才是 success，没有检查项为 none；有读取告警时不给出
// none/success 这种"看起来没问题"的结论，改为 unknown。
func githubChecksOverall(checks []githubCheckSummary, warnings []string) string {
	overall := "none"
	if len(checks) > 0 {
		overall = "success"
		for _, c := range checks {
			switch githubCheckOutcome(c.State) {
			case "failure":
				overall = "failure"
			case "pending":
				if overall != "failure" {
					overall = "pending"
				}
			}
		}
	}
	if len(warnings) > 0 && (overall == "none" || overall == "success") {
		overall = "unknown"
	}
	return overall
}

func githubPRStatus(ctx context.Context, token, path string, args map[string]any) (string, error) {
	number, err := githubArgNumber(args)
	if err != nil {
		return "", err
	}
	client := githubHTTPClient()
	resp, raw, err := githubRequest(ctx, client, http.MethodGet, fmt.Sprintf("%s/repos/%s/pulls/%d", githubAPIBase, path, number), token, nil)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", githubToolError("读取 PR 详情", "Pull requests 读取", resp.StatusCode, raw)
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
	checks, checksState, warnings := githubCombinedChecks(ctx, client, token, path, pr.Head.SHA)
	result := map[string]any{
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
	}
	if len(warnings) > 0 {
		result["checksWarnings"] = warnings
	}
	out, _ := json.Marshal(result)
	return string(out), nil
}

const githubIssueWritePermission = "Issues 读写（在 PR 上操作时也可以是 Pull requests 读写）"

func githubIssueCommentAdd(ctx context.Context, token, path string, number int, body string) (string, error) {
	client := githubHTTPClient()
	payload, _ := json.Marshal(map[string]string{"body": body})
	resp, raw, err := githubRequest(ctx, client, http.MethodPost, fmt.Sprintf("%s/repos/%s/issues/%d/comments", githubAPIBase, path, number), token, payload)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return "", githubToolError("添加评论", githubIssueWritePermission, resp.StatusCode, raw)
	}
	var comment struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	_ = json.Unmarshal(raw, &comment)
	out, _ := json.Marshal(map[string]any{"repository": path, "number": number, "commentId": comment.ID, "url": comment.HTMLURL})
	return string(out), nil
}

func githubIssueLabelsAdd(ctx context.Context, token, path string, number int, labels []string) (string, error) {
	client := githubHTTPClient()
	payload, _ := json.Marshal(map[string][]string{"labels": labels})
	resp, body, err := githubRequest(ctx, client, http.MethodPost, fmt.Sprintf("%s/repos/%s/issues/%d/labels", githubAPIBase, path, number), token, payload)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", githubToolError("添加标签", githubIssueWritePermission, resp.StatusCode, body)
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

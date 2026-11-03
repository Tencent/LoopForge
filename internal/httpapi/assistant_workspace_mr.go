package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// workbench_workspace_submit_mr delivers a conversation workspace without
// an SOP delivery node: it commits and pushes the work branch, then opens a
// GitHub pull request (or a merge request on the optional legacy Gongfeng
// host). Both use the requesting user's own Git Token, so the push and the
// request are attributed to them and limited by their repository permissions.
// SOP runs keep delivering through nodes.

const gongfengWebBase = "https://git.example.com"

// githubWebBase and githubAPIBase are variables so tests can use a local server.
var (
	githubWebBase = "https://github.com"
	githubAPIBase = "https://api.github.com"
)

var (
	gongfengRepoPath = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)+$`)
	gitBranchName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
)

type workspaceMRResult struct {
	Repository   string `json:"repository"`
	SourceBranch string `json:"sourceBranch"`
	TargetBranch string `json:"targetBranch"`
	Commit       string `json:"commit,omitempty"`
	Pushed       bool   `json:"pushed"`
	MRURL        string `json:"mrUrl,omitempty"`
	MRIID        int64  `json:"mrIid,omitempty"`
	Existing     bool   `json:"existing,omitempty"`
	Updated      bool   `json:"updated,omitempty"`
	UpdateError  string `json:"updateError,omitempty"`
	CreateURL    string `json:"createUrl,omitempty"`
	Error        string `json:"error,omitempty"`
}

func workspaceSubmitMRDescriptor() agentToolDescriptor {
	return agentToolDescriptor{
		Name:        "workbench_workspace_submit_mr",
		Description: "把当前工作区（任务，或本对话打开的临时工作区）的改动提交到工作分支、用请求人自己的 Git Token 推送，并发起 Pull Request（GitHub 仓库）或 MR（工蜂仓库），返回链接 mrUrl。调用前先用 workbench_workspace_command 跑通相关测试。title 写清改了什么；description 用 Markdown 写改动摘要和验证结果。工作分支已有打开的 PR/MR 时会推送新提交，并用本次 title、description 更新它，所以 description 要概括整个 PR/MR 的改动，不只是这次的增量。targetBranch 默认是仓库的基线分支。",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":200},"description":{"type":"string","maxLength":20000},"targetBranch":{"type":"string","maxLength":200}},"required":["title"],"additionalProperties":false}`),
		Kind:        "workspace",
	}
}

func (h *agentToolGateway) workspaceSubmitMR(ctx context.Context, s *AgentToolSession, args map[string]any) (string, error) {
	runID := mainBotWorkspaceRun(ctx)
	if runID == "" {
		runID = s.workspaceRun()
	}
	run, ok := h.repo.RunState(runID)
	if !ok {
		return "", errAgentToolForbidden
	}
	if !run.IsWorkspace() {
		if s.RunID != "" {
			return "", fmt.Errorf("只有对话工作区可以直接提交 MR；SOP 运行请由交付节点提交")
		}
		if !borrowableSOPRun(run, s.ProjectID) || !domain.TerminalRunStatus(run.Status) {
			return "", fmt.Errorf("只有对话工作区或已结束的 SOP 工作区可以直接提交 MR；SOP 运行请由交付节点提交，要自己提交先调用 workbench_workspace_sop takeover 接管")
		}
	}
	title, _ := args["title"].(string)
	title = strings.TrimSpace(title)
	if title == "" || len([]rune(title)) > 200 {
		return "", fmt.Errorf("title is required and must be at most 200 characters")
	}
	description, _ := args["description"].(string)
	if len([]rune(description)) > 20000 {
		return "", fmt.Errorf("description must be at most 20000 characters")
	}
	target, _ := args["targetBranch"].(string)
	target = strings.TrimSpace(target)
	if target != "" && (!gitBranchName.MatchString(target) || strings.Contains(target, "..")) {
		return "", fmt.Errorf("targetBranch 不是合法的分支名")
	}
	requester, ok := h.repo.User(s.User.ID)
	if !ok {
		return "", errAgentToolForbidden
	}
	gitToken := strings.TrimSpace(h.repo.GitCredential(ctx, s.User.ID))
	_, apiToken := gitwork.SplitToken(gitToken)
	if apiToken == "" {
		return "", fmt.Errorf("你还没有配置 Git Token，无法推送和发起 PR。请到「个人配置 → 个人凭证」配置：打开 https://github.com/settings/tokens 新建令牌，为目标仓库授予 Contents 和 Pull requests 的读写权限，复制后粘贴并点校验")
	}
	if h.engine == nil {
		return "", fmt.Errorf("执行引擎不可用")
	}
	login := strings.TrimSpace(requester.LoginName)
	email := ""
	if login != "" {
		email = login + "@users.noreply.loopforge.invalid"
	}
	heads, err := h.engine.PushConversationWorkspace(ctx, run.ID, gitToken, login, email, title)
	if err != nil {
		return "", errors.New(redactAgentToolResult(err.Error(), requester))
	}
	pushed := map[string]domain.RunnerWorkBranchHead{}
	for _, head := range heads {
		pushed[head.RepositoryID+"\x00"+head.WorkBranch] = head
	}
	body := strings.TrimSpace(description)
	if body != "" {
		body += "\n\n"
	}
	body += fmt.Sprintf("---\n由 LoopForge 对话直接改代码提交 · 任务 `%s` · 发起人 %s", run.ID, login)
	results := []workspaceMRResult{}
	for _, rc := range run.RepositoryContexts() {
		head, ok := pushed[rc.RepositoryID+"\x00"+rc.WorkBranch]
		if !ok {
			continue
		}
		result := workspaceMRResult{Repository: rc.Name, SourceBranch: rc.WorkBranch, TargetBranch: target, Commit: head.Head, Pushed: true}
		if result.TargetBranch == "" {
			result.TargetBranch = repositoryBaseBranchName(rc)
		}
		if path := githubRepoPath(rc); path != "" {
			result.CreateURL = githubWebBase + "/" + path + "/compare/" + url.PathEscape(result.TargetBranch) + "..." + url.PathEscape(result.SourceBranch) + "?expand=1"
			pr, err := createGitHubPR(ctx, apiToken, path, result.SourceBranch, result.TargetBranch, title, body)
			if err != nil {
				result.Error = redactAgentToolResult(err.Error(), requester)
			} else {
				result.MRIID, result.Existing, result.Updated = pr.IID, pr.Existing, pr.Updated
				if pr.UpdateError != "" {
					result.UpdateError = redactAgentToolResult(pr.UpdateError, requester)
				}
				result.MRURL = fmt.Sprintf("%s/%s/pull/%d", githubWebBase, path, pr.IID)
			}
			results = append(results, result)
			continue
		}
		path := gongfengProjectPath(rc)
		if path == "" {
			result.Error = "不是 GitHub 或已配置的工蜂仓库，请到代码平台手动发起 PR/MR"
			results = append(results, result)
			continue
		}
		result.CreateURL = gongfengWebBase + "/" + path + "/merge_requests/new"
		mr, err := createGongfengMR(ctx, apiToken, path, result.SourceBranch, result.TargetBranch, title, body)
		if err != nil {
			result.Error = redactAgentToolResult(err.Error(), requester)
		} else {
			result.MRIID, result.Existing, result.Updated = mr.IID, mr.Existing, mr.Updated
			if mr.UpdateError != "" {
				result.UpdateError = redactAgentToolResult(mr.UpdateError, requester)
			}
			result.MRURL = fmt.Sprintf("%s/%s/merge_requests/%d", gongfengWebBase, path, mr.IID)
		}
		results = append(results, result)
	}
	if len(results) == 0 {
		return "", fmt.Errorf("工作区里的仓库都没有相对基线分支的改动，没有可提交的内容")
	}
	note := "PR/MR 已发起，把 mrUrl 发给用户。没有改动的仓库不会发起。"
	for _, r := range results {
		if r.Existing {
			note = "工作分支已有打开的 PR/MR，新提交已推送到其中；updated 为 true 表示标题和描述已更新为本次内容，有 updateError 时如实告诉用户描述没能更新。把 mrUrl 发给用户。"
		}
	}
	for _, r := range results {
		if r.MRURL == "" {
			note = "部分仓库没能自动发起 PR/MR：如实告诉用户 error，并给出 createUrl 让用户手动发起（源分支 sourceBranch、目标分支 targetBranch）。"
		}
	}
	out := map[string]any{"results": results, "note": note}
	if run.IsWorkspace() {
		issue, created, err := h.linkConversationRequirement(s, run, conversationRequirement{Title: title, Description: strings.TrimSpace(description)}, true)
		if err == nil {
			err = h.recordConversationMRs(issue.ID, results, run.RepositoryContexts())
		}
		if err != nil {
			log.Printf("record conversation workspace %s delivery: %v", run.ID, err)
		}
		if issue.ID != "" {
			out["issueId"], out["issueTitle"] = issue.ID, issue.Title
			if created {
				out["note"] = note + fmt.Sprintf("这次改动已登记为项目需求 %s「%s」，MR 合并后需求自动完成；告诉用户可以在需求列表查看。", issue.ID, truncateRunes(issue.Title, 60))
			}
		}
	}
	raw, _ := json.Marshal(out)
	return redactAgentToolResult(string(raw), requester), nil
}

// gongfengProjectPath returns "group/name" for Gongfeng repositories and ""
// for anything else, so the token is only ever sent to the Gongfeng host.
func gongfengProjectPath(rc domain.RepositoryContext) string {
	return hostedRepoPath(rc, strings.TrimPrefix(gongfengWebBase, "https://"), gongfengRepoPath)
}

var githubRepoPathPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// githubRepoPath returns "owner/name" for github.com repositories and "" for
// anything else, so the token is only ever sent to GitHub.
func githubRepoPath(rc domain.RepositoryContext) string {
	path, ok := strings.CutPrefix(strings.TrimSpace(rc.GongfengProjectID), "github:")
	if !ok {
		path = hostedRepoPath(rc, "github.com", githubRepoPathPattern)
	}
	if !githubRepoPathPattern.MatchString(path) || strings.Contains(path, "..") {
		return ""
	}
	return path
}

func hostedRepoPath(rc domain.RepositoryContext, host string, valid *regexp.Regexp) string {
	for _, raw := range []string{rc.CloneURL, rc.WebURL} {
		raw = strings.TrimSpace(raw)
		path := ""
		switch {
		case strings.HasPrefix(raw, "git@"+host+":"):
			path = strings.TrimPrefix(raw, "git@"+host+":")
		case raw != "":
			u, err := url.Parse(raw)
			if err != nil || !strings.EqualFold(u.Hostname(), host) {
				continue
			}
			path = u.Path
		}
		path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
		if valid.MatchString(path) {
			return path
		}
	}
	return ""
}

// createGitHubPR opens a pull request. When the branches already have an open
// one it returns that PR and brings its title and body up to this submission.
func createGitHubPR(ctx context.Context, token, path, source, target, title, description string) (gongfengMR, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	repoURL := githubAPIBase + "/repos/" + path
	payload, _ := json.Marshal(map[string]string{"head": source, "base": target, "title": title, "body": description})
	resp, raw, err := githubRequest(ctx, client, http.MethodPost, repoURL+"/pulls", token, payload)
	if err != nil {
		return gongfengMR{}, fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		var pr struct {
			Number int64 `json:"number"`
		}
		if json.Unmarshal(raw, &pr) == nil && pr.Number > 0 {
			return gongfengMR{IID: pr.Number}, nil
		}
		return gongfengMR{}, fmt.Errorf("GitHub 返回的 PR 信息无法解析")
	}
	owner, _, _ := strings.Cut(path, "/")
	query := url.Values{"state": {"open"}, "head": {owner + ":" + source}, "base": {target}}
	listResp, listRaw, listErr := githubRequest(ctx, client, http.MethodGet, repoURL+"/pulls?"+query.Encode(), token, nil)
	var open []struct {
		Number int64 `json:"number"`
	}
	if listErr != nil || listResp.StatusCode != http.StatusOK || json.Unmarshal(listRaw, &open) != nil || len(open) == 0 || open[0].Number <= 0 {
		return gongfengMR{}, githubPRError(resp.StatusCode, raw)
	}
	result := gongfengMR{IID: open[0].Number, Existing: true}
	update, _ := json.Marshal(map[string]string{"title": title, "body": description})
	upResp, upRaw, upErr := githubRequest(ctx, client, http.MethodPatch, fmt.Sprintf("%s/pulls/%d", repoURL, open[0].Number), token, update)
	switch {
	case upErr != nil:
		result.UpdateError = fmt.Sprintf("无法连接 GitHub：%v", upErr)
	case upResp.StatusCode != http.StatusOK:
		result.UpdateError = fmt.Sprintf("更新 PR 标题和描述失败：%v", githubPRError(upResp.StatusCode, upRaw))
	default:
		result.Updated = true
	}
	return result, nil
}

func githubRequest(ctx context.Context, client *http.Client, method, rawURL, token string, payload []byte) (*http.Response, []byte, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	return resp, raw, nil
}

func githubPRError(status int, raw []byte) error {
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
		return fmt.Errorf("GitHub 拒绝发起 PR（HTTP 403）：Git Token 需要目标仓库的 Pull requests 读写权限。%s", detail)
	case http.StatusNotFound:
		return fmt.Errorf("GitHub 找不到仓库或分支（HTTP 404）：%s", detail)
	}
	return fmt.Errorf("GitHub 发起 PR 失败（HTTP %d）：%s", status, detail)
}

type gongfengMR struct {
	IID         int64
	Existing    bool
	Updated     bool
	UpdateError string
}

// createGongfengMR opens an MR. When the branches already have an open one it
// returns that MR and brings its title and description up to this submission,
// so the MR keeps describing what the branch now contains.
func createGongfengMR(ctx context.Context, token, path, source, target, title, description string) (gongfengMR, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	project := gongfengAPIBase + "/projects/" + url.PathEscape(path)
	payload, _ := json.Marshal(map[string]string{"source_branch": source, "target_branch": target, "title": title, "description": description})
	resp, raw, err := gongfengPostOrGet(ctx, client, http.MethodPost, project+"/merge_requests", token, payload)
	if err != nil {
		return gongfengMR{}, fmt.Errorf("无法连接工蜂：%v", err)
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		var mr struct {
			IID int64 `json:"iid"`
		}
		if json.Unmarshal(raw, &mr) == nil && mr.IID > 0 {
			return gongfengMR{IID: mr.IID}, nil
		}
		return gongfengMR{}, fmt.Errorf("工蜂返回的 MR 信息无法解析")
	}
	open, found := findOpenGongfengMR(ctx, client, token, project, source, target)
	if !found {
		return gongfengMR{}, gongfengMRError(resp.StatusCode, raw)
	}
	result := gongfengMR{IID: open.IID, Existing: true}
	if err := updateGongfengMR(ctx, client, token, project, open.ID, title, description); err != nil {
		result.UpdateError = err.Error()
	} else {
		result.Updated = true
	}
	return result, nil
}

type gongfengOpenMR struct {
	ID           int64  `json:"id"`
	IID          int64  `json:"iid"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
}

// updateGongfengMR edits an MR's title and description. The v3 API addresses
// an MR here by its project-wide id, not the iid shown in its URL.
func updateGongfengMR(ctx context.Context, client *http.Client, token, project string, id int64, title, description string) error {
	if id <= 0 {
		return fmt.Errorf("工蜂没有返回 MR 的 id，没能更新标题和描述")
	}
	payload, _ := json.Marshal(map[string]string{"title": title, "description": description})
	resp, raw, err := gongfengPostOrGet(ctx, client, http.MethodPut, fmt.Sprintf("%s/merge_request/%d", project, id), token, payload)
	if err != nil {
		return fmt.Errorf("无法连接工蜂：%v", err)
	}
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("更新 MR 标题和描述失败：%v", gongfengMRError(resp.StatusCode, raw))
}

func findOpenGongfengMR(ctx context.Context, client *http.Client, token, project, source, target string) (gongfengOpenMR, bool) {
	for page := 1; page <= 3; page++ {
		resp, raw, err := gongfengPostOrGet(ctx, client, http.MethodGet, fmt.Sprintf("%s/merge_requests?state=opened&per_page=100&page=%d", project, page), token, nil)
		if err != nil || resp.StatusCode != http.StatusOK {
			return gongfengOpenMR{}, false
		}
		var mrs []gongfengOpenMR
		if json.Unmarshal(raw, &mrs) != nil {
			return gongfengOpenMR{}, false
		}
		for _, mr := range mrs {
			if mr.SourceBranch == source && mr.TargetBranch == target && mr.IID > 0 {
				return mr, true
			}
		}
		if len(mrs) < 100 {
			return gongfengOpenMR{}, false
		}
	}
	return gongfengOpenMR{}, false
}

func gongfengPostOrGet(ctx context.Context, client *http.Client, method, rawURL, token string, payload []byte) (*http.Response, []byte, error) {
	doOnce := func(bearer bool) (*http.Response, []byte, error) {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
		if err != nil {
			return nil, nil, err
		}
		if bearer {
			req.Header.Set("Authorization", "Bearer "+token)
		} else {
			req.Header.Set("PRIVATE-TOKEN", token)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		return resp, raw, nil
	}
	resp, raw, err := doOnce(false)
	if err != nil || (resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden) {
		return resp, raw, err
	}
	bearerResp, bearerRaw, bearerErr := doOnce(true)
	if bearerErr != nil {
		return resp, raw, nil
	}
	if resp.StatusCode == http.StatusForbidden && bearerResp.StatusCode == http.StatusUnauthorized {
		return resp, raw, nil
	}
	return bearerResp, bearerRaw, nil
}

func gongfengMRError(status int, raw []byte) error {
	var body struct {
		Message any `json:"message"`
		Error   any `json:"error"`
	}
	_ = json.Unmarshal(raw, &body)
	detail := strings.TrimSpace(fmt.Sprint(firstNonNil(body.Message, body.Error, "")))
	if detail == "" {
		detail = strings.TrimSpace(truncateRunes(string(raw), 300))
	}
	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("Git Token 无效或已过期（HTTP 401）：请到「个人配置 → 个人凭证」重新生成，并授予目标仓库的读写权限")
	case http.StatusForbidden:
		return fmt.Errorf("代码仓库拒绝发起 MR（HTTP 403）：Git Token 需要目标仓库的写权限，且你需要对该仓库有开发者及以上权限。%s", detail)
	case http.StatusNotFound:
		return fmt.Errorf("工蜂找不到仓库或分支（HTTP 404）：%s", detail)
	}
	return fmt.Errorf("工蜂发起 MR 失败（HTTP %d）：%s", status, detail)
}

func firstNonNil(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

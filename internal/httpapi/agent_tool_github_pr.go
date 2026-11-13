package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// PR 审查与合并、Issue 关闭这组工具让定时 Loop 不打开工作区也能完成
// "列 PR → 看改动 → 审查 → 合并 → 关闭关联 Issue" 的闭环。它们和评论、
// 加标签共用同一套令牌与仓库范围校验；合并、审查、关闭对外不可轻易撤销，
// 因此只在会话允许直接写入（完全权限或帮我批准）时执行。
const (
	githubPRFilesMaxPages    = 3
	githubPRFilePatchRunes   = 3000
	githubPRFilesPatchBudget = 40000
	githubPRBodyRunes        = 4000
	githubIssueBodyRunes     = 20000
	githubIssueCommentRunes  = 2000
	githubIssueMaxComments   = 30
)

// githubWriteAllowed 拒绝在「请求批准」模式下执行合并、审查、关闭：这些
// 操作没有可供用户确认的草稿，执行后只能在 GitHub 上手动撤销。
func githubWriteAllowed(s *AgentToolSession, name string) error {
	if s.ExecMode.AutoWrite {
		return nil
	}
	return fmt.Errorf("%s 会直接修改 GitHub，当前「请求批准」模式没有可确认的草稿，不能执行。请让用户把审批模式切到「完全权限」或「帮我批准」后再试，或告诉用户在 GitHub 上手动完成", name)
}

func githubArgString(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

type githubPRDetail struct {
	Number         int64  `json:"number"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	State          string `json:"state"`
	Draft          bool   `json:"draft"`
	Merged         bool   `json:"merged"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	HTMLURL        string `json:"html_url"`
	Additions      int    `json:"additions"`
	Deletions      int    `json:"deletions"`
	ChangedFiles   int    `json:"changed_files"`
	Commits        int    `json:"commits"`
	User           struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

type githubReviewSummary struct {
	Reviewer string `json:"reviewer"`
	State    string `json:"state"`
}

// githubLatestReviews 取每位审查人最近一次有效结论（APPROVED /
// CHANGES_REQUESTED / DISMISSED）；只留言的 COMMENTED 不覆盖之前的结论。
func githubLatestReviews(ctx context.Context, client *http.Client, token, path string, number int) ([]githubReviewSummary, int, string) {
	resp, raw, err := githubRequest(ctx, client, http.MethodGet, fmt.Sprintf("%s/repos/%s/pulls/%d/reviews?per_page=%d", githubAPIBase, path, number, githubListPageSize), token, nil)
	if err != nil {
		return nil, 0, fmt.Sprintf("审查记录读取失败：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Sprintf("审查记录读取失败（HTTP %d），审查状态未知", resp.StatusCode)
	}
	var items []struct {
		State string `json:"state"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return nil, 0, "审查记录无法解析，审查状态未知"
	}
	latest := map[string]string{}
	for _, it := range items {
		login := strings.TrimSpace(it.User.Login)
		if login == "" {
			continue
		}
		switch it.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			latest[login] = it.State
		case "COMMENTED":
			if _, ok := latest[login]; !ok {
				latest[login] = it.State
			}
		}
	}
	out := make([]githubReviewSummary, 0, len(latest))
	approvals := 0
	for login, state := range latest {
		out = append(out, githubReviewSummary{Reviewer: login, State: state})
		if state == "APPROVED" {
			approvals++
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reviewer < out[j].Reviewer })
	return out, approvals, ""
}

func githubPRFiles(ctx context.Context, token, path string, args map[string]any) (string, error) {
	number, err := githubArgNumber(args)
	if err != nil {
		return "", err
	}
	limit := githubArgInt(args, "limit", 50)
	if limit <= 0 || limit > githubListPageSize {
		limit = 50
	}
	type fileOut struct {
		Filename     string `json:"filename"`
		Status       string `json:"status"`
		Additions    int    `json:"additions"`
		Deletions    int    `json:"deletions"`
		Patch        string `json:"patch,omitempty"`
		PatchOmitted bool   `json:"patchOmitted,omitempty"`
	}
	client := githubHTTPClient()
	out := make([]fileOut, 0, limit)
	budget := githubPRFilesPatchBudget
	hasMore := false
	for page := 1; page <= githubPRFilesMaxPages && len(out) < limit; page++ {
		q := url.Values{"per_page": {strconv.Itoa(githubListPageSize)}, "page": {strconv.Itoa(page)}}
		resp, raw, err := githubRequest(ctx, client, http.MethodGet, fmt.Sprintf("%s/repos/%s/pulls/%d/files?%s", githubAPIBase, path, number, q.Encode()), token, nil)
		if err != nil {
			return "", fmt.Errorf("无法连接 GitHub：%v", err)
		}
		if resp.StatusCode != http.StatusOK {
			return "", githubToolError("读取 PR 改动文件", "Pull requests 读取", resp.StatusCode, raw)
		}
		var items []struct {
			Filename  string `json:"filename"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
			Deletions int    `json:"deletions"`
			Patch     string `json:"patch"`
		}
		if json.Unmarshal(raw, &items) != nil {
			return "", fmt.Errorf("GitHub 返回的 PR 文件列表无法解析")
		}
		for _, it := range items {
			if len(out) == limit {
				hasMore = true
				break
			}
			f := fileOut{Filename: it.Filename, Status: it.Status, Additions: it.Additions, Deletions: it.Deletions}
			f.Patch, f.PatchOmitted, budget = githubPRFilePatch(it.Patch, budget)
			out = append(out, f)
		}
		if hasMore || len(items) < githubListPageSize {
			break
		}
		if page == githubPRFilesMaxPages {
			hasMore = true
		}
	}
	body, _ := json.Marshal(map[string]any{"repository": path, "number": number, "files": out, "count": len(out), "hasMore": hasMore})
	return string(body), nil
}

// githubPRFilePatch 先把单个文件的 diff 截到 githubPRFilePatchRunes 以内，
// 再按剩余的总预算决定是否保留：放得下就保留并扣减预算，放不下就标记省略，
// 空 diff 既不保留也不标记。返回保留的 diff、是否省略和扣减后的预算。
func githubPRFilePatch(patch string, budget int) (kept string, omitted bool, remaining int) {
	if len([]rune(patch)) > githubPRFilePatchRunes {
		patch = string([]rune(patch)[:githubPRFilePatchRunes]) + "\n…（该文件 diff 过长已截断）"
	}
	if n := len([]rune(patch)); n > 0 && n <= budget {
		return patch, false, budget - n
	} else if n > 0 {
		return "", true, budget
	}
	return "", false, budget
}

func githubPRReview(ctx context.Context, token, path string, args map[string]any) (string, error) {
	number, err := githubArgNumber(args)
	if err != nil {
		return "", err
	}
	event := strings.ToUpper(githubArgString(args, "event"))
	body := githubArgString(args, "body")
	switch event {
	case "APPROVE":
	case "REQUEST_CHANGES", "COMMENT":
		if body == "" {
			return "", fmt.Errorf("event=%s 时 body 必填，写清楚审查意见", event)
		}
	default:
		return "", fmt.Errorf("event 只能是 APPROVE、REQUEST_CHANGES 或 COMMENT")
	}
	if len([]rune(body)) > 10000 {
		return "", fmt.Errorf("body must be at most 10000 characters")
	}
	payload := map[string]string{"event": event}
	if body != "" {
		payload["body"] = body
	}
	raw, _ := json.Marshal(payload)
	resp, out, err := githubRequest(ctx, githubHTTPClient(), http.MethodPost, fmt.Sprintf("%s/repos/%s/pulls/%d/reviews", githubAPIBase, path, number), token, raw)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnprocessableEntity {
			return "", fmt.Errorf("GitHub 拒绝提交审查（HTTP 422）：%s。GitHub 不允许 PR 作者审批自己的 PR；这种情况下改用 event=COMMENT 留下审查结论", githubErrorDetail(out))
		}
		return "", githubToolError("提交审查", "Pull requests 读写", resp.StatusCode, out)
	}
	var review struct {
		ID      int64  `json:"id"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
	}
	_ = json.Unmarshal(out, &review)
	result, _ := json.Marshal(map[string]any{"repository": path, "number": number, "reviewId": review.ID, "state": review.State, "url": review.HTMLURL})
	return string(result), nil
}

func githubPRMerge(ctx context.Context, token, path string, args map[string]any) (string, error) {
	number, err := githubArgNumber(args)
	if err != nil {
		return "", err
	}
	method := strings.ToLower(githubArgString(args, "method"))
	if method == "" {
		method = "merge"
	}
	if method != "merge" && method != "squash" && method != "rebase" {
		return "", fmt.Errorf("method 只能是 merge、squash 或 rebase")
	}
	payload := map[string]string{"merge_method": method}
	if sha := githubArgString(args, "sha"); sha != "" {
		payload["sha"] = sha
	}
	if title := githubArgString(args, "commitTitle"); title != "" {
		payload["commit_title"] = title
	}
	if message := githubArgString(args, "commitMessage"); message != "" {
		payload["commit_message"] = message
	}
	raw, _ := json.Marshal(payload)
	resp, out, err := githubRequest(ctx, githubHTTPClient(), http.MethodPut, fmt.Sprintf("%s/repos/%s/pulls/%d/merge", githubAPIBase, path, number), token, raw)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusMethodNotAllowed:
		return "", fmt.Errorf("PR #%d 当前不能合并（HTTP 405）：%s。常见原因：分支保护要求审批或 CI 通过、存在冲突、PR 是草稿或已关闭。先用 workbench_github_pr_status 查看 mergeableState、checksState 和 reviews，不满足条件就跳过并在汇报中说明", number, githubErrorDetail(out))
	case http.StatusConflict:
		return "", fmt.Errorf("PR #%d 的 head 已变化（HTTP 409）：%s。重新用 workbench_github_pr_status 读取最新 sha 并复核改动后再决定是否合并", number, githubErrorDetail(out))
	case http.StatusUnprocessableEntity:
		return "", fmt.Errorf("GitHub 拒绝合并 PR #%d（HTTP 422）：%s。如果是合并方式不被仓库允许，换 method=squash 或 rebase 再试", number, githubErrorDetail(out))
	default:
		return "", githubToolError("合并 PR", "Contents 读写和 Pull requests 读写", resp.StatusCode, out)
	}
	var merged struct {
		SHA     string `json:"sha"`
		Merged  bool   `json:"merged"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(out, &merged)
	result, _ := json.Marshal(map[string]any{"repository": path, "number": number, "merged": merged.Merged, "sha": merged.SHA, "method": method, "message": merged.Message})
	return string(result), nil
}

// githubIssueGet returns an issue's full body and its latest comments, which
// the list tool leaves out.
func githubIssueGet(ctx context.Context, token, path string, args map[string]any) (string, error) {
	number, err := githubArgNumber(args)
	if err != nil {
		return "", err
	}
	client := githubHTTPClient()
	resp, raw, err := githubRequest(ctx, client, http.MethodGet, fmt.Sprintf("%s/repos/%s/issues/%d", githubAPIBase, path, number), token, nil)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", githubToolError("读取 Issue", "Issues 读取", resp.StatusCode, raw)
	}
	var it struct {
		githubIssueItem
		Comments int `json:"comments"`
	}
	if json.Unmarshal(raw, &it) != nil {
		return "", fmt.Errorf("GitHub 返回的 Issue 信息无法解析")
	}
	labels := make([]string, 0, len(it.Labels))
	for _, l := range it.Labels {
		labels = append(labels, l.Name)
	}
	out := map[string]any{
		"repository": path, "number": it.Number, "title": it.Title, "state": it.State, "url": it.HTMLURL,
		"author": it.User.Login, "labels": labels, "body": truncateRunes(it.Body, githubIssueBodyRunes),
		"isPullRequest": len(it.PullRequest) > 0, "createdAt": it.CreatedAt, "updatedAt": it.UpdatedAt, "commentCount": it.Comments,
	}
	comments := []map[string]string{}
	if it.Comments > 0 {
		lastPage := (it.Comments + githubListPageSize - 1) / githubListPageSize
		resp, raw, err := githubRequest(ctx, client, http.MethodGet, fmt.Sprintf("%s/repos/%s/issues/%d/comments?per_page=%d&page=%d", githubAPIBase, path, number, githubListPageSize, lastPage), token, nil)
		var items []struct {
			Body string `json:"body"`
			User struct {
				Login string `json:"login"`
			} `json:"user"`
			CreatedAt string `json:"created_at"`
		}
		switch {
		case err != nil:
			out["commentsWarning"] = fmt.Sprintf("评论读取失败：无法连接 GitHub：%v", err)
		case resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &items) != nil:
			out["commentsWarning"] = fmt.Sprintf("评论读取失败（HTTP %d）：%s", resp.StatusCode, githubErrorDetail(raw))
		default:
			if len(items) > githubIssueMaxComments {
				items = items[len(items)-githubIssueMaxComments:]
			}
			for _, c := range items {
				comments = append(comments, map[string]string{"author": c.User.Login, "createdAt": c.CreatedAt, "body": truncateRunes(c.Body, githubIssueCommentRunes)})
			}
		}
	}
	out["comments"] = comments
	body, _ := json.Marshal(out)
	return string(body), nil
}

func githubIssueClose(ctx context.Context, token, path string, args map[string]any) (string, error) {
	number, err := githubArgNumber(args)
	if err != nil {
		return "", err
	}
	reason := strings.ToLower(githubArgString(args, "reason"))
	if reason == "" {
		reason = "completed"
	}
	if reason != "completed" && reason != "not_planned" {
		return "", fmt.Errorf("reason 只能是 completed 或 not_planned")
	}
	comment := githubArgString(args, "comment")
	if len([]rune(comment)) > 10000 {
		return "", fmt.Errorf("comment must be at most 10000 characters")
	}
	client := githubHTTPClient()
	resp, raw, err := githubRequest(ctx, client, http.MethodGet, fmt.Sprintf("%s/repos/%s/issues/%d", githubAPIBase, path, number), token, nil)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", githubToolError("读取 Issue", "Issues 读取", resp.StatusCode, raw)
	}
	var current struct {
		State       string          `json:"state"`
		HTMLURL     string          `json:"html_url"`
		PullRequest json.RawMessage `json:"pull_request"`
	}
	if json.Unmarshal(raw, &current) != nil {
		return "", fmt.Errorf("GitHub 返回的 Issue 信息无法解析")
	}
	if len(current.PullRequest) > 0 {
		return "", fmt.Errorf("#%d 是 PR 不是 Issue；这个工具只关闭 Issue，PR 请用 workbench_github_pr_merge 合并", number)
	}
	if current.State == "closed" {
		out, _ := json.Marshal(map[string]any{"repository": path, "number": number, "state": "closed", "alreadyClosed": true, "url": current.HTMLURL})
		return string(out), nil
	}
	commentURL := ""
	if comment != "" {
		added, err := githubIssueCommentAdd(ctx, token, path, number, comment)
		if err != nil {
			return "", err
		}
		var c struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal([]byte(added), &c)
		commentURL = c.URL
	}
	payload, _ := json.Marshal(map[string]string{"state": "closed", "state_reason": reason})
	resp, raw, err = githubRequest(ctx, client, http.MethodPatch, fmt.Sprintf("%s/repos/%s/issues/%d", githubAPIBase, path, number), token, payload)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", githubToolError("关闭 Issue", githubIssueWritePermission, resp.StatusCode, raw)
	}
	out := map[string]any{"repository": path, "number": number, "state": "closed", "reason": reason, "url": current.HTMLURL}
	if commentURL != "" {
		out["commentUrl"] = commentURL
	}
	result, _ := json.Marshal(out)
	return string(result), nil
}

func githubIssueCreateArgs(args map[string]any) (string, string, []string, error) {
	title := githubArgString(args, "title")
	if title == "" || len([]rune(title)) > 256 {
		return "", "", nil, fmt.Errorf("title is required and must be at most 256 characters")
	}
	body := githubArgString(args, "body")
	if len([]rune(body)) > 20000 {
		return "", "", nil, fmt.Errorf("body must be at most 20000 characters")
	}
	labels := []string{}
	if raw, ok := args["labels"]; ok && raw != nil {
		items, ok := raw.([]any)
		if !ok || len(items) > 20 {
			return "", "", nil, fmt.Errorf("labels must be an array of at most 20 strings")
		}
		for _, item := range items {
			label, ok := item.(string)
			if !ok || strings.TrimSpace(label) == "" {
				return "", "", nil, fmt.Errorf("labels must be non-empty strings")
			}
			labels = append(labels, strings.TrimSpace(label))
		}
	}
	return title, body, labels, nil
}

func githubIssueCreate(ctx context.Context, token, path, title, body string, labels []string) (string, error) {
	payload, _ := json.Marshal(map[string]any{"title": title, "body": body, "labels": labels})
	resp, raw, err := githubRequest(ctx, githubHTTPClient(), http.MethodPost, fmt.Sprintf("%s/repos/%s/issues", githubAPIBase, path), token, payload)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return "", githubToolError("新建 Issue", githubIssueWritePermission, resp.StatusCode, raw)
	}
	var created struct {
		Number  int64  `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	_ = json.Unmarshal(raw, &created)
	out, _ := json.Marshal(map[string]any{"repository": path, "number": created.Number, "url": created.HTMLURL, "labels": labels})
	return string(out), nil
}

func githubPRClose(ctx context.Context, token, path string, args map[string]any) (string, error) {
	number, err := githubArgNumber(args)
	if err != nil {
		return "", err
	}
	comment := githubArgString(args, "comment")
	if len([]rune(comment)) > 10000 {
		return "", fmt.Errorf("comment must be at most 10000 characters")
	}
	client := githubHTTPClient()
	prURL := fmt.Sprintf("%s/repos/%s/pulls/%d", githubAPIBase, path, number)
	resp, raw, err := githubRequest(ctx, client, http.MethodGet, prURL, token, nil)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", githubToolError("读取 PR", "Pull requests 读取", resp.StatusCode, raw)
	}
	var current struct {
		State   string `json:"state"`
		Merged  bool   `json:"merged"`
		HTMLURL string `json:"html_url"`
	}
	if json.Unmarshal(raw, &current) != nil {
		return "", fmt.Errorf("GitHub 返回的 PR 信息无法解析")
	}
	if current.Merged {
		return "", fmt.Errorf("PR #%d 已经合并，不能再关闭；要撤销改动请另开 revert PR", number)
	}
	if current.State == "closed" {
		out, _ := json.Marshal(map[string]any{"repository": path, "number": number, "state": "closed", "alreadyClosed": true, "url": current.HTMLURL})
		return string(out), nil
	}
	out := map[string]any{"repository": path, "number": number, "state": "closed", "url": current.HTMLURL}
	if comment != "" {
		added, err := githubIssueCommentAdd(ctx, token, path, number, comment)
		if err != nil {
			return "", err
		}
		var c struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal([]byte(added), &c)
		out["commentUrl"] = c.URL
	}
	payload, _ := json.Marshal(map[string]string{"state": "closed"})
	resp, raw, err = githubRequest(ctx, client, http.MethodPatch, prURL, token, payload)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", githubToolError("关闭 PR", "Pull requests 读写", resp.StatusCode, raw)
	}
	result, _ := json.Marshal(out)
	return string(result), nil
}

func githubErrorDetail(raw []byte) string {
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
	return detail
}

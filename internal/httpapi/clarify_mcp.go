package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/tracker"
)

const (
	clarifyDefaultModel = assistantDefaultModel
)

var (
	iwikiURLRe         = regexp.MustCompile(`(?i)https?://(?:[a-z0-9.-]*iwiki[a-z0-9.-]*|\bi\.example\.com)[^\s<>"'）】\]]+`)
	iwikiIDRe          = regexp.MustCompile(`(?i)(?:pageId|pageid|docid|docId)=([0-9A-Za-z_-]+)`)
	iwikiPathRe        = regexp.MustCompile(`(?i)/(?:pages?|p)/([0-9]{4,})`)
	tapdURLRe          = regexp.MustCompile(`(?i)https?://(?:www\.)?(?:tapd\.example\.com|tapd\.cn)[^\s<>"'）】\]]+`)
	gongfengIssueURLRe = regexp.MustCompile(`(?i)https?://git\.example\.com/[^\s<>"'）】\]]+/issues/\d+`)
)

func clarifyAllowedModel(id string) string {
	return assistantAllowedModel(id, assistantKnownCatalog())
}

func asStringKeyMap(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return map[string]any{}
	}
	return m
}

func stringifyTAPDIDs(v any) map[string]any {
	m := asStringKeyMap(v)
	if len(m) == 0 {
		return m
	}
	for key, val := range m {
		if nested, ok := val.(map[string]any); ok {
			m[key] = stringifyTAPDIDs(nested)
			continue
		}
		if tapdIDArgument(key) {
			m[key] = stringifyJSONID(val)
		}
	}
	return m
}

func tapdIDArgument(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "id", "workspace_id", "entry_id", "iteration_id", "story_id", "bug_id", "task_id", "entity_id", "docid", "pageid", "issue_id", "project_id":
		return true
	default:
		return false
	}
}

func stringifyJSONID(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return strings.TrimSpace(t.String())
	case json.RawMessage:
		s := strings.TrimSpace(string(t))
		s = strings.Trim(s, `"`)
		return s
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func stripClarifyMCPCalls(parsed any) any {
	raw, ok := parsed.(map[string]any)
	if !ok || raw == nil {
		return parsed
	}
	delete(raw, "mcpCalls")
	return raw
}

func classifyClarifyMCP(item domain.MCPServerDefinition) string {
	mid := strings.ToLower(strings.TrimSpace(item.MarketID))
	blob := strings.ToLower(item.Name + " " + item.ID + " " + mid)
	switch {
	case mid == "70" || mid == "tapd" || strings.Contains(blob, "tapd"):
		return "tapd"
	case mid == "61" || mid == "gongfeng" || strings.Contains(blob, "gongfeng") || strings.Contains(blob, "工蜂"):
		return "gongfeng"
	case mid == "58" || mid == "iwiki" || strings.Contains(blob, "iwiki"):
		return "iwiki"
	default:
		return "other"
	}
}

func (h *streamHandlers) clarifyHTTPMCPs(projectID string, userID int64) []domain.MCPServerDefinition {
	if h == nil || h.repo == nil {
		return nil
	}
	out := []domain.MCPServerDefinition{}
	for _, item := range assistantOutboundMCPServers(h.repo.MCPServers(), projectID, userID) {
		transport := strings.ToLower(strings.TrimSpace(item.Transport))
		if transport == "" && strings.TrimSpace(item.URL) != "" {
			transport = "http"
		}
		if transport != "http" && transport != "sse" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func clarifyTAPDWorkspace(issue domain.Issue, project domain.Project) string {
	if issue.ExternalRef != nil {
		if ref, err := tracker.ParseTAPDStoryURL(strings.TrimSpace(issue.ExternalRef.ExternalURL)); err == nil && strings.TrimSpace(ref.WorkspaceID) != "" {
			return ref.WorkspaceID
		}
	}
	return strings.TrimSpace(project.TAPDWorkspaceID)
}

func clarifyTAPDEntry(issue domain.Issue) (entryType, entryID string) {
	if issue.ExternalRef == nil {
		return "", ""
	}
	entryID = strings.TrimSpace(issue.ExternalRef.ExternalID)
	switch strings.ToLower(strings.TrimSpace(issue.ExternalRef.ExternalType)) {
	case "bug":
		return "bug", entryID
	case "task":
		return "task", entryID
	}
	return "story", entryID
}

func (h *streamHandlers) clarifyGongfengProjectID(issue domain.Issue) string {
	if issue.ExternalRef != nil {
		if path := gongfengProjectPathFromClarifyURL(issue.ExternalRef.ExternalURL); path != "" {
			return path
		}
	}
	if h == nil || h.repo == nil {
		return ""
	}
	for _, id := range issue.EffectiveRepositoryIDs() {
		repo, ok := h.repo.Repository(id)
		if ok && strings.TrimSpace(repo.GongfengProjectID) != "" {
			return strings.TrimSpace(repo.GongfengProjectID)
		}
	}
	project, ok := h.repo.Project(issue.ProjectID)
	if !ok || strings.TrimSpace(project.DefaultRepositoryID) == "" {
		return ""
	}
	repo, ok := h.repo.Repository(project.DefaultRepositoryID)
	if !ok {
		return ""
	}
	return strings.TrimSpace(repo.GongfengProjectID)
}

func extractTAPDRefs(texts ...string) []clarifyTAPDHit {
	seen := map[string]bool{}
	var out []clarifyTAPDHit
	for _, text := range texts {
		for _, match := range tapdURLRe.FindAllString(text, -1) {
			raw := strings.TrimRight(match, ".,;:，。；：")
			ref, err := tracker.ParseTAPDStoryURL(raw)
			if err != nil || ref.StoryID == "" || seen[ref.StoryID] {
				continue
			}
			seen[ref.StoryID] = true
			out = append(out, clarifyTAPDHit{Ref: ref, URL: raw})
		}
	}
	return out
}

type clarifyTAPDHit struct {
	Ref tracker.TAPDStoryRef
	URL string
}

type clarifyGongfengHit struct {
	Project string
	IID     int
	URL     string
}

func extractGongfengIssueRefs(texts ...string) []clarifyGongfengHit {
	seen := map[string]bool{}
	var out []clarifyGongfengHit
	for _, text := range texts {
		for _, match := range gongfengIssueURLRe.FindAllString(text, -1) {
			raw := strings.TrimRight(match, ".,;:，。；：")
			project := gongfengProjectPathFromClarifyURL(raw)
			iid := gongfengIssueIIDFromURL(raw)
			key := project + "#" + strconv.Itoa(iid)
			if project == "" || iid <= 0 || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, clarifyGongfengHit{Project: project, IID: iid, URL: raw})
		}
	}
	return out
}

func gongfengProjectPathFromClarifyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	path := raw
	if i := strings.Index(strings.ToLower(raw), "git.example.com/"); i >= 0 {
		path = raw[i+len("git.example.com/"):]
	}
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	for i, part := range parts {
		if !strings.EqualFold(part, "issues") {
			continue
		}
		prefix := parts[:i]
		if n := len(prefix); n > 0 && prefix[n-1] == "-" {
			prefix = prefix[:n-1]
		}
		if len(prefix) >= 2 {
			return strings.Join(prefix, "/")
		}
	}
	return ""
}

func gongfengIssueIIDFromURL(raw string) int {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	i := strings.LastIndex(strings.ToLower(raw), "/issues/")
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(digitsPrefixClarify(raw[i+len("/issues/"):]))
	return n
}

func digitsPrefixClarify(s string) string {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return s[:n]
}

func hydrateClarifyIssueFromText(issue *domain.Issue, extras ...string) {
	if issue == nil || issue.ExternalRef != nil {
		return
	}
	texts := append([]string{issue.Title, issue.Description}, extras...)
	if hits := extractTAPDRefs(texts...); len(hits) > 0 {
		hit := hits[0]
		kind := strings.TrimSpace(hit.Ref.Kind)
		if kind == "" {
			kind = "story"
		}
		issue.Source = "tapd"
		issue.ExternalRef = &domain.ExternalRef{Source: "tapd", ExternalID: hit.Ref.StoryID, ExternalType: kind, ExternalURL: hit.URL}
		return
	}
	if hits := extractGongfengIssueRefs(texts...); len(hits) > 0 {
		hit := hits[0]
		issue.Source = "gongfeng"
		issue.ExternalRef = &domain.ExternalRef{Source: "gongfeng", ExternalID: strconv.Itoa(hit.IID), ExternalIID: hit.IID, ExternalType: "issue", ExternalURL: hit.URL}
	}
}

func clarifyRequestTexts(request chatClarifyRequest) []string {
	texts := []string{request.Title, request.Description, request.Message}
	for _, turn := range request.History {
		texts = append(texts, turn.Content)
	}
	return texts
}

func extractIWikiURLs(texts ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, text := range texts {
		for _, match := range iwikiURLRe.FindAllString(text, -1) {
			url := strings.TrimRight(match, ".,;:，。；：")
			if url == "" || seen[url] {
				continue
			}
			seen[url] = true
			out = append(out, url)
		}
	}
	return out
}

func iwikiDocIDFromURL(raw string) string {
	if m := iwikiIDRe.FindStringSubmatch(raw); len(m) == 2 {
		return m[1]
	}
	if m := iwikiPathRe.FindStringSubmatch(raw); len(m) == 2 {
		return m[1]
	}
	return ""
}

func (h *streamHandlers) clarifyMCPInstructions(issue domain.Issue, user domain.User, resumed bool, extras ...string) string {
	return h.clarifyMCPInstructionsFor(issue, user, resumed, extras...)
}

func (h *streamHandlers) clarifyMCPInstructionsFor(issue domain.Issue, user domain.User, resumed bool, extras ...string) string {
	projectID := strings.TrimSpace(issue.ProjectID)
	if projectID == "" {
		return ""
	}
	servers := h.clarifyHTTPMCPs(projectID, user.ID)
	project, _ := h.repo.Project(projectID)
	var tapd, gongfeng, iwiki []domain.MCPServerDefinition
	var other []domain.MCPServerDefinition
	for _, item := range servers {
		switch classifyClarifyMCP(item) {
		case "tapd":
			tapd = append(tapd, item)
		case "gongfeng":
			gongfeng = append(gongfeng, item)
		case "iwiki":
			iwiki = append(iwiki, item)
		default:
			other = append(other, item)
		}
	}
	var b strings.Builder
	b.WriteString("\n\n## 可用工具（只读）\n使用当前项目和个人已安装的 HTTP MCP 以及 agentflow__* 查询原单、评论和文档。stdio MCP 不可用。不要调用写入类工具。资料足够后调用 submit_clarification。\n")
	if len(tapd) > 0 {
		b.WriteString("TAPD 的 id/workspace_id/entry_id 必须是字符串，读取用 tapd__proxy_execute_tool。\n")
	}
	if len(gongfeng) > 0 {
		b.WriteString("代码托管 Issue 用 gongfeng__get_issue_detail、gongfeng__get_issue_notes 读取。\n")
	}
	if len(iwiki) > 0 {
		b.WriteString("文档链接用 iwiki__getDocument 读取。\n")
	}
	if len(servers) > 0 {
		b.WriteString("当前项目和个人可用 HTTP MCP：\n")
		for _, item := range servers {
			fmt.Fprintf(&b, "- %s（id=%s", item.Name, item.ID)
			if strings.TrimSpace(item.MarketID) != "" {
				fmt.Fprintf(&b, ", marketId=%s", item.MarketID)
			}
			b.WriteString("）\n")
		}
	}
	tapdTool := func(name string) string {
		return "tapd__" + name
	}
	if len(tapd) > 0 && strings.EqualFold(strings.TrimSpace(issue.Source), "tapd") {
		ws := clarifyTAPDWorkspace(issue, project)
		entryType, entryID := clarifyTAPDEntry(issue)
		getTool := "stories_get"
		switch entryType {
		case "bug":
			getTool = "bugs_get"
		case "task":
			getTool = "tasks_get"
		}
		if resumed {
			fmt.Fprintf(&b, "这是 TAPD %s。对话里已有此前分析，不要再重拉原单和评论。仅当出现尚未读取的新 iWiki 链接时才 getDocument。\n", entryType)
		} else {
			fmt.Fprintf(&b, "这是 TAPD %s。先静默读取原单和评论。调用 %s：tool_name=%s，tool_args.workspace_id=%s，tool_args.id=%s（字符串），tool_args.limit=1。评论同样调用该工具，tool_name=comments_get，tool_args.workspace_id=%s，tool_args.entry_type=%s，tool_args.entry_id=%s。不要用 params，必须用 tool_args。\n",
				entryType, tapdTool("proxy_execute_tool"), getTool, ws, entryID, ws, entryType, entryID)
		}
	}
	if len(gongfeng) > 0 && strings.EqualFold(strings.TrimSpace(issue.Source), "gongfeng") {
		gfProject := h.clarifyGongfengProjectID(issue)
		issueID, issueIID := "", 0
		if issue.ExternalRef != nil {
			issueID = strings.TrimSpace(issue.ExternalRef.ExternalID)
			issueIID = issue.ExternalRef.ExternalIID
		}
		if resumed {
			b.WriteString("这是工蜂 Issue。对话里已有此前分析，不要再重拉原单和评论。\n")
		} else {
			fmt.Fprintf(&b, "这是工蜂 Issue。必须读取原单：调用 gongfeng__get_issue_detail（project_id=%s, issue_iid=%d），并同步 gongfeng__get_issue_notes（project_id=%s, issue_id=%s）看评论。\n",
				gfProject, issueIID, gfProject, issueID)
		}
	}
	if issue.ID == "" && issue.ExternalRef == nil && (len(tapd) > 0 || len(gongfeng) > 0 || len(iwiki) > 0) {
		b.WriteString("这是尚未建单的需求草稿，没有原单可拉。描述或对话里出现上述工具能读取的链接时必须先读取后再出题；没有链接不要为了找原单去 list。\n")
	}
	if len(iwiki) > 0 {
		urls := extractIWikiURLs(append([]string{issue.Title, issue.Description}, extras...)...)
		if resumed {
			b.WriteString("仅当出现尚未读取过的新 iWiki 链接时，用 iwiki__getDocument 读取；不要顺着文档里的链接继续追。\n")
		} else {
			b.WriteString("若需求正文、TAPD/工蜂原单或评论里出现 iWiki 链接，必须同步读取：用 iwiki__getDocument，arguments.docid 为文档 ID（可从 URL 的 pageId/docid 或 /pages/{id} 提取）。不要只根据链接标题猜测正文。文档里再引用的 iWiki 只读与当前缺口直接相关的一篇。\n")
		}
		if len(urls) > 0 {
			b.WriteString("当前描述中已发现 iWiki 链接：\n")
			for _, url := range urls {
				line := "- " + url
				if id := iwikiDocIDFromURL(url); id != "" {
					line += "（docid=" + id + "）"
				}
				b.WriteString(line + "\n")
			}
		}
	}
	if len(other) > 0 {
		b.WriteString("其他已安装的 HTTP MCP 按工具名直接调用，仅限只读。\n")
	}
	b.WriteString("资料足够后调用 submit_clarification。\n")
	return b.String()
}

func clarifyMCPWriteName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || n == "tools/list" {
		return false
	}
	switch {
	case strings.Contains(n, "create"), strings.Contains(n, "update"), strings.Contains(n, "delete"),
		strings.Contains(n, "save"), strings.Contains(n, "write"), strings.HasPrefix(n, "add_"),
		strings.Contains(n, "move"), strings.Contains(n, "rename"), strings.Contains(n, "copy"):
		return true
	default:
		return false
	}
}

func normalizeClarifyTAPDTool(tool string) string {
	switch strings.TrimSpace(tool) {
	case "comments_list":
		return "comments_get"
	case "stories_list":
		return "stories_get"
	case "bugs_list":
		return "bugs_get"
	case "tasks_list":
		return "tasks_get"
	default:
		return tool
	}
}

func wrapClarifyTAPDCall(call assistantMCPCall) assistantMCPCall {
	tool := normalizeClarifyTAPDTool(call.Tool)
	switch tool {
	case "", "tools/list", "lookup_tapd_tool", "lookup_tool_param_schema", "tql_syntax_reference":
		call.Tool = tool
		call.Arguments = stringifyTAPDIDs(assistantMCPCallArguments(call))
		return call
	}
	args := normalizeTAPDProxyArguments(tool, assistantMCPCallArguments(call))
	call.Tool = "proxy_execute_tool"
	call.Arguments = args
	call.Path, call.Query, call.Body = nil, nil, nil
	return call
}

func normalizeTAPDProxyArguments(tool string, args map[string]any) map[string]any {
	if args == nil {
		args = map[string]any{}
	}
	inner := firstObjectArg(args, "tool_args", "params", "arguments", "input", "args")
	leftovers := map[string]any{}
	for key, val := range args {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "tool_name", "tool_args", "params", "arguments", "input", "args":
			continue
		default:
			leftovers[key] = val
		}
	}
	if inner == nil {
		inner = leftovers
	} else {
		for key, val := range leftovers {
			if _, ok := inner[key]; !ok {
				inner[key] = val
			}
		}
	}
	name := normalizeClarifyTAPDTool(stringifyJSONID(args["tool_name"]))
	if name == "" && tool != "proxy_execute_tool" {
		name = tool
	}
	out := map[string]any{}
	if name != "" {
		out["tool_name"] = name
	}
	if len(inner) > 0 {
		out["tool_args"] = stringifyTAPDIDs(inner)
	}
	return out
}

func firstObjectArg(args map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if nested, ok := args[key].(map[string]any); ok && nested != nil {
			return nested
		}
	}
	return nil
}

func clarifyMCPCallWrite(kind string, call assistantMCPCall) bool {
	if clarifyMCPWriteName(call.Tool) {
		return true
	}
	if kind != "tapd" {
		return false
	}
	args := assistantMCPCallArguments(call)
	inner := strings.TrimSpace(fmt.Sprint(args["tool_name"]))
	if inner == "" {
		return false
	}
	return clarifyMCPWriteName(normalizeClarifyTAPDTool(inner))
}

func (h *streamHandlers) clarifyMCPUser(issue domain.Issue, responsible domain.User) domain.User {
	if h == nil || h.repo == nil {
		return responsible
	}
	project, ok := h.repo.Project(issue.ProjectID)
	if !ok || project.OwnerID <= 0 {
		return responsible
	}
	owner, ok := h.repo.User(project.OwnerID)
	if !ok || strings.TrimSpace(owner.TaihuPAT) == "" {
		return responsible
	}
	return owner
}

func (h *streamHandlers) dispatchClarifyMCPCall(asst *assistantHandlers, user domain.User, projectID string, call assistantMCPCall, emit func(event string, payload any)) (string, error) {
	if assistantPlatformServer(call.Server) && strings.TrimSpace(call.Tool) != "" && call.Tool != "tools/list" {
		return "", fmt.Errorf("澄清只允许调用当前项目的 HTTP MCP，不能调用平台写入工具")
	}
	server, ok := asst.lookupAssistantMCP(projectID, user.ID, call.Server)
	if !ok {
		return "", fmt.Errorf("当前项目没有名为 %q 的 MCP，或你无权使用", strings.TrimSpace(call.Server))
	}
	kind := classifyClarifyMCP(server)
	if kind == "tapd" {
		call = wrapClarifyTAPDCall(call)
	}
	if clarifyMCPCallWrite(kind, call) {
		name := strings.TrimSpace(call.Tool)
		if kind == "tapd" {
			if inner := strings.TrimSpace(fmt.Sprint(assistantMCPCallArguments(call)["tool_name"])); inner != "" {
				name = inner
			}
		}
		return "", fmt.Errorf("澄清只允许只读 MCP，拒绝 %s", name)
	}
	if emit != nil {
		detail := strings.TrimSpace(call.Tool)
		if kind == "tapd" {
			if inner := strings.TrimSpace(fmt.Sprint(assistantMCPCallArguments(call)["tool_name"])); inner != "" {
				detail = inner
			}
		}
		emit("thought", map[string]string{"kind": "tool", "tool": "MCP", "detail": server.Name + " " + detail})
	}
	caller := user
	if server.Scope != "personal" {
		caller = h.clarifyMCPUser(domain.Issue{ProjectID: projectID}, user)
	}
	return asst.callAssistantMCP(caller, projectID, call)
}

func (h *streamHandlers) openClarifyToolSession(ctx context.Context, user domain.User, issue domain.Issue, cancel context.CancelFunc) (*AgentToolSession, agentToolFiles, error) {
	return startAgentToolSession(ctx, user, issue.ProjectID, AgentToolClarify, assistantExecMode{}, issue.ID, h.clarifyHTTPMCPs(issue.ProjectID, user.ID), cancel, filepath.Join(clarifyCLIHome(user.ID), "mcp-sessions"))
}

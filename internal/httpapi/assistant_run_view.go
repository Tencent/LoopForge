package httpapi

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// Raw Run payloads open with the frozen workflow snapshot (often 10k+ chars,
// and every list item repeats it), so the assistant's tool-result budget used
// to end before node states. The assistant then could not see which node
// failed, why, or what a human gate was waiting for. These views keep the
// diagnosis and drop what the UI already shows elsewhere.
const (
	assistantRunListLimit  = 30
	assistantRunErrorLimit = 1500
	assistantRunOutputTail = 1200
	assistantRunBriefTail  = 240
)

type assistantCredentialFix struct {
	Field    string   `json:"field"`
	Label    string   `json:"label"`
	Owner    string   `json:"owner,omitempty"`
	Settings string   `json:"settings"`
	HelpURL  string   `json:"helpUrl,omitempty"`
	Steps    []string `json:"steps"`
}

type assistantRunProblem struct {
	NodeID     string                  `json:"nodeId,omitempty"`
	NodeName   string                  `json:"nodeName,omitempty"`
	Kind       string                  `json:"kind"`
	Reason     string                  `json:"reason"`
	Credential *assistantCredentialFix `json:"credential,omitempty"`
}

type assistantRunNodeView struct {
	ID          string             `json:"id"`
	Name        string             `json:"name,omitempty"`
	Type        string             `json:"type,omitempty"`
	Status      string             `json:"status"`
	Attempt     int                `json:"attempt,omitempty"`
	ReworkCount int                `json:"reworkCount,omitempty"`
	Model       string             `json:"model,omitempty"`
	Error       string             `json:"error,omitempty"`
	Feedback    string             `json:"feedback,omitempty"`
	Gate        *domain.GateResult `json:"gateResult,omitempty"`
	OutputTail  string             `json:"outputTail,omitempty"`
	StartedAt   *time.Time         `json:"startedAt,omitempty"`
	FinishedAt  *time.Time         `json:"finishedAt,omitempty"`
}

type assistantRunRepository struct {
	Name       string `json:"name,omitempty"`
	CloneURL   string `json:"cloneUrl,omitempty"`
	BaseBranch string `json:"baseBranch,omitempty"`
	WorkBranch string `json:"workBranch,omitempty"`
}

type assistantRunView struct {
	ID               string                   `json:"id"`
	WorkflowID       string                   `json:"workflowId,omitempty"`
	WorkflowName     string                   `json:"workflowName,omitempty"`
	Status           string                   `json:"status"`
	AdmissionState   string                   `json:"admissionState,omitempty"`
	CancelRequested  bool                     `json:"cancelRequested,omitempty"`
	PauseRequested   bool                     `json:"pauseRequested,omitempty"`
	ResponsibleUser  string                   `json:"responsibleUser,omitempty"`
	ExecutionContext domain.TelemetryContext  `json:"executionContext"`
	Input            string                   `json:"input,omitempty"`
	Repositories     []assistantRunRepository `json:"repositories,omitempty"`
	Problems         []assistantRunProblem    `json:"problems"`
	Nodes            []assistantRunNodeView   `json:"nodes"`
	RecentEvents     []domain.RunEvent        `json:"recentEvents,omitempty"`
	Usage            domain.TokenUsage        `json:"usage"`
	ArtifactCount    int                      `json:"artifactCount,omitempty"`
	CreatedAt        time.Time                `json:"createdAt"`
	UpdatedAt        time.Time                `json:"updatedAt"`
	Note             string                   `json:"note"`
}

type assistantRunBrief struct {
	ID           string                `json:"id"`
	WorkflowName string                `json:"workflowName,omitempty"`
	Status       string                `json:"status"`
	IssueID      string                `json:"issueId,omitempty"`
	IssueNumber  int64                 `json:"issueNumber,omitempty"`
	Progress     string                `json:"progress"`
	Active       []string              `json:"activeNodes,omitempty"`
	Problems     []assistantRunProblem `json:"problems,omitempty"`
	CreatedAt    time.Time             `json:"createdAt"`
	UpdatedAt    time.Time             `json:"updatedAt"`
}

var assistantCredentialRules = []struct {
	field, label string
	rule         *regexp.Regexp
	help         string
	steps        []string
}{
	{"codexApiKey", "Codex API Key", regexp.MustCompile(`(?i)Codex[^。\n]{0,16}API Key`), "", []string{
		"到 LoopForge「个人配置 → Codex API Key」填写 Key（及 Base URL）并保存，或把执行机器的认证方式切回本机登录",
		"回到运行重试失败节点",
	}},
	{"claudeCodeApiKey", "Claude Code API Key", regexp.MustCompile(`(?i)Claude Code[^。\n]{0,16}API Key`), "", []string{
		"到 LoopForge「个人配置 → Claude Code API Key」填写 Key（及 Base URL）并保存，或把执行机器的认证方式切回本机登录",
		"回到运行重试失败节点",
	}},
	{"codeBuddyApiKey", "CodeBuddy API Key", regexp.MustCompile(`(?i)CodeBuddy[^。\n]{0,20}(API Key|Key|凭据)|拒绝了你的 API Key`), "https://www.codebuddy.ai/profile/keys", []string{
		"确认 CodeBuddy 账号所在站点并在该站点创建 API Key：国际站 https://www.codebuddy.ai/profile/keys ，国内站 https://copilot.tencent.com/profile/ ，iOA https://tencent.sso.copilot.tencent.com/profile/keys",
		"到 LoopForge「个人配置 → CodeBuddy API Key」选择同一网络环境，粘贴 Key 并保存；DeepSeek、OpenAI 等第三方模型的 Key 不能用作 CodeBuddy Key",
		"回到运行重试失败节点",
	}},
	{"gitSshKey", "Git SSH 密钥", regexp.MustCompile(`Git SSH (密钥|私钥)`), "", []string{
		"把公钥添加到代码托管账号，并按需授予仓库写权限",
		"到 LoopForge「个人配置 → Git SSH 密钥」为该主机添加无口令私钥，同一主机的仓库自动共用",
		"请管理员信任仓库的 SSH 主机，然后重试",
	}},
	{"gitToken", "GitHub Git Token", regexp.MustCompile(`(?i)Git Token|工蜂[^。\n]{0,12}令牌|could not read Username|Authentication failed|HTTP Basic: Access denied|terminal prompts disabled|requested URL returned error: 40[13]`), "https://github.com/settings/tokens", []string{
		"打开 GitHub「Settings → Developer settings → Personal access tokens」新建令牌",
		"为目标仓库授予 Contents 和 Pull requests 的读写权限，创建后立即复制 token",
		"到 LoopForge「个人配置 → GitHub Git Token」粘贴，点「校验」，通过后自动保存",
		"回到运行重试失败节点",
	}},
}

// assistantCredentialProblem mirrors credentialHint in chatbot/src/credentials.js.
func assistantCredentialProblem(text, owner string) *assistantCredentialFix {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	for _, item := range assistantCredentialRules {
		if item.rule.MatchString(text) {
			return &assistantCredentialFix{Field: item.field, Label: item.label, Owner: owner, Settings: "个人配置 → " + item.label, HelpURL: item.help, Steps: item.steps}
		}
	}
	return nil
}

func (h *assistantHandlers) assistantRunOwner(run domain.Run) string {
	if h == nil || h.deps == nil || h.repo == nil || run.ExecutionContext.ResponsibleUserID <= 0 {
		return ""
	}
	if user, ok := h.repo.User(run.ExecutionContext.ResponsibleUserID); ok {
		return strings.TrimSpace(user.LoginName)
	}
	return ""
}

// assistantRunResult reshapes Run payloads returned to the assistant. Other
// operations and unparsable bodies pass through unchanged.
func (h *assistantHandlers) assistantRunResult(op string, body []byte) []byte {
	switch op {
	case "runs_get":
		var run domain.Run
		if json.Unmarshal(body, &run) != nil || run.ID == "" {
			return body
		}
		if out, err := json.Marshal(h.assistantRunDetail(run)); err == nil {
			return out
		}
	case "runs_list", "issues_runs_list":
		var runs []domain.Run
		if json.Unmarshal(body, &runs) != nil {
			return body
		}
		sort.SliceStable(runs, func(i, j int) bool { return runs[i].UpdatedAt.After(runs[j].UpdatedAt) })
		shown := runs
		if len(shown) > assistantRunListLimit {
			shown = shown[:assistantRunListLimit]
		}
		items := make([]assistantRunBrief, 0, len(shown))
		for _, run := range shown {
			items = append(items, h.assistantRunBrief(run))
		}
		out, err := json.Marshal(map[string]any{
			"total": len(runs),
			"shown": len(items),
			"runs":  items,
			"note":  "按更新时间倒序的摘要；problems 是失败或等待确认的节点及原因。要看某条运行的完整诊断（节点错误、门禁原因、凭证修复步骤）请调用 runs_get。",
		})
		if err == nil {
			return out
		}
	}
	return body
}

func (h *assistantHandlers) assistantRunDetail(run domain.Run) assistantRunView {
	owner := h.assistantRunOwner(run)
	view := assistantRunView{
		ID:               run.ID,
		WorkflowID:       run.WorkflowID,
		WorkflowName:     run.WorkflowSnapshot.Name,
		Status:           run.Status,
		AdmissionState:   run.AdmissionState,
		CancelRequested:  run.CancelRequested,
		PauseRequested:   run.PauseRequested,
		ResponsibleUser:  owner,
		ExecutionContext: run.ExecutionContext,
		Input:            truncateRunes(run.Input, 800),
		Usage:            run.Usage,
		ArtifactCount:    run.ArtifactCount,
		CreatedAt:        run.CreatedAt,
		UpdatedAt:        run.UpdatedAt,
		Note:             "已省略 workflowSnapshot 等大字段。problems 列出失败 / 等待人工确认的节点与原因；credential 表示需要运行责任人本人配置的个人凭证及步骤。节点执行日志用 runs_logs_list，遥测用 runs_telemetry_list。",
	}
	for _, rc := range run.RepositoryContexts() {
		view.Repositories = append(view.Repositories, assistantRunRepository{Name: rc.Name, CloneURL: rc.CloneURL, BaseBranch: repositoryBaseBranchName(rc), WorkBranch: rc.WorkBranch})
	}
	problems := map[string]bool{}
	for _, item := range assistantRunProblems(run, owner, assistantRunErrorLimit) {
		problems[item.NodeID] = true
		view.Problems = append(view.Problems, item)
	}
	for _, id := range assistantRunNodeOrder(run) {
		node := run.Nodes[id]
		def := assistantRunNodeDefinition(run, id)
		item := assistantRunNodeView{ID: id, Name: def.Name, Type: def.Type, Status: "pending"}
		if node != nil {
			item.Status = node.Status
			item.Attempt = node.Attempt
			item.ReworkCount = node.ReworkCount
			item.Model = node.Model
			item.Error = truncateRunes(node.Error, assistantRunErrorLimit)
			item.Feedback = truncateRunes(node.Feedback, 600)
			item.Gate = assistantGateView(node.GateResult)
			tail := assistantRunBriefTail
			if problems[id] {
				tail = assistantRunOutputTail
			}
			item.OutputTail = tailRunes(node.Output, tail)
			if !node.StartedAt.IsZero() {
				started := node.StartedAt
				item.StartedAt = &started
			}
			if !node.FinishedAt.IsZero() {
				finished := node.FinishedAt
				item.FinishedAt = &finished
			}
		}
		view.Nodes = append(view.Nodes, item)
	}
	if n := len(run.Events); n > 0 {
		view.RecentEvents = append([]domain.RunEvent(nil), run.Events[max(0, n-8):]...)
	}
	if view.Problems == nil {
		view.Problems = []assistantRunProblem{}
	}
	return view
}

func (h *assistantHandlers) assistantRunBrief(run domain.Run) assistantRunBrief {
	brief := assistantRunBrief{
		ID:           run.ID,
		WorkflowName: run.WorkflowSnapshot.Name,
		Status:       run.Status,
		IssueID:      run.ExecutionContext.IssueID,
		IssueNumber:  int64(run.ExecutionContext.IssueNumber),
		CreatedAt:    run.CreatedAt,
		UpdatedAt:    run.UpdatedAt,
	}
	order := assistantRunNodeOrder(run)
	done := 0
	for _, id := range order {
		node := run.Nodes[id]
		if node == nil {
			continue
		}
		switch node.Status {
		case "succeeded", "skipped":
			done++
		case "running", "waiting_approval":
			name := assistantRunNodeDefinition(run, id).Name
			if name == "" {
				name = id
			}
			brief.Active = append(brief.Active, name+"（"+node.Status+"）")
		}
	}
	brief.Progress = strconv.Itoa(done) + "/" + strconv.Itoa(len(order))
	if run.Status != "succeeded" && run.Status != "canceled" && run.Status != "cancelled" {
		brief.Problems = assistantRunProblems(run, h.assistantRunOwner(run), 300)
	}
	return brief
}

// assistantRunProblems lists the nodes a human has to look at: failures with
// their error and human gates with what they are waiting for.
func assistantRunProblems(run domain.Run, owner string, limit int) []assistantRunProblem {
	var out []assistantRunProblem
	for _, id := range assistantRunNodeOrder(run) {
		node := run.Nodes[id]
		if node == nil {
			continue
		}
		def := assistantRunNodeDefinition(run, id)
		name := def.Name
		if name == "" {
			name = id
		}
		switch node.Status {
		case "failed":
			reason := strings.TrimSpace(node.Error)
			if reason == "" && node.GateResult != nil {
				reason = strings.TrimSpace(node.GateResult.Reason)
			}
			if reason == "" {
				reason = "节点失败但没有记录错误，请用 runs_logs_list 查看执行日志"
			}
			out = append(out, assistantRunProblem{NodeID: id, NodeName: name, Kind: "failed", Reason: truncateRunes(reason, limit), Credential: assistantCredentialProblem(reason, owner)})
		case "waiting_approval":
			reason := ""
			if gate := node.GateResult; gate != nil {
				reason = strings.TrimSpace(strings.Join(nonEmpty(gate.Reason, gate.ActionRequired), "；需要处理："))
			}
			if reason == "" {
				reason = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(node.Error), "agent requested human confirmation:"))
			}
			if reason == "" && def.Type == "approval" {
				reason = domain.ApprovalWaitReason(run, id)
			}
			if reason == "" {
				reason = "等待人工确认"
			}
			out = append(out, assistantRunProblem{NodeID: id, NodeName: name, Kind: "waiting_approval", Reason: truncateRunes(reason, limit), Credential: assistantCredentialProblem(reason, owner)})
		}
	}
	if run.IsWorkspace() {
		out = append(out, workspacePreparationProblems(run, owner, limit)...)
	}
	if len(out) == 0 && run.Status == "failed" {
		reason := "运行失败，但没有节点记录错误"
		for i := len(run.Events) - 1; i >= 0; i-- {
			if msg := strings.TrimSpace(run.Events[i].Message); msg != "" {
				reason = msg
				break
			}
		}
		out = append(out, assistantRunProblem{Kind: "failed", Reason: truncateRunes(reason, limit), Credential: assistantCredentialProblem(reason, owner)})
	}
	return out
}

// workspacePreparationStages are the steps the engine records on a run as
// workspace.<stage>.<started|progress|completed|failed> events, in the order
// they run.
var workspacePreparationStages = []struct{ key, name string }{
	{"clone", "克隆仓库"},
	{"image", "构建沙箱镜像"},
	{"preparation", "工作区准备脚本"},
}

// workspacePreparationProblems reports each preparation stage whose latest
// attempt failed. A conversation workspace has no nodes, so without this a
// failed clone or image build left problems empty. A later start or success
// of the same stage clears the failure.
func workspacePreparationProblems(run domain.Run, owner string, limit int) []assistantRunProblem {
	failed := map[string]string{}
	for _, event := range run.Events {
		rest, ok := strings.CutPrefix(event.Type, "workspace.")
		if !ok {
			continue
		}
		stage, kind, ok := strings.Cut(rest, ".")
		if !ok {
			continue
		}
		switch kind {
		case "failed":
			failed[stage] = strings.TrimSpace(event.Message)
		case "started", "completed":
			delete(failed, stage)
		}
	}
	var out []assistantRunProblem
	for _, stage := range workspacePreparationStages {
		reason, ok := failed[stage.key]
		if !ok {
			continue
		}
		if reason == "" {
			reason = stage.name + "失败"
		}
		out = append(out, assistantRunProblem{NodeName: stage.name, Kind: "failed", Reason: truncateRunes(reason, limit), Credential: assistantCredentialProblem(reason, owner)})
	}
	return out
}

func assistantRunNodeOrder(run domain.Run) []string {
	seen := map[string]bool{}
	var order []string
	for _, node := range run.WorkflowSnapshot.Nodes {
		if node.ID != "" && !seen[node.ID] {
			seen[node.ID] = true
			order = append(order, node.ID)
		}
	}
	var extra []string
	for id := range run.Nodes {
		if !seen[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	return append(order, extra...)
}

func assistantRunNodeDefinition(run domain.Run, id string) domain.WorkflowNode {
	for _, node := range run.WorkflowSnapshot.Nodes {
		if node.ID == id {
			return node
		}
	}
	return domain.WorkflowNode{ID: id}
}

func assistantGateView(gate *domain.GateResult) *domain.GateResult {
	if gate == nil {
		return nil
	}
	out := *gate
	out.Reason = truncateRunes(gate.Reason, 800)
	out.ActionRequired = truncateRunes(gate.ActionRequired, 600)
	out.Summary = truncateRunes(gate.Summary, 600)
	if len(out.Findings) > 6 {
		out.Findings = out.Findings[:6]
	}
	findings := make([]domain.GateFinding, 0, len(out.Findings))
	for _, finding := range out.Findings {
		finding.Evidence = truncateRunes(finding.Evidence, 200)
		findings = append(findings, finding)
	}
	out.Findings = findings
	return &out
}

func repositoryBaseBranchName(rc domain.RepositoryContext) string {
	if branch := strings.TrimSpace(rc.BaseBranch); branch != "" {
		return branch
	}
	return strings.TrimSpace(rc.DefaultBranch)
}

func tailRunes(text string, max int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= max {
		return string(runes)
	}
	return "…" + string(runes[len(runes)-max:])
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

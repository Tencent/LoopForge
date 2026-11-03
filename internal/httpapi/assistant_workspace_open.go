package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"agentflow/internal/domain"
)

const workspaceOpenSchema = `{"type":"object","properties":{"goal":{"type":"string","minLength":1,"maxLength":500,"description":"一句话说明要做什么"},"issueId":{"type":"string","maxLength":128,"description":"切到这个需求最近一次 SOP 运行的工作区；需求必须跑过 SOP"},"workspaceRunId":{"type":"string","maxLength":128,"description":"切回本对话之前开过的工作区，或某次 SOP 运行的工作区"},"new":{"type":"boolean","description":"新开一个对话工作区"},"sandboxProfileId":{"type":"string","maxLength":128,"description":"新开时的运行环境 id，按要改的代码选择；不填使用 generic-docker"},"repositories":{"type":"array","maxItems":20,"items":{"type":"string","maxLength":200},"description":"新开时要检出的仓库名或 id；不填检出项目全部仓库"},"requirement":{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":200,"description":"一句话写清要交付的需求"},"description":{"type":"string","maxLength":20000,"description":"背景、要改什么、验收要点"}},"required":["title"],"additionalProperties":false,"description":"用户这次是要改代码交付一个新需求（修 bug、加功能、改文案等，最后会提交 MR），而本对话还没有关联未完成的需求时填写。系统会在项目需求列表里创建这个需求，关联到本对话、工作区和之后提交的 MR，并计入数据统计。只读评审、排查、问答不要填；切到 SOP 运行时不生效"}},"required":["goal"],"additionalProperties":false}`

func workspaceOpenDescriptor() agentToolDescriptor {
	return agentToolDescriptor{
		Name:        "workbench_workspace_open",
		Description: "打开或切换本对话绑定的工作区。用户要看代码、改代码（改文案、修小 bug、补测试等）或跑命令时调用，之后用 workbench_workspace_tree/read/write/command 操作并验证，用户要提交时调用 workbench_workspace_submit_mr（只为有改动的仓库发起 MR）。不走 SOP，不需要用户另开会话。一个对话同一时间只绑定一个工作区，用哪个由你按用户当前实际要处理的需求决定（@ 只是线索）：issueId、workspaceRunId、new 最多传一个；都不传时继续用当前绑定的工作区，还没有就新开。issueId 切到该需求最近一次 SOP 运行的工作区（已回收的会恢复快照），SOP 执行中可用 workbench_workspace_sop 暂停、继续或接管；workspaceRunId 切回本对话之前开过的工作区；new 新开一个对话工作区，按 sandboxProfileId 和 repositories 检出、每个仓库各建新工作分支，环境和仓库创建后不再变化。没跑过 SOP 的新需求，当前工作区的仓库和环境合适就沿用，不合适就新开。切换后旧工作区解绑，空闲时保存快照并回收，切回时恢复。用户要改代码交付新需求时传 requirement，让需求进入需求列表和数据统计。",
		InputSchema: json.RawMessage(workspaceOpenSchema),
		Kind:        "workspace",
	}
}

// workspaceOpenDescriptorFor lists the environments and repositories this
// conversation may choose from, so the model can pick without extra lookups.
func (h *agentToolGateway) workspaceOpenDescriptorFor(s *AgentToolSession) agentToolDescriptor {
	d := workspaceOpenDescriptor()
	var b strings.Builder
	b.WriteString(d.Description)
	if fixed := strings.TrimSpace(s.SandboxProfileID); fixed != "" {
		fmt.Fprintf(&b, "\n\n用户已在高级设置中指定运行环境 %s，不要填 sandboxProfileId。", fixed)
	} else {
		b.WriteString("\n\n可选运行环境（sandboxProfileId，根据仓库语言和工具链选择，拿不准用 generic-docker）：")
		for _, profile := range h.repo.ConversationSandboxes(s.ProjectID) {
			name := profile.Name
			if df, ok := h.repo.Dockerfile(profile.DockerfileID); ok && df.Name != "" && df.Name != name {
				name += "，镜像 " + df.Name
			}
			fmt.Fprintf(&b, "\n- %s：%s", profile.ID, truncateRunes(name, 80))
		}
	}
	b.WriteString("\n\n项目仓库（repositories，只选这次要改或要运行的仓库，拿不准就不填、全部检出）：")
	for _, r := range h.repo.Repositories() {
		if r.ProjectID == s.ProjectID {
			fmt.Fprintf(&b, "\n- %s", r.Name)
			if path := strings.Trim(r.GongfengProjectID, "/"); path != "" {
				fmt.Fprintf(&b, "（%s）", path)
			}
		}
	}
	d.Description = b.String()
	return d
}

// usableConversationWorkspace reports whether a bound workspace can still be
// used by a conversation of projectID.
func usableConversationWorkspace(run domain.Run, projectID string) bool {
	return run.ExecutionContext.ProjectID == projectID && run.IsWorkspace() && !domain.TerminalRunStatus(run.Status)
}

func repositoryMatches(id, name, gongfengPath, want string) bool {
	want = strings.Trim(strings.TrimSpace(want), "/")
	path := strings.Trim(gongfengPath, "/")
	return id == want || strings.EqualFold(name, want) || path != "" && (strings.EqualFold(path, want) || strings.HasSuffix(strings.ToLower(path), "/"+strings.ToLower(want)))
}

// selectConversationRepositories returns the wanted repositories of the
// project, or all of them when nothing is wanted; the default repository
// comes first.
func selectConversationRepositories(project domain.Project, all []domain.Repository, want []string) ([]domain.RepositoryContext, error) {
	var candidates []domain.Repository
	names := []string{}
	for _, r := range all {
		if r.ProjectID != project.ID {
			continue
		}
		if r.ID == project.DefaultRepositoryID {
			candidates = append([]domain.Repository{r}, candidates...)
		} else {
			candidates = append(candidates, r)
		}
		names = append(names, r.Name)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("项目「%s」还没有配置代码仓库，无法开工作区；请项目 Owner 在项目设置里添加仓库", project.Name)
	}
	selected := map[string]bool{}
	for _, w := range want {
		found := false
		for _, r := range candidates {
			if repositoryMatches(r.ID, r.Name, r.GongfengProjectID, w) {
				selected[r.ID], found = true, true
			}
		}
		if !found {
			return nil, fmt.Errorf("项目里没有仓库「%s」，可选：%s", strings.TrimSpace(w), strings.Join(names, "、"))
		}
	}
	contexts := []domain.RepositoryContext{}
	for _, r := range candidates {
		if len(want) == 0 || selected[r.ID] {
			contexts = append(contexts, repositoryRunContext(r))
		}
	}
	return contexts, nil
}

// missingRepositories lists the wanted repositories a reused workspace did not
// check out.
func missingRepositories(run domain.Run, want []string) []string {
	missing := []string{}
	for _, w := range want {
		found := false
		for _, rc := range run.RepositoryContexts() {
			found = found || repositoryMatches(rc.RepositoryID, rc.Name, rc.GongfengProjectID, w)
		}
		if !found {
			missing = append(missing, strings.TrimSpace(w))
		}
	}
	return missing
}

type workspaceOpenChoice struct {
	Goal         string
	SandboxID    string
	Repositories []string
	// At most one of IssueID, WorkspaceRunID and New picks the workspace;
	// none keeps the bound one.
	IssueID        string
	WorkspaceRunID string
	New            bool
	// Requirement is the new requirement Main Bot identified for a
	// conversation workspace.
	Requirement conversationRequirement
}

func parseWorkspaceOpenChoice(args map[string]any) (workspaceOpenChoice, error) {
	choice := workspaceOpenChoice{}
	text := func(key string) string {
		value, _ := args[key].(string)
		return strings.TrimSpace(value)
	}
	choice.Goal = text("goal")
	if choice.Goal == "" {
		return choice, fmt.Errorf("goal is required")
	}
	choice.SandboxID = text("sandboxProfileId")
	choice.IssueID = text("issueId")
	choice.WorkspaceRunID = text("workspaceRunId")
	choice.New, _ = args["new"].(bool)
	requirement, err := parseConversationRequirement(args)
	if err != nil {
		return choice, err
	}
	choice.Requirement = requirement
	targets := 0
	for _, set := range []bool{choice.IssueID != "", choice.WorkspaceRunID != "", choice.New} {
		if set {
			targets++
		}
	}
	if targets > 1 {
		return choice, fmt.Errorf("issueId、workspaceRunId、new 最多传一个")
	}
	if raw, ok := args["repositories"]; ok && raw != nil {
		items, ok := raw.([]any)
		if !ok {
			return choice, fmt.Errorf("repositories 必须是仓库名数组")
		}
		for _, item := range items {
			name, ok := item.(string)
			if !ok {
				return choice, fmt.Errorf("repositories 必须是仓库名数组")
			}
			if name = strings.TrimSpace(name); name != "" {
				choice.Repositories = append(choice.Repositories, name)
			}
		}
	}
	return choice, nil
}

// openConversationWorkspace binds the workspace Main Bot picked to an ordinary
// conversation and checks the code out so failures such as a missing Git
// Token surface immediately.
func (h *agentToolGateway) openConversationWorkspace(ctx context.Context, s *AgentToolSession, args map[string]any) (string, error) {
	if !s.canOpenConversationWorkspace() {
		return "", fmt.Errorf("任务会话直接使用任务工作区，不需要再开")
	}
	choice, err := parseWorkspaceOpenChoice(args)
	if err != nil {
		return "", err
	}
	bound, err := h.bindConversationWorkspace(ctx, s, choice)
	if err != nil {
		return "", err
	}
	run := bound.Run
	repositories := []map[string]string{}
	for _, rc := range run.RepositoryContexts() {
		repositories = append(repositories, map[string]string{"name": rc.Name, "dir": rc.WorkspaceDir, "baseBranch": repositoryBaseBranchName(rc), "workBranch": rc.WorkBranch})
	}
	out := map[string]any{"runId": run.ID, "reused": bound.Reused, "created": bound.Created, "sandboxProfileId": run.WorkflowSnapshot.SandboxProfileID, "repositories": repositories}
	var notice strings.Builder
	switch {
	case !run.IsWorkspace():
		out["sopRunId"], out["sopStatus"], out["issueId"] = run.ID, run.Status, run.ExecutionContext.IssueID
		fmt.Fprintf(&notice, "本对话现在绑定需求 %s 的 SOP 运行 %s 的工作区。", run.ExecutionContext.IssueID, run.ID)
	case bound.Created:
		notice.WriteString("已为本对话新开工作区并绑定。")
	case bound.Reused:
		notice.WriteString("继续使用本对话当前绑定的工作区。")
	default:
		notice.WriteString("已切回本对话之前开过的工作区。")
	}
	notice.WriteString("每个仓库检出在 repositories[].dir 目录下，文件路径和命令都相对工作区根目录（例如 cd <dir> && go test ./...）。")
	if run.IsWorkspace() {
		notice.WriteString("改完先用 workbench_workspace_command 跑相关测试；用户要求提交时调用 workbench_workspace_submit_mr。不要在终端里 git push。")
	} else {
		notice.WriteString(borrowedSOPNotice(run))
	}
	if !bound.Created {
		if missing := missingRepositories(run, choice.Repositories); len(missing) > 0 {
			out["missingRepositories"] = missing
			notice.WriteString("这个工作区没有检出 missingRepositories，仓库已固定；需要这些仓库时传 new 新开一个工作区。")
		}
		if choice.SandboxID != "" && choice.SandboxID != run.WorkflowSnapshot.SandboxProfileID {
			notice.WriteString("这个工作区的环境已固定为 sandboxProfileId，本次选择的环境没有生效；需要别的环境时传 new 新开一个工作区。")
		}
	}
	if run.IsWorkspace() {
		issue, created, err := h.linkConversationRequirement(s, run, choice.Requirement, false)
		if err != nil {
			log.Printf("link conversation workspace %s to its requirement: %v", run.ID, err)
		}
		if issue.ID != "" {
			out["issueId"], out["issueTitle"] = issue.ID, issue.Title
			if created {
				fmt.Fprintf(&notice, "已在项目需求列表创建需求 %s「%s」，关联到本对话和这个工作区，之后提交的 MR 会挂到这个需求上。", issue.ID, truncateRunes(issue.Title, 60))
			} else {
				fmt.Fprintf(&notice, "这个工作区关联需求 %s「%s」。", issue.ID, truncateRunes(issue.Title, 60))
			}
		}
	}
	if previous, ok := h.repo.RunState(bound.Previous); ok {
		out["previousRunId"] = previous.ID
		fmt.Fprintf(&notice, "之前绑定的工作区 %s 已解绑，空闲时保存快照并回收，切回时恢复。", previous.ID)
		if bound.CheckpointError != "" {
			out["previousCheckpointError"] = bound.CheckpointError
			notice.WriteString("切换前保存它的快照没有成功，它的文件仍原样保留，回收前会再次保存，保存成功之前不会回收。")
		}
		if !previous.IsWorkspace() && !domain.TerminalRunStatus(previous.Status) && previous.PauseRequested {
			notice.WriteString("它所属的 SOP 运行仍处于暂停状态，要让它继续，先切回去再调用 workbench_workspace_sop resume。")
		}
	}
	treeCtx := context.WithValue(ctx, mainBotWorkspaceKey{}, run.ID)
	tree, err := h.callMainBotWorkspace(treeCtx, s, "workbench_workspace_tree", map[string]any{})
	if err != nil {
		return "", fmt.Errorf("工作区已绑定（%s），但准备代码时失败：%v", run.ID, err)
	}
	out["files"] = json.RawMessage(truncateJSONForTool(tree, 6000))
	out["notice"] = notice.String()
	raw, _ := json.Marshal(out)
	return string(raw), nil
}

type conversationWorkspaceBinding struct {
	Run domain.Run
	// Reused is set when Run was already bound; Created when this call opened
	// it.
	Reused  bool
	Created bool
	// Previous is the workspace this call switched away from.
	Previous string
	// CheckpointError is why saving Previous before the switch failed.
	CheckpointError string
}

// bindConversationWorkspace binds the workspace the choice picks: an issue's
// SOP run, a workspace the conversation opened earlier, the bound one, or a
// new one in the user's fixed sandbox, else the model's choice, else
// generic-docker. The workspace it switches away from is released.
func (h *agentToolGateway) bindConversationWorkspace(ctx context.Context, s *AgentToolSession, choice workspaceOpenChoice) (conversationWorkspaceBinding, error) {
	if s.workspaceTurn != nil {
		s.workspaceTurn.operations.Lock()
		defer s.workspaceTurn.operations.Unlock()
		// Refuse before a new workspace run is created that nothing would bind.
		if err := s.workspaceTurn.usable(); err != nil {
			return conversationWorkspaceBinding{}, err
		}
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	project, ok := h.repo.Project(s.ProjectID)
	if !ok {
		return conversationWorkspaceBinding{}, fmt.Errorf("项目不存在")
	}
	conv, _ := h.repo.AssistantSession(s.ChatOwnerID, s.ChatSessionID)
	binding := conversationWorkspaceBinding{}
	switch {
	case choice.IssueID != "":
		issue, ok := h.repo.Issue(choice.IssueID)
		if !ok || issue.ProjectID != s.ProjectID {
			return binding, fmt.Errorf("需求 %s 不存在或不属于当前项目", choice.IssueID)
		}
		if binding.Run, ok = issueSOPRun(h.deps, s.ProjectID, issue.ID); !ok {
			return binding, fmt.Errorf("需求 %s 还没跑过 SOP，没有它的工作区：沿用当前工作区，或传 new 新开一个", issue.ID)
		}
	case choice.WorkspaceRunID != "":
		run, ok := h.repo.RunState(choice.WorkspaceRunID)
		if !ok || !conversationMayBind(conv, run, s.ProjectID) {
			return binding, fmt.Errorf("工作区 %s 不是本对话开过的可用工作区，也不是本项目的 SOP 运行", choice.WorkspaceRunID)
		}
		binding.Run = run
	case !choice.New && s.workspaceRunID != "":
		run, ok := h.repo.RunState(s.workspaceRunID)
		if !ok || !conversationMayBind(conv, run, s.ProjectID) {
			return binding, fmt.Errorf("当前绑定的工作区 %s 已不可用：传 new 新开一个，或切到其他工作区", s.workspaceRunID)
		}
		binding.Run = run
	default:
		run, err := h.startConversationWorkspace(s, project, choice)
		if err != nil {
			return binding, err
		}
		binding.Run, binding.Created = run, true
	}
	if binding.Run.ID == s.workspaceRunID {
		binding.Reused = true
		return binding, nil
	}
	if s.workspaceTurn != nil {
		// A failed save only defers reclamation: the idle sweep saves a released
		// workspace again before reclaiming it and keeps its files if that save
		// fails too. Blocking the switch would strand the turn on a workspace that
		// cannot be saved (too large, or slower than the save timeout).
		if err := s.workspaceTurn.checkpoint(ctx); errors.Is(err, errAssistantTurnClosed) {
			if binding.Created {
				_ = h.engine.ReleaseConversationWorkspace(binding.Run.ID)
			}
			return conversationWorkspaceBinding{}, err
		} else if err != nil {
			binding.CheckpointError = err.Error()
			log.Printf("checkpoint conversation workspace before switch session=%s: %v", s.ChatSessionID, err)
		}
	}
	binding.Previous = s.workspaceRunID
	s.workspaceRunID = binding.Run.ID
	// The workspace still serves this turn if the binding cannot be saved; the
	// end-of-turn save writes it again.
	if err := h.repo.SetAssistantSessionWorkspaceRun(s.ChatOwnerID, s.ChatSessionID, binding.Run.ID, binding.Run.IsWorkspace()); err != nil {
		log.Printf("bind conversation workspace session=%s run=%s: %v", s.ChatSessionID, binding.Run.ID, err)
	}
	if binding.Previous != "" && h.engine != nil {
		if err := h.engine.ReleaseConversationWorkspace(binding.Previous); err != nil {
			log.Printf("release conversation workspace session=%s run=%s: %v", s.ChatSessionID, binding.Previous, err)
		}
	}
	return binding, nil
}

func (h *agentToolGateway) startConversationWorkspace(s *AgentToolSession, project domain.Project, choice workspaceOpenChoice) (domain.Run, error) {
	repositories, err := selectConversationRepositories(project, h.repo.Repositories(), choice.Repositories)
	if err != nil {
		return domain.Run{}, err
	}
	chosen := strings.TrimSpace(s.SandboxProfileID)
	if chosen == "" {
		chosen = choice.SandboxID
	}
	sandbox, err := h.repo.ResolveConversationSandbox(project.ID, chosen)
	if err != nil {
		return domain.Run{}, err
	}
	if h.engine == nil {
		return domain.Run{}, fmt.Errorf("执行引擎不可用")
	}
	return h.engine.StartConversationWorkspace("对话改代码："+truncateRunes(choice.Goal, 200), repositories, domain.TelemetryContext{ProjectID: project.ID, ResponsibleUserID: s.UserID}, sandbox.ID)
}

func truncateJSONForTool(raw string, limit int) string {
	if len(raw) <= limit {
		return raw
	}
	data, _ := json.Marshal(map[string]any{"truncated": true, "preview": truncateRunes(raw, limit)})
	return string(data)
}

// discardConversationWorkspace removes a workspace a deleted conversation
// opened. Only that conversation could reach it (forks do not inherit
// it), so keeping it would leave an idle workspace run in the run
// list for every deleted chat. Something still running in it, or a paused or
// queued run, is left for the run page instead of being cut off.
func (h *assistantHandlers) discardConversationWorkspace(runID string) {
	runID = strings.TrimSpace(runID)
	if runID == "" || h.engine == nil {
		return
	}
	run, ok := h.repo.RunState(runID)
	if !ok || !run.IsWorkspace() || run.PauseRequested {
		return
	}
	if !domain.TerminalRunStatus(run.Status) {
		busy := run.Status != "waiting"
		for _, node := range run.Nodes {
			busy = busy || node != nil && (node.Status == "running" || node.Status == "waiting_approval")
		}
		if busy {
			log.Printf("keep workspace run=%s of a deleted conversation: it is %s or has work in progress", runID, run.Status)
			return
		}
		if err := h.engine.Cancel(runID); err != nil {
			log.Printf("end workspace run=%s of a deleted conversation: %v", runID, err)
			return
		}
	}
	if err := h.engine.DeleteRun(runID); err != nil {
		log.Printf("delete workspace run=%s of a deleted conversation: %v", runID, err)
	}
}

func repositoryRunContext(r domain.Repository) domain.RepositoryContext {
	return domain.RepositoryContext{RepositoryID: r.ID, Provider: r.Provider, Name: r.Name, GongfengProjectID: r.GongfengProjectID, WebURL: r.WebURL, CloneURL: r.CloneURL, BaseBranch: r.DefaultBranch, DefaultBranch: r.DefaultBranch, LocalPath: r.LocalPath, PipelineProjectID: r.PipelineProjectID, PipelineDefID: r.PipelineDefID, PipelineURLPattern: r.PipelineURLPattern}
}

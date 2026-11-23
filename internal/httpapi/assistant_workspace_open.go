package httpapi

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"slices"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// A conversation workspace is an environment, not a checkout: Main Bot picks
// the sandbox profile (or workspaceRunId to bind a same-project SOP run or an
// earlier workspace of this conversation on the same execution machine). Empty
// workspaces start empty so the agent clones, branches, commits and pushes with
// its own git using the requester's Git credentials.
const workspaceOpenSchema = `{"type":"object","properties":{"sandboxProfileId":{"type":"string","maxLength":128,"description":"运行环境（沙箱配置）id；用于新开或切回本对话在该环境自己的工作区，不填沿用当前工作区的环境（没有工作区时用会话设置、AI 助手设置里倾向的环境或 generic-docker）。传入 workspaceRunId 接入已有工作区/SOP 时沿用原运行冻结的沙箱环境，不要传不同的 sandboxProfileId"},"workspaceRunId":{"type":"string","maxLength":128,"description":"可选：要直接接入的同项目 SOP 运行 id（或本对话之前开过的工作区 id）。要求与当前对话在同一执行机器上：会直接绑定并操作该原工作区现场（包括未推送改动与中间产物，改完后也可回 SOP 从后续节点重跑或继续；已回收工作区会自动恢复快照；传子 SOP 时自动定位到父工作区）。日常继续当前工作区或只切换对话自身环境时不要传"}},"additionalProperties":false}`

func workspaceOpenDescriptor() agentToolDescriptor {
	return agentToolDescriptor{
		Name:        "workbench_workspace_open",
		Description: "进入工作区模式：在指定的运行环境（沙箱配置）里打开本对话的工作区，或直接接入某次同项目 SOP 运行的原工作区，之后用 CLI 原生的文件、补丁和命令工具工作。需要看代码、改代码、跑命令、构建、访问 GitHub 或 GitLab（查 PR/MR、开 PR/MR、合并、评论等）时调用。新开的普通工作区是空白可写目录，可以直接创建文件、运行命令和预览。涉及已有仓库时，自行 git clone、建分支、提交、推送并开 PR/MR。若要在普通新对话里接着某次历史/暂停 SOP 的原工作区现场干活（保留未推送改动与产物，且支持之后回原 SOP 重跑节点或继续），传 workspaceRunId 直接接入（需与当前对话在同一执行机器，已回收的快照会自动恢复；删除本对话不会删除借用的 SOP 工作区）。平台已按本轮发言人的个人凭证配置好 Git（已配置主机的 HTTPS 自动认证，SSH 用个人 SSH 密钥），gh 和 glab 会按仓库所在主机拿到令牌，环境里没有时可以自己安装；其他 API 需要令牌时用 git credential fill 按主机获取。不要打印、回显或写入任何令牌。一个对话同一时间只有一个活动工作区：不传参数或传相同环境时继续用当前工作区，传不同的 sandboxProfileId 就切换过去；切走的工作区解绑，空闲时保存快照后回收。返回 handoff=true 时只回复一句「好的。」结束本阶段，平台会在工作区里续接同一原生会话。项目对话中开好的 GitHub PR 或 GitLab MR 由平台在本轮结束时自动登记到需求并跟踪合入。子 Agent 不能调用本工具。",
		InputSchema: json.RawMessage(workspaceOpenSchema),
		Kind:        "workspace",
	}
}

// workspaceOpenDescriptorFor lists the environments this conversation may
// choose from, so the model can pick without extra lookups.
func (h *agentToolGateway) workspaceOpenDescriptorFor(s *AgentToolSession) agentToolDescriptor {
	d := workspaceOpenDescriptor()
	var b strings.Builder
	b.WriteString(d.Description)
	if fixed := strings.TrimSpace(s.SandboxProfileID); fixed != "" {
		fmt.Fprintf(&b, "\n\n用户已在高级设置中指定运行环境 %s，不要填 sandboxProfileId。", fixed)
	} else {
		choices, preferred := h.conversationSandboxChoices(s)
		fmt.Fprintf(&b, "\n\n可选运行环境（sandboxProfileId，%s）：", workspaceSandboxGuide(choices, preferred))
		for _, profile := range choices {
			name := profile.Name
			if profile.Driver != "" && profile.Driver != domain.SandboxDriverDocker {
				name += "，" + profile.Driver + " 沙箱"
			}
			fmt.Fprintf(&b, "\n- %s：%s", profile.ID, truncateRunes(name, 80))
		}
	}
	if s.ProjectID == "" {
		b.WriteString("\n\n个人对话可直接打开可写工作区，使用共享运行环境。workspaceRunId 只可切回本对话开过的工作区。")
	} else if repos := projectRepositoryHints(h.repo.Repositories(), s.ProjectID); len(repos) > 0 {
		catalog := jsonObject(repos)
		if len(catalog) <= workspaceRepositoryInlineBytes {
			b.WriteString("\n\n当前项目登记的仓库（JSON 数据，供 git clone 参考）\n" + catalog)
		} else {
			b.WriteString("\n\n进入工作区后可以读取完整的项目仓库清单。")
		}
	}
	d.Description = b.String()
	return d
}

// conversationSandboxKind folds a sandbox driver into the kinds a user can
// prefer for conversation workspaces.
func conversationSandboxKind(profile domain.SandboxProfile) string {
	switch strings.TrimSpace(profile.Driver) {
	case "", domain.SandboxDriverDocker:
		return domain.SandboxDriverDocker
	}
	return domain.SandboxDriverNative
}

// conversationSandboxChoices lists the environments the agent may open for
// this conversation, sandboxes of the speaker's preferred kind first, and the
// one it should use by default: empty without a preference or a sandbox of
// that kind.
func (h *agentToolGateway) conversationSandboxChoices(s *AgentToolSession) ([]domain.SandboxProfile, string) {
	choices := h.repo.ConversationSandboxes(s.ProjectID)
	nativeAvailable := runtime.NativeSandboxAvailable
	if s.ExecutionRunnerID != "" {
		runner, _ := h.repo.Runner(s.ExecutionRunnerID)
		targetOS := strings.ToLower(strings.TrimSpace(runner.OS))
		nativeAvailable = func(driver string) bool {
			if !runner.NativeSandboxAvailable {
				return false
			}
			driver, err := domain.ResolveSandboxDriver(driver, targetOS)
			return err == nil && (driver == domain.SandboxDriverSeatbelt && targetOS == "darwin" ||
				driver == domain.SandboxDriverBwrap && targetOS == "linux")
		}
	}
	choices = slices.DeleteFunc(choices, func(profile domain.SandboxProfile) bool {
		return conversationSandboxKind(profile) == domain.SandboxDriverNative && !nativeAvailable(profile.Driver)
	})
	prefer := h.repo.AssistantPreferences(s.UserID).WorkspaceDriver
	if prefer == "" {
		return choices, ""
	}
	rank := func(profile domain.SandboxProfile) int {
		switch {
		case conversationSandboxKind(profile) != prefer:
			return 2
		case profile.ID == repo.DefaultSandboxProfileID:
			return 0
		}
		return 1
	}
	slices.SortStableFunc(choices, func(a, b domain.SandboxProfile) int { return rank(a) - rank(b) })
	if len(choices) == 0 || rank(choices[0]) == 2 {
		return choices, ""
	}
	return choices, choices[0].ID
}

func workspaceSandboxGuide(choices []domain.SandboxProfile, preferred string) string {
	for _, profile := range choices {
		if profile.ID != preferred {
			continue
		}
		if conversationSandboxKind(profile) == domain.SandboxDriverNative {
			return fmt.Sprintf("用户在 AI 助手设置里倾向 native 沙箱（执行机器上的系统轻量隔离，使用执行机器已装的工具链）：新开工作区且没有特殊要求时用 %s，任务需要的语言或工具链只有其他环境具备时再换", preferred)
		}
		return fmt.Sprintf("用户在 AI 助手设置里倾向 Docker 沙箱：新开工作区且没有特殊要求时用 %s，任务需要的语言或工具链只有其他环境具备时再换", preferred)
	}
	return "按要做的事需要的语言、工具链选择，拿不准用 generic-docker"
}

type workspaceRepositoryHint struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	CloneURL      string `json:"cloneUrl,omitempty"`
	WebURL        string `json:"webUrl,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
}

func projectRepositoryHints(all []domain.Repository, projectID string) []workspaceRepositoryHint {
	repositories := projectRepositories(all, projectID)
	hints := make([]workspaceRepositoryHint, 0, len(repositories))
	for _, r := range repositories {
		hint := workspaceRepositoryHint{
			ID: r.ID, Name: r.Name, CloneURL: workspaceRepositoryURL(r.CloneURL), DefaultBranch: strings.TrimSpace(r.DefaultBranch),
		}
		if hint.CloneURL == "" {
			hint.WebURL = workspaceRepositoryURL(r.WebURL)
		}
		hints = append(hints, hint)
	}
	slices.SortFunc(hints, func(a, b workspaceRepositoryHint) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return hints
}

// Network URLs in model input need only the base address. Older records
// may have signed query strings or fragments in addition to URL userinfo.
func workspaceRepositoryURL(raw string) string {
	base := strings.TrimSpace(raw)
	separator := strings.Index(base, "://")
	if separator < 0 {
		return base
	}
	prefix, err := url.Parse(base[:separator+3])
	if err != nil || prefix.Scheme == "" {
		return base
	}
	// Git treats these characters in file:// paths as part of the filename.
	if prefix.Scheme != "file" {
		if i := strings.IndexAny(base, "?#"); i >= 0 {
			base = base[:i]
		}
	}
	if _, err := url.Parse(base); err != nil {
		return ""
	}
	return domain.StripURLCredentials(base)
}

type workspaceOpenChoice struct {
	SandboxID      string
	WorkspaceRunID string
}

func parseWorkspaceOpenChoice(args map[string]any) (workspaceOpenChoice, error) {
	choice := workspaceOpenChoice{}
	for key := range args {
		if key != "sandboxProfileId" && key != "workspaceRunId" {
			return choice, fmt.Errorf("workbench_workspace_open 只接收 sandboxProfileId 和可选的 workspaceRunId：普通工作区不再预先检出仓库或分支，进入工作区后自己 git clone / checkout（收到多余参数 %s）", key)
		}
	}
	readString := func(key string) (string, error) {
		raw, ok := args[key]
		if !ok || raw == nil {
			return "", nil
		}
		value, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("%s 必须是字符串", key)
		}
		return strings.TrimSpace(value), nil
	}
	var err error
	if choice.SandboxID, err = readString("sandboxProfileId"); err != nil {
		return choice, err
	}
	if choice.WorkspaceRunID, err = readString("workspaceRunId"); err != nil {
		return choice, err
	}
	return choice, nil
}

// openConversationWorkspace binds an environment (or a borrowable SOP / own
// workspace runId on the same execution machine) to an ordinary conversation
// and hands the CLI over to it. A task conversation enters its task's
// workspace once the SOP has stopped.
func (h *agentToolGateway) openConversationWorkspace(ctx context.Context, s *AgentToolSession, args map[string]any) (string, error) {
	choice, err := parseWorkspaceOpenChoice(args)
	if err != nil {
		return "", err
	}
	if s.RunID != "" {
		if choice.WorkspaceRunID != "" && choice.WorkspaceRunID != s.RunID {
			return "", fmt.Errorf("任务对话固定使用自身工作区，不能切换到其他运行 %s", choice.WorkspaceRunID)
		}
		return h.openTaskWorkspace(s)
	}
	if !s.canOpenConversationWorkspace() {
		return "", errAgentToolForbidden
	}
	bound, err := h.bindConversationWorkspace(ctx, s, choice)
	if err != nil {
		return "", err
	}
	run := bound.Run
	out := map[string]any{"runId": run.ID, "reused": bound.Reused, "created": bound.Created, "sandboxProfileId": run.WorkflowSnapshot.SandboxProfileID}
	var notice strings.Builder
	switch {
	case !run.IsWorkspace():
		out["sopRunId"], out["sopStatus"], out["issueId"] = run.ID, run.Status, run.ExecutionContext.IssueID
		if bound.Reused {
			fmt.Fprintf(&notice, "继续使用本对话当前绑定的需求 %s 的 SOP 运行 %s 的工作区。", run.ExecutionContext.IssueID, run.ID)
		} else {
			fmt.Fprintf(&notice, "已将本对话绑定到需求 %s 的 SOP 运行 %s 的工作区。", run.ExecutionContext.IssueID, run.ID)
		}
		notice.WriteString(borrowedSOPNotice(run))
	case bound.Created:
		fmt.Fprintf(&notice, "已在运行环境 %s 中为本对话新开工作区。", run.WorkflowSnapshot.SandboxProfileID)
	case bound.Reused:
		notice.WriteString("继续使用本对话当前的工作区。")
		if choice.SandboxID != "" && !h.workspaceSandboxIDsMatch(choice.SandboxID, run.WorkflowSnapshot.SandboxProfileID) {
			notice.WriteString("本次选择的环境没有生效。")
		}
	default:
		fmt.Fprintf(&notice, "已切回运行环境 %s 中本对话之前开过的工作区 %s。", run.WorkflowSnapshot.SandboxProfileID, run.ID)
	}
	if run.IsWorkspace() && bound.Created && len(run.RepositoryContexts()) == 0 {
		notice.WriteString("工作区已提供空白可写目录，可创建文件、运行命令和预览。有仓库相关任务时，用 git clone 拉取所需仓库。")
	}
	if previous, ok := h.repo.RunState(bound.Previous); ok {
		out["previousRunId"] = previous.ID
		if previous.IsWorkspace() {
			fmt.Fprintf(&notice, "之前的工作区 %s 已解绑，空闲时保存快照并回收。", previous.ID)
		} else {
			fmt.Fprintf(&notice, "之前接入的 SOP 工作区 %s 已解绑，本对话修改保留在原工作区中。", previous.ID)
		}
		if bound.CheckpointError != "" {
			out["previousCheckpointError"] = bound.CheckpointError
			notice.WriteString("切换前保存它的快照没有成功，它的文件仍原样保留，回收前会再次保存，保存成功之前不会回收。")
		}
	}
	// Preparing the workspace restores a reclaimed snapshot and starts the
	// sandbox, so an unusable environment fails here instead of after handoff.
	treeCtx := context.WithValue(ctx, mainBotWorkspaceKey{}, run.ID)
	tree, err := h.callMainBotWorkspace(treeCtx, s, "workbench_workspace_tree", map[string]any{})
	if err != nil {
		// Reusing the bound workspace switched nothing, so there is no binding
		// to restore: keep it for the next attempt.
		if bound.Reused {
			return "", fmt.Errorf("工作区已绑定（%s），但准备时失败：%w", run.ID, err)
		}
		active, rollbackErr := h.rollbackConversationWorkspaceBinding(ctx, s, bound)
		switch {
		case rollbackErr != nil:
			return "", fmt.Errorf("工作区 %s 准备失败：%v。恢复绑定失败：%w", run.ID, err, rollbackErr)
		case bound.Created:
			return "", fmt.Errorf("工作区准备失败（已放弃这个工作区）：%w", err)
		case active == bound.Previous:
			return "", fmt.Errorf("打开工作区 %s 失败（已恢复原绑定）：%w", run.ID, err)
		default:
			return "", fmt.Errorf("打开工作区 %s 失败：%w", run.ID, err)
		}
	}
	out["files"] = json.RawMessage(truncateJSONForTool(tree, 6000))
	if run.IsWorkspace() || sopStopped(run) {
		s.requestMode(domain.AssistantModeWorkspace)
		out["handoff"] = true
		notice.WriteString("工作区已准备。" + assistantHandoffNotice)
	} else {
		out["handoff"] = false
		if s.RuntimeMode == domain.AssistantModeWorkspace {
			s.requestMode(domain.AssistantModePlatform)
		} else {
			s.requestMode("")
		}
	}
	out["notice"] = notice.String()
	raw, _ := json.Marshal(out)
	return string(raw), nil
}

func (h *agentToolGateway) resolveWorkspaceRunTarget(conv domain.AssistantSession, projectID, targetID string) (domain.Run, error) {
	run, ok := h.repo.RunState(targetID)
	if projectID == "" {
		if !ok || !run.IsPersonalWorkspace() || !conversationMayBind(conv, run, projectID) {
			return domain.Run{}, fmt.Errorf("工作区 %s 不存在或不属于当前个人对话", targetID)
		}
		return run, nil
	}
	if !ok || run.ExecutionContext.ProjectID != projectID {
		return domain.Run{}, fmt.Errorf("工作区或运行 %s 不存在或不属于当前项目", targetID)
	}
	if !run.IsWorkspace() {
		if canonicalID := engine.WorkspaceRunID(run); canonicalID != "" && canonicalID != run.ID {
			parent, found := h.repo.RunState(canonicalID)
			if !found || !borrowableSOPRun(parent, projectID) {
				return domain.Run{}, fmt.Errorf("子 SOP %s 所属父工作区 %s 不属于当前项目或不可接入", run.ID, canonicalID)
			}
			run = parent
		}
		if run.HistoricalGroup() {
			return domain.Run{}, fmt.Errorf("运行 %s 是历史团队协作记录，不支持直接接入工作区", run.ID)
		}
		if !borrowableSOPRun(run, projectID) {
			return domain.Run{}, fmt.Errorf("运行 %s 不能作为当前对话的工作区", run.ID)
		}
		return run, nil
	}
	if domain.TerminalRunStatus(run.Status) {
		return domain.Run{}, fmt.Errorf("工作区 %s 已结束（%s），无法再打开", run.ID, run.Status)
	}
	if !slices.Contains(ownWorkspaceRunIDs(conv), run.ID) {
		return domain.Run{}, fmt.Errorf("工作区 %s 属于其他对话的私有工作区，不能直接共用；如需基于其中的代码继续开发，请进入本对话工作区后自行 git clone 并 checkout 对应分支", run.ID)
	}
	return run, nil
}

// workspaceExecutionRunner is the execution machine holding run's workspace.
// A legacy run (no frozen execution mode) ran on its owner's default runner,
// which the raw ExecutionRunnerID field does not record.
func (h *agentToolGateway) workspaceExecutionRunner(run domain.Run) string {
	if strings.TrimSpace(run.ExecutionContext.ExecutionMode) == "" && h.engine != nil {
		return h.engine.RunExecutionRunner(run)
	}
	return strings.TrimSpace(run.ExecutionContext.ExecutionRunnerID)
}

func (h *agentToolGateway) executionMachineLabel(runnerID string) string {
	runnerID = strings.TrimSpace(runnerID)
	if runnerID == "" {
		return "平台内置环境（cloud）"
	}
	if r, ok := h.repo.Runner(runnerID); ok && strings.TrimSpace(r.Name) != "" && r.Name != runnerID {
		return fmt.Sprintf("执行机器 %s（%s）", r.Name, runnerID)
	}
	return fmt.Sprintf("执行机器 %s", runnerID)
}

// openTaskWorkspace hands a task conversation over to its task's workspace.
func (h *agentToolGateway) openTaskWorkspace(s *AgentToolSession) (string, error) {
	run, ok := h.repo.RunState(s.RunID)
	if !ok || run.IsPersonalWorkspace() || run.ExecutionContext.ProjectID != s.ProjectID {
		return "", errAgentToolForbidden
	}
	if !run.IsWorkspace() && !sopStopped(run) {
		return "", fmt.Errorf("SOP 运行 %s（%s）执行中，工作区只读：先暂停（runs_pause）或取消（runs_cancel）后再进入工作区", run.ID, run.Status)
	}
	s.requestMode(domain.AssistantModeWorkspace)
	raw, _ := json.Marshal(map[string]any{"runId": run.ID, "handoff": true, "notice": "进入任务工作区。" + assistantHandoffNotice})
	return string(raw), nil
}

func (h *agentToolGateway) rollbackConversationWorkspaceBinding(ctx context.Context, s *AgentToolSession, bound conversationWorkspaceBinding) (string, error) {
	if s.workspaceTurn != nil {
		s.workspaceTurn.operations.Lock()
		defer s.workspaceTurn.operations.Unlock()
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	if bound.Created {
		h.cancelUnpreparedConversationWorkspace(bound.Run.ID)
	}
	active, err := h.repo.RollbackAssistantSessionWorkspaceRun(s.ChatOwnerID, s.ChatSessionID, s.ProjectID, bound.Run.ID, bound.Previous, bound.Created)
	if err != nil {
		return "", err
	}
	if s.workspaceTurn != nil && s.workspaceRunID == bound.Run.ID {
		// Release the failed binding's lease so the same turn can reuse the
		// restored workspace.
		if err := s.workspaceTurn.checkpoint(ctx); err != nil && !errors.Is(err, errAssistantTurnClosed) {
			log.Printf("release failed workspace lease session=%s run=%s: %v", s.ChatSessionID, bound.Run.ID, err)
		}
	}
	s.workspaceRunID = active
	return active, nil
}

func (h *agentToolGateway) cancelUnpreparedConversationWorkspace(runID string) {
	if h.engine != nil {
		if err := h.engine.Cancel(runID); err != nil {
			log.Printf("cancel unprepared conversation workspace %s: %v", runID, err)
		}
		if err := h.engine.ReleaseConversationWorkspace(runID); err != nil {
			log.Printf("release unprepared conversation workspace %s: %v", runID, err)
		}
	}
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

// bindConversationWorkspace binds an explicitly requested SOP / own workspace
// (workspaceRunId), keeps the bound workspace unless a different environment is
// asked for (the user's fixed sandbox, else the model's choice), or resumes /
// creates the conversation's own workspace in wanted. Switching saves the
// previous workspace via checkpoint before releasing it.
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
	conv, exists := h.repo.AssistantSession(s.ChatOwnerID, s.ChatSessionID)
	if s.ProjectID == "" {
		if !s.canOpenConversationWorkspace() || s.User.ID != s.UserID || !exists || conv.ProjectID != "" || conv.Shared || conv.RunID != "" {
			return conversationWorkspaceBinding{}, errAgentToolForbidden
		}
	} else if _, ok := h.repo.Project(s.ProjectID); !ok {
		return conversationWorkspaceBinding{}, fmt.Errorf("项目不存在")
	}
	binding := conversationWorkspaceBinding{}
	if choice.WorkspaceRunID != "" {
		target, err := h.resolveWorkspaceRunTarget(conv, s.ProjectID, choice.WorkspaceRunID)
		if err != nil {
			return binding, err
		}
		if choice.SandboxID != "" && target.WorkflowSnapshot.SandboxProfileID != "" && !h.workspaceSandboxIDsMatch(choice.SandboxID, target.WorkflowSnapshot.SandboxProfileID) {
			return binding, fmt.Errorf("接入已有工作区 %s 时沿用其原本冻结的沙箱环境 %s，不能同时切换到 %s", target.ID, target.WorkflowSnapshot.SandboxProfileID, choice.SandboxID)
		}
		binding.Run = target
	} else {
		wanted := strings.TrimSpace(s.SandboxProfileID)
		if wanted == "" {
			wanted = choice.SandboxID
		}
		reuse := false
		if s.workspaceRunID != "" {
			run, ok := h.repo.RunState(s.workspaceRunID)
			if ok && conversationMayBind(conv, run, s.ProjectID) && (wanted == "" || h.workspaceSandboxIDsMatch(wanted, run.WorkflowSnapshot.SandboxProfileID) || !run.IsWorkspace() && choice.SandboxID == "") {
				binding.Run, reuse = run, true
			}
		}
		if !reuse && wanted == "" {
			_, wanted = h.conversationSandboxChoices(s)
		}
		if !reuse && wanted != "" {
			// Coming back to an environment this conversation used before resumes
			// that workspace, with the repositories it already cloned.
			if run, ok := h.ownConversationWorkspace(conv, s.ProjectID, wanted); ok {
				binding.Run, reuse = run, true
			}
		}
		if !reuse {
			run, err := h.startConversationWorkspace(s, conv, wanted)
			if err != nil {
				return binding, err
			}
			binding.Run, binding.Created = run, true
		}
	}
	// An SOP workspace belongs to the run, so working in it needs the same
	// write access as the run's workspace edits, terminal, resume and retry.
	// This also covers a viewer speaking in a shared conversation that another
	// member bound to an SOP.
	if !binding.Run.IsWorkspace() && !h.speakerProjectWriter(s.ProjectID, s.User, s.UserID) {
		return binding, fmt.Errorf("只读成员不能接入或进入 SOP 运行 %s 的工作区：工作区写入需要项目成员、项目 Owner 或平台管理员权限", binding.Run.ID)
	}
	if runRunner := h.workspaceExecutionRunner(binding.Run); runRunner != conv.Execution().RunnerID {
		srcMachine := h.executionMachineLabel(runRunner)
		convMachine := h.executionMachineLabel(conv.Execution().RunnerID)
		return binding, fmt.Errorf("工作区 %s 位于%s，与当前对话绑定的%s不在同一执行机器上；如需直接操作该工作区，请在「%s」下新建对话后再接入", binding.Run.ID, srcMachine, convMachine, srcMachine)
	}
	if snapshot := binding.Run.WorkflowSnapshot.SandboxSnapshot; snapshot != nil && !domain.ConversationSandboxDriver(snapshot.Profile.Driver) {
		return binding, fmt.Errorf("对话工作区只支持 Docker 或 native 沙箱（工作区 %s 使用的是 %s）", binding.Run.ID, snapshot.Profile.Driver)
	}
	if binding.Run.ID == s.workspaceRunID {
		binding.Reused = true
		return binding, nil
	}
	if s.workspaceTurn != nil {
		// A failed save only defers reclamation: the idle sweep saves a released
		// workspace again before reclaiming it and keeps its files if that save
		// fails too.
		if err := s.workspaceTurn.checkpoint(ctx); errors.Is(err, errAssistantTurnClosed) {
			if binding.Created {
				h.cancelUnpreparedConversationWorkspace(binding.Run.ID)
			}
			return conversationWorkspaceBinding{}, err
		} else if err != nil {
			binding.CheckpointError = err.Error()
			log.Printf("checkpoint conversation workspace before switch session=%s: %v", s.ChatSessionID, err)
		}
	}
	binding.Previous = s.workspaceRunID
	if err := h.repo.SetAssistantSessionWorkspaceRun(s.ChatOwnerID, s.ChatSessionID, s.ProjectID, binding.Run.ID, binding.Run.IsWorkspace()); err != nil {
		if binding.Created {
			h.cancelUnpreparedConversationWorkspace(binding.Run.ID)
		}
		return conversationWorkspaceBinding{}, fmt.Errorf("保存对话工作区绑定失败：%w", err)
	}
	s.workspaceRunID = binding.Run.ID
	if binding.Previous != "" && h.engine != nil {
		if err := h.engine.ReleaseConversationWorkspace(binding.Previous); err != nil {
			log.Printf("release conversation workspace session=%s run=%s: %v", s.ChatSessionID, binding.Previous, err)
		}
	}
	return binding, nil
}

// ownConversationWorkspace returns the most recent workspace the conversation
// opened in sandboxID that it may still bind.
func (h *agentToolGateway) ownConversationWorkspace(conv domain.AssistantSession, projectID, sandboxID string) (domain.Run, bool) {
	own := ownWorkspaceRunIDs(conv)
	for i := len(own) - 1; i >= 0; i-- {
		run, ok := h.repo.RunState(own[i])
		if ok && conversationMayBind(conv, run, projectID) && h.workspaceSandboxIDsMatch(sandboxID, run.WorkflowSnapshot.SandboxProfileID) {
			return run, true
		}
	}
	return domain.Run{}, false
}

// Older conversations and frozen workspaces may still spell the platform
// default with an equivalent ID. It identifies a single workspace sandbox.
func (h *agentToolGateway) workspaceSandboxIDsMatch(a, b string) bool {
	if a == b {
		return true
	}
	for _, id := range []string{a, b} {
		if reserved, err := h.repo.IsReservedSandboxID(id); err != nil || !reserved {
			return false
		}
	}
	return true
}

func (h *agentToolGateway) startConversationWorkspace(s *AgentToolSession, conv domain.AssistantSession, sandboxID string) (domain.Run, error) {
	sandbox, err := h.repo.ResolveConversationSandbox(s.ProjectID, sandboxID)
	if err != nil {
		return domain.Run{}, err
	}
	if h.engine == nil {
		return domain.Run{}, fmt.Errorf("执行引擎不可用")
	}
	title := "对话工作区：" + truncateRunes(sandbox.Name, 80)
	target := conv.Execution()
	return h.engine.StartConversationWorkspace(title, domain.TelemetryContext{
		ProjectID: s.ProjectID, ResponsibleUserID: s.UserID,
		ExecutionPinned: true, ExecutionMode: target.Pool, ExecutionRunnerID: target.RunnerID,
	}, sandbox.ID)
}

func truncateJSONForTool(raw string, limit int) string {
	if len(raw) <= limit {
		return raw
	}
	data, _ := json.Marshal(map[string]any{"truncated": true, "preview": truncateRunes(raw, limit)})
	return string(data)
}

func repositoryRunContext(r domain.Repository) domain.RepositoryContext {
	return domain.RepositoryContext{RepositoryID: r.ID, Provider: r.Provider, Name: r.Name, GongfengProjectID: r.GongfengProjectID, WebURL: r.WebURL, CloneURL: r.CloneURL, BaseBranch: r.DefaultBranch, DefaultBranch: r.DefaultBranch, LocalPath: r.LocalPath, PipelineProjectID: r.PipelineProjectID, PipelineDefID: r.PipelineDefID, PipelineURLPattern: r.PipelineURLPattern}
}

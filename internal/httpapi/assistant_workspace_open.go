package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
)

// A conversation workspace is an environment, not a checkout: Main Bot picks
// the sandbox profile (or workspaceRunId to bind a same-project SOP run or an
// earlier workspace of this conversation on the same execution machine). Empty
// workspaces start empty so the agent clones, branches, commits and pushes with
// its own git using the requester's Git credentials.
const workspaceOpenSchema = `{"type":"object","properties":{"sandboxProfileId":{"type":"string","maxLength":128,"description":"运行环境（沙箱配置）id；用于新开或切回本对话在该环境自己的工作区，不填沿用当前工作区的环境（没有工作区时用会话设置或 generic-docker）。传入 workspaceRunId 接入已有工作区/SOP 时沿用原运行冻结的沙箱环境，不要传不同的 sandboxProfileId"},"workspaceRunId":{"type":"string","maxLength":128,"description":"可选：要直接接入的同项目 SOP 运行 id（或本对话之前开过的工作区 id）。要求与当前对话在同一执行机器上：会直接绑定并操作该原工作区现场（包括未推送改动与中间产物，改完后也可回 SOP 从后续节点重跑或继续；已回收工作区会自动恢复快照；传子 SOP 时自动定位到父工作区）。日常继续当前工作区或只切换对话自身环境时不要传"}},"additionalProperties":false}`

func workspaceOpenDescriptor() agentToolDescriptor {
	return agentToolDescriptor{
		Name:        "workbench_workspace_open",
		Description: "进入工作区模式：在指定的运行环境（沙箱配置）里打开本对话的工作区，或直接接入某次同项目 SOP 运行的原工作区，之后用 CLI 原生的文件、补丁和命令工具工作。需要看代码、改代码、跑命令、构建、访问 GitHub（查 PR、开 PR、合并、评论等）时调用。新开普通工作区默认是空目录，不预先检出任何仓库：自己 git clone 需要的仓库，自己建分支、提交、推送、开 PR、合并。若要在普通新对话里接着某次历史/暂停 SOP 的原工作区现场干活（保留未推送改动与产物，且支持之后回原 SOP 重跑节点或继续），传 workspaceRunId 直接接入（需与当前对话在同一执行机器，已回收的快照会自动恢复；删除本对话不会删除借用的 SOP 工作区）。平台已按本轮发言人的个人凭证配置好 Git（github.com 的 HTTPS 用 Git Token，SSH 用个人 SSH 密钥），GH_TOKEN 也已设置，环境里没有 gh 时可以自己安装或用 curl 调 API。不要打印、回显或写入任何令牌。一个对话同一时间只有一个活动工作区：不传参数或传相同环境时继续用当前工作区，传不同的 sandboxProfileId 就切换过去；切走的工作区解绑，空闲时保存快照后回收。返回 handoff=true 时只回复一句「好的。」结束本阶段，平台会在工作区里续接同一原生会话。开好的 PR 由平台在本轮结束时自动登记到需求并跟踪合入。子 Agent 不能调用本工具。",
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
		b.WriteString("\n\n可选运行环境（sandboxProfileId，按要做的事需要的语言、工具链选择，拿不准用 generic-docker）：")
		for _, profile := range h.repo.ConversationSandboxes(s.ProjectID) {
			name := profile.Name
			if df, ok := h.repo.Dockerfile(profile.DockerfileID); ok && df.Name != "" && df.Name != name {
				name += "，镜像 " + df.Name
			}
			if profile.Driver != "" && profile.Driver != domain.SandboxDriverDocker {
				name += "，" + profile.Driver + " 沙箱"
			}
			fmt.Fprintf(&b, "\n- %s：%s", profile.ID, truncateRunes(name, 80))
		}
	}
	if repos := projectRepositoryHints(h.repo.Repositories(), s.ProjectID); repos != "" {
		b.WriteString("\n\n项目登记的仓库（供 git clone 参考，不会自动检出）：" + repos)
	}
	d.Description = b.String()
	return d
}

func projectRepositoryHints(all []domain.Repository, projectID string) string {
	var b strings.Builder
	for _, r := range all {
		if r.ProjectID != projectID {
			continue
		}
		fmt.Fprintf(&b, "\n- %s", r.Name)
		if clone := strings.TrimSpace(r.CloneURL); clone != "" {
			fmt.Fprintf(&b, "：%s", clone)
		} else if web := strings.TrimSpace(r.WebURL); web != "" {
			fmt.Fprintf(&b, "：%s", web)
		}
		if branch := strings.TrimSpace(r.DefaultBranch); branch != "" {
			fmt.Fprintf(&b, "（主分支 %s）", branch)
		}
	}
	return b.String()
}

// usableConversationWorkspace reports whether a bound workspace can still be
// used by a conversation of projectID.
func usableConversationWorkspace(run domain.Run, projectID string) bool {
	return run.ExecutionContext.ProjectID == projectID && run.IsWorkspace() && !domain.TerminalRunStatus(run.Status)
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
		return "", fmt.Errorf("只有项目对话可以打开工作区")
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
		if choice.SandboxID != "" && choice.SandboxID != run.WorkflowSnapshot.SandboxProfileID {
			notice.WriteString("本次选择的环境没有生效。")
		}
	default:
		fmt.Fprintf(&notice, "已切回运行环境 %s 中本对话之前开过的工作区 %s。", run.WorkflowSnapshot.SandboxProfileID, run.ID)
	}
	if run.IsWorkspace() && bound.Created && len(run.RepositoryContexts()) == 0 {
		notice.WriteString("工作区是空目录：用 git clone 拉取需要的仓库，自己建分支、提交、推送并开 PR/MR。")
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
		if bound.Created {
			h.discardConversationWorkspace(s, bound)
			return "", fmt.Errorf("工作区准备失败（已放弃这个工作区）：%v", err)
		}
		// Reusing the bound workspace switched nothing, so there is no binding
		// to restore: keep it for the next attempt.
		if !bound.Reused && bound.Previous != bound.Run.ID {
			h.restoreConversationWorkspaceBinding(s, bound.Previous)
			return "", fmt.Errorf("打开工作区 %s 失败（已恢复原绑定）：%v", run.ID, err)
		}
		return "", fmt.Errorf("工作区已绑定（%s），但准备时失败：%v", run.ID, err)
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
	if !ok || strings.TrimSpace(run.ExecutionContext.ProjectID) == "" || run.ExecutionContext.ProjectID != projectID {
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
	if !ok || run.ExecutionContext.ProjectID != s.ProjectID {
		return "", errAgentToolForbidden
	}
	if !run.IsWorkspace() && !sopStopped(run) {
		return "", fmt.Errorf("SOP 运行 %s（%s）执行中，工作区只读：先暂停（runs_pause）或取消（runs_cancel）后再进入工作区", run.ID, run.Status)
	}
	s.requestMode(domain.AssistantModeWorkspace)
	raw, _ := json.Marshal(map[string]any{"runId": run.ID, "handoff": true, "notice": "进入任务工作区。" + assistantHandoffNotice})
	return string(raw), nil
}

func (h *agentToolGateway) restoreConversationWorkspaceBinding(s *AgentToolSession, previous string) {
	s.workspaceRunID = previous
	if err := h.repo.SetAssistantSessionWorkspaceRun(s.ChatOwnerID, s.ChatSessionID, previous, false); err != nil {
		log.Printf("rebind conversation workspace after failure session=%s run=%s: %v", s.ChatSessionID, previous, err)
	}
}

// discardConversationWorkspace cancels a workspace this call created but could
// not prepare, and puts the conversation back on the workspace it used before.
func (h *agentToolGateway) discardConversationWorkspace(s *AgentToolSession, bound conversationWorkspaceBinding) {
	if h.engine != nil {
		if err := h.engine.Cancel(bound.Run.ID); err != nil {
			log.Printf("cancel unprepared conversation workspace %s: %v", bound.Run.ID, err)
		}
		if err := h.engine.ReleaseConversationWorkspace(bound.Run.ID); err != nil {
			log.Printf("release unprepared conversation workspace %s: %v", bound.Run.ID, err)
		}
	}
	h.restoreConversationWorkspaceBinding(s, bound.Previous)
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
	project, ok := h.repo.Project(s.ProjectID)
	if !ok {
		return conversationWorkspaceBinding{}, fmt.Errorf("项目不存在")
	}
	conv, _ := h.repo.AssistantSession(s.ChatOwnerID, s.ChatSessionID)
	binding := conversationWorkspaceBinding{}
	if choice.WorkspaceRunID != "" {
		target, err := h.resolveWorkspaceRunTarget(conv, s.ProjectID, choice.WorkspaceRunID)
		if err != nil {
			return binding, err
		}
		if choice.SandboxID != "" && target.WorkflowSnapshot.SandboxProfileID != "" && choice.SandboxID != target.WorkflowSnapshot.SandboxProfileID {
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
			if ok && conversationMayBind(conv, run, s.ProjectID) && (wanted == "" || wanted == run.WorkflowSnapshot.SandboxProfileID || !run.IsWorkspace() && choice.SandboxID == "") {
				binding.Run, reuse = run, true
			}
		}
		if !reuse && wanted != "" {
			// Coming back to an environment this conversation used before resumes
			// that workspace, with the repositories it already cloned.
			if run, ok := h.ownConversationWorkspace(conv, s.ProjectID, wanted); ok {
				binding.Run, reuse = run, true
			}
		}
		if !reuse {
			run, err := h.startConversationWorkspace(s, project, conv, wanted)
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

// ownConversationWorkspace returns the most recent workspace the conversation
// opened in sandboxID that it may still bind.
func (h *agentToolGateway) ownConversationWorkspace(conv domain.AssistantSession, projectID, sandboxID string) (domain.Run, bool) {
	own := ownWorkspaceRunIDs(conv)
	for i := len(own) - 1; i >= 0; i-- {
		run, ok := h.repo.RunState(own[i])
		if ok && run.WorkflowSnapshot.SandboxProfileID == sandboxID && conversationMayBind(conv, run, projectID) {
			return run, true
		}
	}
	return domain.Run{}, false
}

func (h *agentToolGateway) startConversationWorkspace(s *AgentToolSession, project domain.Project, conv domain.AssistantSession, sandboxID string) (domain.Run, error) {
	sandbox, err := h.repo.ResolveConversationSandbox(project.ID, sandboxID)
	if err != nil {
		return domain.Run{}, err
	}
	if h.engine == nil {
		return domain.Run{}, fmt.Errorf("执行引擎不可用")
	}
	title := "对话工作区：" + truncateRunes(sandbox.Name, 80)
	return h.engine.StartConversationWorkspace(title, []domain.RepositoryContext{}, domain.TelemetryContext{ProjectID: project.ID, ResponsibleUserID: s.UserID, ExecutionPinned: true, ExecutionMode: conv.Execution().Pool, ExecutionRunnerID: conv.Execution().RunnerID}, sandbox.ID)
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

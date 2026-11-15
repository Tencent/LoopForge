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

// A conversation workspace is an environment, not a checkout: Main Bot only
// picks the sandbox profile. The workspace starts empty and the agent clones,
// branches, commits and pushes with its own git using the requester's Git
// credentials, which the platform binds to the workspace CLI.
const workspaceOpenSchema = `{"type":"object","properties":{"sandboxProfileId":{"type":"string","maxLength":128,"description":"运行环境（沙箱配置）id；不填沿用当前工作区的环境，还没有工作区时使用会话设置或 generic-docker"}},"additionalProperties":false}`

func workspaceOpenDescriptor() agentToolDescriptor {
	return agentToolDescriptor{
		Name:        "workbench_workspace_open",
		Description: "进入工作区模式：在指定的运行环境（沙箱配置）里打开本对话的工作区，之后用 CLI 原生的文件、补丁和命令工具工作。需要看代码、改代码、跑命令、构建、访问 GitHub/工蜂（查 PR、开 PR、合并、评论等）时调用。工作区是空目录，不预先检出任何仓库：自己 git clone 需要的仓库，自己建分支、提交、推送、开 PR/MR、合并；平台已按本轮发言人的个人凭证配置好 Git（HTTPS 用 Git Token，SSH 用个人 SSH 密钥），GH_TOKEN 也已设置，环境里没有 gh 时可以自己安装或用 curl 调 API。不要打印、回显或写入任何令牌。一个对话同一时间只有一个工作区：不传 sandboxProfileId 或传的与当前工作区相同时继续用当前工作区，传了不同的环境就切换过去：本对话之前在那个环境用过的工作区接着用（文件和已克隆的仓库都在），没用过就新开一个空工作区；切走的工作区解绑，空闲时保存快照后回收。返回 handoff=true 时只回复一句「好的。」结束本阶段，平台会在工作区里续接同一原生会话。开好 PR/MR 后调用 workbench_delivery_register 登记，让需求列表跟踪合入。子 Agent 不能调用本工具。",
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
	SandboxID string
}

func parseWorkspaceOpenChoice(args map[string]any) (workspaceOpenChoice, error) {
	choice := workspaceOpenChoice{}
	for key := range args {
		if key != "sandboxProfileId" {
			return choice, fmt.Errorf("workbench_workspace_open 只接收 sandboxProfileId：工作区不再预先检出仓库或分支，进入工作区后自己 git clone / checkout（收到多余参数 %s）", key)
		}
	}
	if raw, ok := args["sandboxProfileId"]; ok && raw != nil {
		value, ok := raw.(string)
		if !ok {
			return choice, fmt.Errorf("sandboxProfileId 必须是字符串")
		}
		choice.SandboxID = strings.TrimSpace(value)
	}
	return choice, nil
}

// openConversationWorkspace binds an environment to an ordinary conversation
// and hands the CLI over to it. A task conversation enters its task's
// workspace once the SOP has stopped.
func (h *agentToolGateway) openConversationWorkspace(ctx context.Context, s *AgentToolSession, args map[string]any) (string, error) {
	choice, err := parseWorkspaceOpenChoice(args)
	if err != nil {
		return "", err
	}
	if s.RunID != "" {
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
		fmt.Fprintf(&notice, "本对话绑定的是需求 %s 的 SOP 运行 %s 的工作区。", run.ExecutionContext.IssueID, run.ID)
	case bound.Created:
		fmt.Fprintf(&notice, "已在运行环境 %s 中为本对话新开工作区。", run.WorkflowSnapshot.SandboxProfileID)
	default:
		notice.WriteString("继续使用本对话当前的工作区。")
		if choice.SandboxID != "" && choice.SandboxID != run.WorkflowSnapshot.SandboxProfileID {
			notice.WriteString("本次选择的环境没有生效。")
		}
	}
	if run.IsWorkspace() && len(run.RepositoryContexts()) == 0 {
		notice.WriteString("工作区是空目录：用 git clone 拉取需要的仓库，自己建分支、提交、推送并开 PR/MR。")
	}
	if previous, ok := h.repo.RunState(bound.Previous); ok {
		out["previousRunId"] = previous.ID
		fmt.Fprintf(&notice, "之前的工作区 %s 已解绑，空闲时保存快照并回收。", previous.ID)
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
		}
		notice.WriteString("SOP 仍在执行，工作区只读；用 runs_pause 或 runs_cancel 停下后再进入工作区模式。")
	}
	out["notice"] = notice.String()
	raw, _ := json.Marshal(out)
	return string(raw), nil
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
	s.workspaceRunID = bound.Previous
	if err := h.repo.SetAssistantSessionWorkspaceRun(s.ChatOwnerID, s.ChatSessionID, bound.Previous, false); err != nil {
		log.Printf("rebind conversation workspace after failure session=%s run=%s: %v", s.ChatSessionID, bound.Previous, err)
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

// bindConversationWorkspace keeps the bound workspace unless a different
// environment is asked for (the user's fixed sandbox, else the model's
// choice). It then resumes the conversation's own earlier workspace in that
// environment, else opens a new empty one (generic-docker when nothing was
// chosen). The workspace it switches away from is released.
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
	wanted := strings.TrimSpace(s.SandboxProfileID)
	if wanted == "" {
		wanted = choice.SandboxID
	}
	binding := conversationWorkspaceBinding{}
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
	if binding.Run.ExecutionContext.ExecutionRunnerID != conv.Execution().RunnerID {
		return binding, fmt.Errorf("工作区与会话绑定的执行机器不同，请在相应机器新建对话")
	}
	if snapshot := binding.Run.WorkflowSnapshot.SandboxSnapshot; snapshot != nil && !domain.ConversationSandboxDriver(snapshot.Profile.Driver) {
		return binding, fmt.Errorf("对话工作区只支持 Docker 或 native 沙箱")
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

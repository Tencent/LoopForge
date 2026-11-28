package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/workbuddy"
)

var errWorkBuddyDispatched = errors.New("WorkBuddy 会话已发起")

func (e *Engine) workBuddyRole(wf domain.WorkflowDefinition, node domain.WorkflowNode) (domain.AgentDefinition, bool) {
	if node.Type != "agent" {
		return domain.AgentDefinition{}, false
	}
	role, ok := workflowAgent(e.repo, wf, node.AgentID)
	return role, ok && role.ExecutorID == domain.WorkBuddyExecutorID
}

func (e *Engine) workBuddyOnly(wf domain.WorkflowDefinition) bool {
	remote := false
	for _, node := range wf.Nodes {
		if node.Type == "approval" {
			continue
		}
		if _, role := e.workBuddyRole(wf, node); node.Type != domain.WorkflowNodeWorkBuddy && !role {
			return false
		}
		remote = true
	}
	return remote
}

func (e *Engine) executeWorkBuddyNode(ctx context.Context, runID string, wf domain.WorkflowDefinition, node domain.WorkflowNode, role *domain.AgentDefinition, attempt, generation int, failNode func(error, bool)) {
	if role != nil {
		if err := role.ValidateWorkBuddy(); err != nil {
			failNode(err, false)
			return
		}
		if node.ExecutionScope == domain.ExecutionScopeRepositories {
			failNode(fmt.Errorf("WorkBuddy 节点 %s 以运行作为执行范围", node.ID), false)
			return
		}
		// The normal Agent node stays in the workflow snapshot. This copy uses
		// the same output and attempt fencing as a dedicated WorkBuddy node.
		node.Type, node.WorkBuddy, node.TaskPrompt = domain.WorkflowNodeWorkBuddy, role.WorkBuddy, role.Prompt
		node.ArtifactOutputs = mergeArtifactContracts(role.ArtifactOutputs, node.ArtifactOutputs)
	} else if err := node.ValidateWorkBuddy(); err != nil {
		failNode(err, false)
		return
	}
	current, ok := e.repo.RunProgress(runID)
	if !ok || !e.ownsRun(current) {
		return
	}
	projectID, err := e.runProjectCredentialProject(current)
	if err != nil {
		failNode(err, false)
		return
	}
	if role != nil && role.ProjectID != projectID {
		failNode(fmt.Errorf("WorkBuddy 角色必须属于当前运行项目"), false)
		return
	}
	config := *node.WorkBuddy
	key, err := e.repo.ResolveProjectAKSK(ctx, projectID, strings.TrimSpace(config.CredentialID))
	if err != nil {
		failNode(err, false)
		return
	}
	client, err := workbuddy.New(config, key)
	if err != nil {
		failNode(err, false)
		return
	}
	remoteCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	gate, err := workbuddy.BeginGate(remoteCtx)
	if err != nil {
		failNode(err, false)
		return
	}
	defer gate.Discard(ctx)
	artifactRefs, err := e.workBuddyArtifactInputs(remoteCtx, wf, node, current)
	if err != nil {
		failNode(err, false)
		return
	}
	var model string
	var rules []domain.RuleDefinition
	if role != nil {
		model = strings.TrimSpace(role.Model)
		if model == "default" {
			model = ""
		}
		rules = e.resolveAssets(*role, projectID, wf.ID, current.ExecutionContext.ResponsibleUserID).Rules
	}
	prompt := workBuddyPrompt(wf, node, role, rules, current, gate, artifactRefs)
	token, err := client.AccessToken(remoteCtx)
	if err != nil {
		failNode(err, false)
		return
	}
	// Persist the dispatch fence before the non-idempotent cloud API call.
	reserved := false
	updated, err := e.updateRunAtErr(runID, false, func(r *domain.Run) {
		nr := r.Nodes[node.ID]
		if !shouldCommitNodeOutcome(*r, nr, generation, attempt, false) || r.CancelRequested || nr.WorkBuddyAttempt == attempt {
			return
		}
		nr.WorkBuddyAttempt = attempt
		nr.SessionID, nr.ResumeSessionID = "", ""
		nr.Provider = domain.WorkflowNodeWorkBuddy
		nr.Model = ""
		reserved = true
	})
	if err != nil {
		failNode(err, false)
		return
	}
	if !updated || remoteCtx.Err() != nil {
		failNode(remoteCtx.Err(), false)
		return
	}
	if !reserved {
		failNode(fmt.Errorf("%w，请检查远端会话后再手动重跑", errWorkBuddyDispatched), false)
		return
	}
	failRemote := func(err error, reject bool) {
		failNode(fmt.Errorf("%w：%w", errWorkBuddyDispatched, err), reject)
	}
	session, err := client.CreateSession(remoteCtx, config)
	if err != nil {
		failRemote(err, false)
		return
	}
	sessionSaved := false
	updated, err = e.updateRunAtErr(runID, false, func(r *domain.Run) {
		nr := r.Nodes[node.ID]
		if shouldCommitNodeOutcome(*r, nr, generation, attempt, false) && !r.CancelRequested {
			nr.SessionID = session.ID
			e.event(r, node.ID, "workbuddy.session.created", "WorkBuddy 会话已创建")
			sessionSaved = true
		}
	})
	if err != nil {
		failRemote(err, false)
		return
	}
	if !updated || !sessionSaved {
		return
	}
	sink := func(event domain.TelemetryEvent) {
		event.SessionID = session.ID
		event.Provider = domain.WorkflowNodeWorkBuddy
		event.Source = domain.TelemetrySource{Layer: "executor", Adapter: "workbuddy-acp", Provider: domain.WorkflowNodeWorkBuddy}
		e.observe(runID, node.ID, event)
	}
	knowledgeRefs := node.KnowledgeRefs
	if override := current.ExecutionContext.KnowledgeRefs; override != nil {
		knowledgeRefs = *override
	}
	if len(knowledgeRefs) > 0 {
		sink(domain.TelemetryEvent{Category: "sandbox", Name: "mcp.config.skipped", Phase: "start", Level: "warn", Message: "WorkBuddy 云端会话仅使用 Agent 版本绑定的连接器，本次跳过 LoopForge 知识库绑定"})
	}
	sink(domain.TelemetryEvent{Category: "lifecycle", Name: "agent.started", Phase: "start", Message: "WorkBuddy 托管智能体开始执行"})
	onText := func(chunk string) {
		sink(domain.TelemetryEvent{Category: "output", Name: "assistant.delta", Phase: "progress", Message: chunk})
	}
	streamText := onText
	if gate.ReturnsViaACP() {
		streamText = nil // A receipt may itself span multiple ACP chunks.
	}
	turn, err := client.RunTurn(remoteCtx, session, token, model, prompt, streamText, gate)
	output := turn.Text
	if gate.ReturnsViaACP() {
		output = strings.ReplaceAll(workbuddy.GateDisplayText(output), gate.Ticket, "[Gate 票据]")
		if output != "" {
			onText(output)
		}
	}
	if turn.Usage.Input != 0 || turn.Usage.Output != 0 || turn.Usage.CacheRead != 0 || turn.Usage.CacheWrite != 0 {
		sink(domain.TelemetryEvent{Category: "usage", Name: "llm.usage", Phase: "finish", Model: turn.Usage.Model, Usage: &domain.TokenUsage{
			InputTokens: turn.Usage.Input, OutputTokens: turn.Usage.Output, CacheReadTokens: turn.Usage.CacheRead, CacheWriteTokens: turn.Usage.CacheWrite,
		}})
	}
	if err != nil {
		failRemote(err, nodeRejects(node))
		return
	}
	rawDecision, err := gate.Result(remoteCtx, turn)
	if err != nil {
		sink(domain.TelemetryEvent{Category: "verification", Name: "gate.submit.missing", Phase: "finish", Level: "error", Message: err.Error()})
		failRemote(err, false)
		return
	}
	submitted, err := decodeGateResult(rawDecision)
	if err != nil {
		failRemote(err, false)
		return
	}
	decisionSaved := false
	updated, err = e.updateRunAtErr(runID, false, func(r *domain.Run) {
		nr := r.Nodes[node.ID]
		if shouldCommitNodeOutcome(*r, nr, generation, attempt, false) && !r.CancelRequested {
			nr.GateResult = submitted
			nr.Output = output
			nr.Model = turn.Model
			if nr.Model == "" {
				nr.Model = turn.Usage.Model
			}
			decisionSaved = true
		}
	})
	if err != nil {
		failRemote(err, false)
		return
	}
	if !updated || !decisionSaved {
		return
	}
	decisionMessage := "收到 Agent 节点终态："
	if gate.UsesACPQuestion() {
		decisionMessage = "通过 ACP 反向请求收到 Gate MCP 终态："
	}
	sink(domain.TelemetryEvent{Category: "verification", Name: "gate.submit.received", Phase: "finish", Message: decisionMessage + submitted.Decision, Attributes: map[string]any{"decision": submitted.Decision, "reason": submitted.Reason}})
	if decisionErr, action, reject := applyGateResult(submitted, node.RejectTo); decisionErr != nil {
		if action == "human" {
			e.waitForHumanGate(runID, node.ID, generation, attempt, output, decisionErr, nil)
		} else {
			failRemote(decisionErr, reject)
		}
		return
	}
	directory, err := os.MkdirTemp("", "loopforge-workbuddy-")
	if err != nil {
		failRemote(err, false)
		return
	}
	defer os.RemoveAll(directory)
	outputs := archivedWorkspaceOutputs{workspaceOutputs: workspaceOutputs(directory), save: func(id string, file io.Reader) (domain.ArtifactBlob, error) {
		return e.repo.SaveArtifactBlob(ctx, runID, id, file)
	}}
	if err := client.StageArtifacts(remoteCtx, session, node.ArtifactOutputs, directory, func(message string) {
		sink(domain.TelemetryEvent{Category: "artifact", Name: "artifact.optional.skipped", Phase: "finish", Level: "warn", Message: message})
	}); err != nil {
		failRemote(err, nodeRejects(node))
		return
	}
	if ctx.Err() != nil {
		return
	}
	sink(domain.TelemetryEvent{Category: "lifecycle", Name: "agent.completed", Phase: "finish", Message: "WorkBuddy 托管智能体已完成"})
	e.completeNode(ctx, runID, node, outputs, output, workBuddyManualGate(role), attempt, generation, sink, failRemote)
}

func workBuddyManualGate(role *domain.AgentDefinition) string {
	if role == nil {
		return ""
	}
	for _, gate := range role.Verification {
		if gate.Mode == "manual" && gate.Required {
			return gate.Name
		}
	}
	for _, point := range role.Interventions {
		if point.Required && (point.Stage == "after_execution" || point.Stage == "before_publish") {
			return point.Name
		}
	}
	return ""
}

func workBuddyPrompt(wf domain.WorkflowDefinition, node domain.WorkflowNode, role *domain.AgentDefinition, rules []domain.RuleDefinition, run domain.Run, gate *workbuddy.GateAttempt, artifacts *strings.Replacer) string {
	var prompt strings.Builder
	if role == nil {
		prompt.WriteString(renderPromptSpec(wf, node, node.TaskPrompt, run, artifacts))
	} else {
		// Manual gates are handled by LoopForge after file collection.
		prompt.WriteString(applyPromptPlaceholders(nil, wf, node, &domain.AgentDefinition{Name: role.Name, Prompt: role.Prompt}, run, artifacts))
		for _, rule := range rules {
			if strings.TrimSpace(rule.Content) != "" {
				fmt.Fprintf(&prompt, "\n\n## 规则：%s\n\n%s", rule.Name, rule.Content)
			}
		}
	}
	if len(node.ArtifactOutputs) > 0 {
		prompt.WriteString("\n\n## 交付文件\n在 WorkBuddy 的 /workspace 下按以下相对路径写入文件，并用 PresentFiles 登记产物。登记完再提交节点终态。\n")
		for _, contract := range node.ArtifactOutputs {
			label := "可选"
			if contract.Required {
				label = "必需"
			}
			fmt.Fprintf(&prompt, "- %s（%s）：%s\n", contract.Name, label, contract.Path)
		}
	}
	if gate.UsesACPQuestion() {
		result := appendGateDecisionInstruction(prompt.String(), node.RejectTo, "用 AskUserQuestion 调用 LoopForge Gate")
		example := `{"decision":"pass","reason":"文件已登记"}`
		return result + "\n只发一道题，header 填 `Gate`，选项为 `提交` 和 `取消`，multiSelect 为 false。" +
			"question 字段必须以 `" + gate.GateQuestionPrefix() + "` 开头，后面紧跟一行终态 JSON，例如 `" + example + "`。" +
			"把票据原样放在这次工具调用中。" +
			"收到 Gate MCP 已提交的回答后，简短回复已提交并结束本轮。"
	}
	result := appendSubmitGateInstruction(prompt.String(), node.RejectTo, false) +
		"\n使用 LoopForge Gate 连接器，调用时将 ticket 参数设为 `" + gate.Ticket + "`。仅在工具调用中使用此票据。"
	if gate.ReturnsViaACP() {
		result += "\n工具成功后，把工具返回的 LOOPFORGE_GATE_V1. 开头的签名回执逐字复制到最终回复的独立一行。保持整行原样，请勿改写或构造回执。"
	}
	return result
}

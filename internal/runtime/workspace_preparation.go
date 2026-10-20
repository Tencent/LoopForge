package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"agentflow/internal/domain"
)

// Preparation receipts describe environment reconstruction only. They are not
// workflow node attempts and never imply that business execution was replayed.
type WorkspacePreparationStep struct {
	NodeID      string    `json:"nodeId"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	Output      string    `json:"output,omitempty"`
	FinishedAt  time.Time `json:"finishedAt,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
}

type WorkspacePreparationStatus struct {
	Status        string                     `json:"status"`
	StepName      string                     `json:"stepName,omitempty"`
	Error         string                     `json:"error,omitempty"`
	Steps         []WorkspacePreparationStep `json:"steps"`
	UpdatedAt     time.Time                  `json:"updatedAt,omitempty"`
	Environment   string                     `json:"environment,omitempty"`
	ReplayBlocked bool                       `json:"replayBlocked,omitempty"`
}

func ReadWorkspacePreparationStatus(root string) WorkspacePreparationStatus {
	state := WorkspacePreparationStatus{Status: "unprepared", Steps: []WorkspacePreparationStep{}}
	raw, err := readWorkspaceFile(root, filepath.Join(root, ".agentflow", "workspace-preparation", "state.json"))
	if err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	return state
}
func saveWorkspacePreparationStatus(root string, state WorkspacePreparationStatus) error {
	dir, err := EnsureControlDirectory(root, "workspace-preparation")
	if err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".state-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(raw)
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, "state.json"))
}

// WorkspacePreparationWorkflow keeps the original SOP dependency graph when
// free collaboration removes its business scheduling edges.
func WorkspacePreparationWorkflow(wf domain.WorkflowDefinition) domain.WorkflowDefinition {
	if wf.CollaborationMode == "group" && wf.SOPCheckpoint != nil {
		snapshot := wf.SOPCheckpoint.Workflow
		if snapshot.SandboxSnapshot == nil {
			snapshot.SandboxSnapshot = wf.SandboxSnapshot
		}
		return snapshot
	}
	return wf
}

// PreparationNodes uses the whole frozen graph for ordering. A marked script
// can depend on ordinary business nodes; the control plane only selects marked
// scripts whose normal workflow execution already completed successfully.
func preparationNodes(wf domain.WorkflowDefinition) ([]domain.WorkflowNode, error) {
	wf = WorkspacePreparationWorkflow(wf)
	nodes := make(map[string]domain.WorkflowNode, len(wf.Nodes))
	for _, n := range wf.Nodes {
		nodes[n.ID] = n
	}
	done := map[string]bool{}
	ordered := []domain.WorkflowNode{}
	for len(done) < len(nodes) {
		progress := false
		for _, node := range wf.Nodes {
			if done[node.ID] {
				continue
			}
			blocked := false
			for _, edge := range wf.Edges {
				if edge.Target == node.ID {
					if _, exists := nodes[edge.Source]; !exists {
						return nil, fmt.Errorf("工作区重建步骤依赖不存在的节点 %s", edge.Source)
					}
					if !done[edge.Source] {
						blocked = true
					}
				}
			}
			if blocked {
				continue
			}
			done[node.ID] = true
			progress = true
			if node.RunOnWorkspaceRebuild {
				ordered = append(ordered, node)
			}
		}
		if !progress {
			return nil, fmt.Errorf("工作区重建步骤存在循环依赖")
		}
	}
	return ordered, nil
}
func PreparationNodes(wf domain.WorkflowDefinition) []domain.WorkflowNode {
	nodes, _ := preparationNodes(wf)
	return nodes
}

func preparationFingerprint(node domain.WorkflowNode, repositories []domain.RepositoryContext, snapshot *domain.RunnerSandboxSpec) string {
	raw, _ := json.Marshal(struct {
		Node    domain.WorkflowNode
		Repos   []domain.RepositoryContext
		Sandbox *domain.RunnerSandboxSpec
	}{node, repositories, snapshot})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func preparationQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

var preparationANSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
var preparationSecretKey = regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|passwd|authorization|credential)`)
var preparationSecretAssignment = regexp.MustCompile(`(?i)(\b[\w.-]*(?:api[_-]?key|token|secret|password|passwd|authorization)[\w.-]*["']?\s*[=:]\s*)(?:"[^"]*"|'[^']*'|(?:Bearer\s+)?[^\s,;]+)`)
var preparationURLCredentials = regexp.MustCompile(`(?i)(https?://)[^/\s:@]+:[^/\s@]+@`)

func redactPreparationOutput(sb Sandbox, output string) string {
	secrets := []string{}
	if managed, ok := sb.(*managedSandbox); ok {
		managed.credentialsMu.RLock()
		credentials := managed.credentials
		managed.credentialsMu.RUnlock()
		secrets = append(secrets, credentials.GitToken, credentials.TaihuPAT, credentials.CodeBuddy.APIKey, credentials.Codex.APIKey, credentials.ClaudeCode.APIKey)
		for _, server := range credentials.MCPServers {
			for _, value := range server.Headers {
				secrets = append(secrets, value)
				if strings.HasPrefix(strings.ToLower(value), "bearer ") {
					secrets = append(secrets, value[len("bearer "):])
				}
			}
			for key, value := range server.Environment {
				if preparationSecretKey.MatchString(key) {
					secrets = append(secrets, value)
				}
			}
		}
	}
	for key, value := range sandboxIsolationEnv(sb) {
		if preparationSecretKey.MatchString(key) {
			secrets = append(secrets, value)
		}
	}
	// Replacing a short token first could leave a recognizable suffix of a
	// longer credential that contains it.
	for i := range secrets {
		secrets[i] = strings.TrimSpace(secrets[i])
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		if secret != "" {
			output = strings.ReplaceAll(output, secret, "[redacted]")
		}
	}
	output = preparationANSI.ReplaceAllString(output, "")
	output = preparationSecretAssignment.ReplaceAllString(output, "${1}[redacted]")
	output = preparationURLCredentials.ReplaceAllString(output, "${1}[redacted]@")
	return strings.ToValidUTF8(output, "�")
}

// preparationFailureDetail exposes only a short tail. Installers usually put
// their actionable error after long progress logs, so retaining the beginning
// of their output would hide the failure we need to diagnose.
func preparationFailureDetail(output string) string {
	output = strings.TrimSpace(output)
	lines := []string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// CLIs often append a large help signature after the actual failure.
		// Do not let that boilerplate evict the actionable diagnostic.
		if line == "" || strings.HasPrefix(strings.ToLower(line), "usage:") || strings.Count(line, " [--") >= 2 {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	output = strings.Join(lines, "\n")
	const limit = 2 << 10
	if len(output) > limit {
		start := len(output) - limit + len("…\n")
		for start < len(output) && !utf8.RuneStart(output[start]) {
			start++
		}
		output = "…\n" + output[start:]
	}
	return output
}

func preparationCommand(node domain.WorkflowNode, repo *domain.RepositoryContext) string {
	script := node.Script
	if node.Type == "command" {
		script = node.Command
	}
	if node.ScriptLanguage == "python" && node.Type != "command" {
		script = "python3 -c " + preparationQuote(script)
	}
	scope := node.ExecutionScope
	if scope == "" {
		scope = domain.ExecutionScopeRun
	}
	prefix := "export AGENTFLOW_WORKSPACE=\"$PWD\"\nexport AGENTFLOW_EXECUTION_SCOPE=" + preparationQuote(scope) + "\n"
	if repo != nil {
		prefix += "export AGENTFLOW_REPOSITORY_ID=" + preparationQuote(repo.RepositoryID) + "\nexport AGENTFLOW_REPOSITORY_NAME=" + preparationQuote(repo.Name) + "\nexport AGENTFLOW_REPOSITORY_DIR=" + preparationQuote(repo.WorkspaceDir) + "\ncd -- " + preparationQuote(repo.WorkspaceDir) + " || exit $?\n"
	}
	return prefix + script
}

// EnsureWorkspacePreparation replays the explicitly selected, previously
// completed scripts only when their execution environment was reconstructed.
// Callers must serialize preparation. Changes to source files do not invalidate
// these receipts; ordinary SOP script nodes always execute normally in order.
func EnsureWorkspacePreparation(ctx context.Context, sb Sandbox, wf domain.WorkflowDefinition, repositories []domain.RepositoryContext, sink EventSink) (state WorkspacePreparationStatus, resultErr error) {
	wf = WorkspacePreparationWorkflow(wf)
	nodes, err := preparationNodes(wf)
	if err == nil {
		err = domain.ValidateWorkflowPreparation(wf)
	}
	if err != nil {
		return WorkspacePreparationStatus{Status: "failed", Error: err.Error(), Steps: []WorkspacePreparationStep{}}, err
	}
	epoch, err := SandboxEnvironmentIdentity(ctx, sb)
	if err != nil {
		return WorkspacePreparationStatus{Status: "failed", Error: err.Error(), Steps: []WorkspacePreparationStep{}}, err
	}
	old := ReadWorkspacePreparationStatus(sb.Workspace())
	prior := map[string]WorkspacePreparationStep{}
	if old.Environment == epoch {
		for _, step := range old.Steps {
			prior[step.NodeID] = step
		}
	}
	state = WorkspacePreparationStatus{Status: "preparing", Steps: []WorkspacePreparationStep{}, Environment: epoch, UpdatedAt: time.Now().UTC()}
	if old.Environment == epoch {
		state.Steps = append([]WorkspacePreparationStep(nil), old.Steps...)
	}
	setStep := func(step WorkspacePreparationStep) int {
		for i := range state.Steps {
			if state.Steps[i].NodeID == step.NodeID {
				state.Steps[i] = step
				return i
			}
		}
		state.Steps = append(state.Steps, step)
		return len(state.Steps) - 1
	}

	fail := func(err error) (WorkspacePreparationStatus, error) {
		state.Status = "failed"
		state.Error = err.Error()
		state.UpdatedAt = time.Now().UTC()
		_ = saveWorkspacePreparationStatus(sb.Workspace(), state)
		return state, err
	}
	emit := func(node domain.WorkflowNode, status, message string) {
		if sink != nil {
			sink(domain.TelemetryEvent{Category: "workspace", Name: "workspace.preparation." + status, Message: message, Attributes: map[string]any{"preparationNodeId": node.ID, "preparationNodeName": node.Name}})
		}
	}
	for _, node := range nodes {
		if err = ctx.Err(); err != nil {
			return fail(err)
		}
		fingerprint := preparationFingerprint(node, repositories, wf.SandboxSnapshot)
		cached := prior[node.ID]
		if old.Environment == epoch && (old.Status == "ready" || old.ReplayBlocked) && cached.Fingerprint == "" {
			message := fmt.Sprintf("当前环境缺少已完成脚本 %s 的执行记录；为避免重复操作，请从该节点正常重跑，或重建工作区后再恢复", node.Name)
			state.ReplayBlocked = true
			setStep(WorkspacePreparationStep{NodeID: node.ID, Name: node.Name, Status: "unprepared", Error: message})
			return fail(fmt.Errorf("%s", message))
		}
		if cached.Status == "ready" && cached.Fingerprint == fingerprint {
			setStep(cached)
			continue
		}
		step := WorkspacePreparationStep{NodeID: node.ID, Name: node.Name, Status: "preparing", Fingerprint: fingerprint}
		at := setStep(step)
		state.StepName = node.Name
		state.UpdatedAt = time.Now().UTC()
		if err = saveWorkspacePreparationStatus(sb.Workspace(), state); err != nil {
			return fail(err)
		}
		emit(node, "started", "正在恢复工作区环境："+node.Name)
		var output string
		execute := func(repo *domain.RepositoryContext) error {
			raw, execErr := sb.Exec(ctx, CommandSpec{Argv: []string{"sh", "-lc", preparationCommand(node, repo)}, Provider: "script", CredentialBindings: []CredentialBinding{BindGit, BindMCP}}, nil)
			output += redactPreparationOutput(sb, raw)
			if len(output) > 32<<10 {
				start := len(output) - (32 << 10)
				for start < len(output) && !utf8.RuneStart(output[start]) {
					start++
				}
				output = output[start:]
			}
			return execErr
		}
		if node.ExecutionScope == domain.ExecutionScopeRepositories {
			if len(repositories) == 0 {
				err = fmt.Errorf("重建步骤 %s 需要代码仓库", node.Name)
			} else {
				for i := range repositories {
					err = execute(&repositories[i])
					if err != nil {
						break
					}
				}
			}
		} else {
			err = execute(nil)
		}
		state.Steps[at].Output = output
		state.Steps[at].FinishedAt = time.Now().UTC()
		if err != nil {
			state.Steps[at].Status = "failed"
			failure := fmt.Errorf("恢复工作区 %s：%w", node.Name, err)
			if detail := preparationFailureDetail(output); detail != "" {
				failure = fmt.Errorf("%w\n%s", failure, detail)
			}
			state.Steps[at].Error = failure.Error()
			emit(node, "failed", "工作区环境恢复失败："+node.Name)
			return fail(failure)
		}
		state.Steps[at].Status = "ready"
		emit(node, "completed", "工作区环境恢复完成："+node.Name)
	}
	state.Status = "ready"
	state.StepName = ""
	state.UpdatedAt = time.Now().UTC()
	if err = saveWorkspacePreparationStatus(sb.Workspace(), state); err != nil {
		return fail(err)
	}
	return state, nil
}

// RecordWorkspacePreparation records a real, normally scheduled script success.
// It executes no commands. The next node therefore never repeats that script
// merely because it was marked for future workspace reconstruction.
func RecordWorkspacePreparation(ctx context.Context, sb Sandbox, wf domain.WorkflowDefinition, repositories []domain.RepositoryContext, nodeID, output string) (WorkspacePreparationStatus, error) {
	wf = WorkspacePreparationWorkflow(wf)
	var selected *domain.WorkflowNode
	for i := range wf.Nodes {
		if wf.Nodes[i].ID == nodeID && wf.Nodes[i].RunOnWorkspaceRebuild {
			selected = &wf.Nodes[i]
			break
		}
	}
	if selected == nil {
		return WorkspacePreparationStatus{}, fmt.Errorf("节点 %s 未启用工作区重建执行", nodeID)
	}
	if selected.Type != "script" && selected.Type != "command" {
		return WorkspacePreparationStatus{}, fmt.Errorf("工作区重建节点必须为脚本")
	}
	epoch, err := ReadSandboxEnvironmentIdentity(ctx, sb)
	if err != nil {
		return WorkspacePreparationStatus{}, err
	}
	state := ReadWorkspacePreparationStatus(sb.Workspace())
	if state.Environment != epoch {
		state = WorkspacePreparationStatus{Steps: []WorkspacePreparationStep{}, Environment: epoch}
	}
	if len(output) > 32<<10 {
		output = output[:32<<10]
	}
	step := WorkspacePreparationStep{NodeID: nodeID, Name: selected.Name, Status: "ready", Output: output, Fingerprint: preparationFingerprint(*selected, repositories, wf.SandboxSnapshot), FinishedAt: time.Now().UTC()}
	replaced := false
	for i := range state.Steps {
		if state.Steps[i].NodeID == nodeID {
			state.Steps[i] = step
			replaced = true
			break
		}
	}
	if !replaced {
		state.Steps = append(state.Steps, step)
	}
	state.Status = "ready"
	state.StepName = ""
	state.Error = ""
	state.ReplayBlocked = false
	state.UpdatedAt = time.Now().UTC()
	if err = saveWorkspacePreparationStatus(sb.Workspace(), state); err != nil {
		return state, err
	}
	return state, nil
}

func WorkspacePreparationReusable(state WorkspacePreparationStatus, wf domain.WorkflowDefinition, repositories []domain.RepositoryContext, epoch string) bool {
	wf = WorkspacePreparationWorkflow(wf)
	if state.Status != "ready" || state.Environment != epoch {
		return false
	}
	nodes, err := preparationNodes(wf)
	if err != nil {
		return false
	}
	cached := map[string]WorkspacePreparationStep{}
	for _, step := range state.Steps {
		cached[step.NodeID] = step
	}
	for _, node := range nodes {
		step := cached[node.ID]
		if step.Status != "ready" || step.Fingerprint != preparationFingerprint(node, repositories, wf.SandboxSnapshot) {
			return false
		}
	}
	return true
}
func PublicWorkspacePreparationStatus(state WorkspacePreparationStatus) WorkspacePreparationStatus {
	state.Environment = ""
	state.ReplayBlocked = false
	state.Steps = append([]WorkspacePreparationStep(nil), state.Steps...)
	for i := range state.Steps {
		state.Steps[i].Output = ""
		state.Steps[i].Fingerprint = ""
	}
	return state
}

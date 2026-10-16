package domain

import (
	"errors"
	"strings"
	"time"
)

// ExecutorEffortLevels lists the reasoning-effort levels each built-in CLI
// accepts on its command line. An empty level keeps the CLI default.
var ExecutorEffortLevels = map[string][]string{
	"claude-code": {"low", "medium", "high", "xhigh", "max", "ultracode"},
	"codebuddy":   {"low", "medium", "high", "xhigh", "max", "ultracode"},
	"codex":       {"low", "medium", "high", "xhigh", "max", "ultra"},
}

// ValidateAgentEffort checks a role's reasoning-effort choice against the
// levels published for the resolved executor kind.
func ValidateAgentEffort(effort, executorKind string) error {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return nil
	}
	levels, ok := ExecutorEffortLevels[strings.TrimSpace(executorKind)]
	if !ok {
		return errors.New("当前 CLI 不支持设置思考强度")
	}
	for _, level := range levels {
		if level == effort {
			return nil
		}
	}
	return errors.New("思考强度取值不被当前 CLI 支持")
}

// BaselineAlignAgentID is the platform-shared role that only merges the
// default branch into the integrate baseline. Project issue-fix agents must
// not be reused for this job.
const BaselineAlignAgentID = "baseline-align-resolver"

// PlatformMCPServerID is the reserved id of AgentFlow's own platform MCP
// server (Streamable HTTP at /mcp). It is a synthetic binding resolved by the
// engine at run time, never a row in the mcp_servers table.
const PlatformMCPServerID = "agentflow-platform"

type AgentDefinition struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Version      int             `json:"version"`
	ProjectID    string          `json:"projectId,omitempty"`
	Shared       bool            `json:"shared,omitempty"`
	Description  string          `json:"description"`
	Instructions string          `json:"instructions"`
	TaskPrompt   *TaskPromptSpec `json:"taskPrompt,omitempty"`
	Rules        []string        `json:"rules"`
	Skills       []string        `json:"skills"`
	MCPServers   []string        `json:"mcpServers"`
	// PlatformMCP binds the reserved agentflow-platform MCP to this role as an
	// independent toggle (not an mcpServers entry). When enabled, every run
	// invocation of this role receives the platform's public operations
	// (issues, tasks, runs, projects, ...) as MCP tools.
	PlatformMCP bool `json:"platformMcp,omitempty"`
	// KnowledgeBase binds the reserved read-only agentflow-knowledge MCP and
	// the built-in knowledge usage rule. Upstream credentials stay on the
	// platform; the role only receives a short-lived project-scoped token.
	KnowledgeBase   bool                     `json:"knowledgeBase,omitempty"`
	Hooks           []AgentHook              `json:"hooks,omitempty"`
	ExecutorID      string                   `json:"executorId,omitempty"`
	Model           string                   `json:"model,omitempty"`
	Effort          string                   `json:"effort,omitempty"`
	Loop            AgentLoopPolicy          `json:"loop"`
	Interventions   []HumanInterventionPoint `json:"interventions,omitempty"`
	ArtifactOutputs []ArtifactContract       `json:"artifactOutputs,omitempty"`
	Verification    []VerificationGate       `json:"verification"`
	UpdatedAt       time.Time                `json:"updatedAt"`
}

// AgentHook is a role-scoped lifecycle script. The runtime compiles the common
// subset into the native hook configuration of Claude Code, Codex, or CodeBuddy
// only for invocations of this Agent.
type AgentHook struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Event      string `json:"event"` // SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop
	Matcher    string `json:"matcher,omitempty"`
	Language   string `json:"language"` // shell, python
	Script     string `json:"script"`
	TimeoutSec int    `json:"timeoutSec"`
	// ExecutionScope controls whether the hook runs once at the Run workspace
	// root or once for every repository checkout. Empty keeps legacy Run scope.
	ExecutionScope string `json:"executionScope,omitempty"`
}

// AgentLoopPolicy belongs to the reusable Agent role rather than an SOP node.
// A node may still cap attempts, while this policy controls self-correction
// inside one Agent invocation and one sandbox workspace.
type AgentLoopPolicy struct {
	Enabled        bool   `json:"enabled"`
	MaxIterations  int    `json:"maxIterations"`
	ExitCondition  string `json:"exitCondition"` // all_gates_pass, executor_success
	OnExhausted    string `json:"onExhausted"`   // fail, human
	FeedbackPrompt string `json:"feedbackPrompt,omitempty"`
}

// HumanInterventionPoint declares a reusable checkpoint owned by the Agent.
// after_execution and before_publish are enforced by the current engine;
// before_execution and on_failure are reserved explicitly for remote workers.
type HumanInterventionPoint struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Stage        string `json:"stage"` // before_execution, after_execution, before_publish, on_failure
	Required     bool   `json:"required"`
	Instructions string `json:"instructions,omitempty"`
}

type ExecutorDefinition struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	ProjectID string   `json:"projectId,omitempty"`
	Kind      string   `json:"kind"` // mock, claude-code, codex, codebuddy
	Binary    string   `json:"binary"`
	Args      []string `json:"args"`
	Protocol  string   `json:"protocol"`
	Model     string   `json:"model,omitempty"`
	Effort    string   `json:"effort,omitempty"`
}

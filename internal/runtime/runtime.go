package runtime

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

type EventSink func(event domain.TelemetryEvent)

type CredentialBinding string

const (
	BindCodeBuddy  CredentialBinding = "codebuddy"
	BindCodex      CredentialBinding = "codex"
	BindClaudeCode CredentialBinding = "claude-code"
	BindGit        CredentialBinding = "git"
	BindMCP        CredentialBinding = "mcp"
)

type CodeBuddyCredential struct {
	APIKey              string
	InternetEnvironment string
	// Sponsored is set when the responsible user has no key of their own and
	// the platform's shared key is used; such runs are limited to
	// SponsoredModels (the default first).
	Sponsored       bool
	SponsoredModels []string
}

// RunCredentials lives only on the in-memory run sandbox handle. It is never
// serialized into domain objects, run-context.json, or workspace files.
type RunCredentials struct {
	CodeBuddy    CodeBuddyCredential
	Codex        ProviderCredential
	ClaudeCode   ProviderCredential
	GitToken     string
	GitUserName  string
	GitUserEmail string
	MCPServers   []domain.MCPServerDefinition
}

type ProviderCredential struct {
	APIKey  string
	BaseURL string
}

type InvocationOptions struct {
	RunID           string
	NodeID          string
	ResumeSessionID string
	GateResultPath  string
	Repositories    []domain.RepositoryContext
	// BrowserMCP is resolved on the executing host, never sent to a runner.
	BrowserMCP *domain.MCPServerDefinition `json:"-"`
}

// CommandSpec is the closed process-launch contract between providers/engine
// and a sandbox. Callers can request known credential bindings and runtime
// features, but cannot supply arbitrary environment variables.
type CommandSpec struct {
	Argv               []string
	Provider           string
	CredentialBindings []CredentialBinding
	PrivateHome        bool
	HookOutput         string
	Telemetry          *InvocationOptions
	environment        map[string]string
}

// WithPrivateHomeDir keeps CodeBuddy/Claude isolated from the operator HOME
// while reusing a durable directory so --resume can see prior transcripts.
func (c CommandSpec) WithPrivateHomeDir(dir string) CommandSpec {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return c
	}
	env := make(map[string]string, len(c.environment)+2)
	for key, value := range c.environment {
		env[key] = value
	}
	env["HOME"] = dir
	if c.Provider == "codex" {
		env["CODEX_HOME"] = filepath.Join(dir, ".codex")
	}
	if c.Provider == "claude-code" {
		env["CLAUDE_CONFIG_DIR"] = filepath.Join(dir, ".claude")
	}
	env["TMPDIR"] = filepath.Join(dir, "tmp")
	if c.Provider == "codebuddy" {
		env["CODEBUDDY_CONFIG_DIR"] = filepath.Join(dir, ".codebuddy")
	}
	c.environment = env
	c.PrivateHome = false
	return c
}

// WithMCPStartupWait lets a CodeBuddy -p process wait up to timeout for MCP
// servers that expose always-loaded tools before its first model request.
// The headless default is about 2s; a server that connects later is missing
// from that request's tool list, and its non-deferred tools are not in the
// ToolSearch index either. The wait returns as soon as those servers settle.
func (c CommandSpec) WithMCPStartupWait(timeout time.Duration) CommandSpec {
	if c.Provider != "codebuddy" || timeout <= 0 {
		return c
	}
	env := make(map[string]string, len(c.environment)+1)
	for key, value := range c.environment {
		env[key] = value
	}
	env[codeBuddyMCPStartupWaitEnv] = strconv.FormatInt(timeout.Milliseconds(), 10)
	c.environment = env
	return c
}

const codeBuddyMCPStartupWaitEnv = "CODEBUDDY_FIRST_RUN_MCP_PREWAIT_TIMEOUT_MS"

type Sandbox interface {
	ID() string
	Workspace() string
	Exec(ctx context.Context, command CommandSpec, sink EventSink) (string, error)
	Close(ctx context.Context) error
}

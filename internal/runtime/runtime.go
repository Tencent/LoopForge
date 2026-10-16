package runtime

import (
	"context"
	"path/filepath"
	"strings"

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
}

// RunCredentials lives only on the in-memory run sandbox handle. It is never
// serialized into domain objects, run-context.json, or workspace files.
type RunCredentials struct {
	TaihuPAT     string
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
	Concurrent      bool
	RunID           string
	NodeID          string
	ResumeSessionID string
	GateResultPath  string
	Repositories    []domain.RepositoryContext
}

// CommandSpec is the closed process-launch contract between providers/engine
// and a sandbox. Callers can request known credential bindings and runtime
// features, but cannot supply arbitrary environment variables.
type CommandSpec struct {
	Concurrent         bool
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
	c.environment = env
	c.PrivateHome = false
	return c
}

type Sandbox interface {
	ID() string
	Workspace() string
	Exec(ctx context.Context, command CommandSpec, sink EventSink) (string, error)
	Close(ctx context.Context) error
}

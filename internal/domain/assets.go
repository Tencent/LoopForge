package domain

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type RuleDefinition struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ProjectID   string `json:"projectId,omitempty"`
	Shared      bool   `json:"shared,omitempty"`
	Description string `json:"description"`
	Content     string `json:"content"`
	Priority    int    `json:"priority"`
	// Global marks the rule as a workflow-wide constraint. When true, every
	// node of a workflow listed in WorkflowIDs receives this rule at run time,
	// in addition to the rules already bound on that node's role.
	Global      bool      `json:"global,omitempty"`
	WorkflowIDs []string  `json:"workflowIds,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type SkillDefinition struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	ProjectID   string            `json:"projectId,omitempty"`
	Shared      bool              `json:"shared,omitempty"`
	Description string            `json:"description"`
	Content     string            `json:"content"`
	Files       map[string]string `json:"files,omitempty"`
	// FileRefs is the manifest of binary assets stored in the skill_files
	// table (one row per file). Only metadata travels in the payload; the
	// content is loaded on demand when a run is dispatched.
	FileRefs  []SkillFileRef `json:"fileRefs,omitempty"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// SkillFileRef describes one binary file of a skill. Path is relative to the
// skill directory and validated by ValidateRelativeFilePath.
type SkillFileRef struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
	// Mode is the unix permission bits (e.g. 0o755) preserved from the
	// upload so executable scripts keep their exec bit in the sandbox.
	Mode      uint32    `json:"mode,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

type MCPServerDefinition struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	ProjectID        string            `json:"projectId,omitempty"`
	MarketID         string            `json:"marketId,omitempty"`
	Scope            string            `json:"scope,omitempty"` // project, personal; empty is a legacy shared asset
	OwnerUserID      int64             `json:"ownerUserId,omitempty"`
	IconURL          string            `json:"iconUrl,omitempty"`
	Description      string            `json:"description"`
	Transport        string            `json:"transport"` // stdio, http
	Command          string            `json:"command,omitempty"`
	Args             []string          `json:"args,omitempty"`
	URL              string            `json:"url,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	InstallScript    string            `json:"installScript,omitempty"`
	WorkingDirectory string            `json:"workingDirectory,omitempty"`
	PathEntries      []string          `json:"pathEntries,omitempty"`
	FileRefs         []MCPFileRef      `json:"fileRefs,omitempty"`
	AuthType         string            `json:"authType,omitempty"` // empty, taihu-pat
	TimeoutSec       int               `json:"timeoutSec"`
	UpdatedAt        time.Time         `json:"updatedAt"`
}

type MCPFileRef struct {
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256,omitempty"`
	Mode      uint32    `json:"mode,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

type AgentAssetBundle struct {
	Rules      []RuleDefinition      `json:"rules"`
	Skills     []SkillDefinition     `json:"skills"`
	MCPServers []MCPServerDefinition `json:"mcpServers"`
	// SkillFiles carries binary skill file content (skillID -> path ->
	// bytes) next to the definitions so both the local runtime and remote
	// runner machines can materialize them into the sandbox.
	SkillFiles map[string]map[string][]byte `json:"skillFiles,omitempty"`
	MCPFiles   map[string]map[string][]byte `json:"mcpFiles,omitempty"`
}

// DockerfileDefinition is a Studio-managed build recipe. Content and Files are
// stored on the catalog row in MySQL, the same way EnvironmentDefinition stores
// install.sh. Docker sandboxes select one of these; the runtime materializes a
// temporary build context from the stored payload at sandbox initialization.
type DockerfileDefinition struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	ProjectID   string            `json:"projectId,omitempty"`
	Shared      bool              `json:"shared,omitempty"`
	Description string            `json:"description"`
	Content     string            `json:"content"`
	Files       map[string]string `json:"files,omitempty"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

// EnvironmentDefinition is a named execution toolchain for process sandboxes.
// InstallScript is stored on the environment asset and loaded from the catalog
// at sandbox init; it runs once with PREFIX set to the isolated environment root.
type EnvironmentDefinition struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	ProjectID     string            `json:"projectId,omitempty"`
	Shared        bool              `json:"shared,omitempty"`
	Description   string            `json:"description"`
	Variables     map[string]string `json:"variables,omitempty"`
	PathEntries   []string          `json:"pathEntries,omitempty"`
	InstallScript string            `json:"installScript,omitempty"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

type SandboxProfile struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	ProjectID      string            `json:"projectId,omitempty"`
	Shared         bool              `json:"shared,omitempty"`
	Driver         string            `json:"driver"` // local, seatbelt, bwrap, docker
	DockerfileID   string            `json:"dockerfileId,omitempty"`
	EnvironmentID  string            `json:"environmentId,omitempty"`
	ReadOnlyRootFS bool              `json:"readOnlyRootFS"`
	Environment    map[string]string `json:"environment,omitempty"`
}

func ValidateDockerfile(v DockerfileDefinition) error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Name) == "" {
		return fmt.Errorf("Dockerfile 需要 id 和 name")
	}
	if strings.TrimSpace(v.Content) == "" {
		return fmt.Errorf("Dockerfile 内容不能为空")
	}
	return ValidateManagedFiles(v.Files)
}

func ValidateEnvironment(v EnvironmentDefinition) error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Name) == "" {
		return fmt.Errorf("环境需要 id 和 name")
	}
	if err := ValidateEnvironmentVariables(v.Variables); err != nil {
		return err
	}
	for _, entry := range v.PathEntries {
		if err := ValidateRelativeFilePath(entry); err != nil {
			return fmt.Errorf("pathEntries: %w", err)
		}
	}
	if strings.ContainsRune(v.InstallScript, '\x00') {
		return fmt.Errorf("install.sh 不能包含 NUL")
	}
	return nil
}

func ValidateEnvironmentVariables(values map[string]string) error {
	for key, value := range values {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("invalid environment variable name %q", key)
		}
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("environment variable %q contains NUL", key)
		}
	}
	return nil
}

func ValidateManagedFiles(files map[string]string) error {
	for name := range files {
		if err := ValidateRelativeFilePath(name); err != nil {
			return err
		}
	}
	return nil
}

// Binary skill upload limits, enforced by the HTTP API.
const (
	// SkillFileMaxBytes caps one uploaded skill file (1MB).
	SkillFileMaxBytes int64 = 1 << 20
	// SkillMaxFileCount caps the number of files per skill (text + binary).
	SkillMaxFileCount = 500
	// SkillTotalMaxBytes caps the combined size of all files per skill (64MB).
	SkillTotalMaxBytes int64 = 64 << 20
	MCPFileMaxBytes          = SkillFileMaxBytes
	MCPMaxFileCount          = SkillMaxFileCount
	MCPTotalMaxBytes         = SkillTotalMaxBytes
)

// NormalizeSkillFileMode keeps only the unix permission bits and defaults to
// 0o644 when the stored mode carries no permission bits.
func NormalizeSkillFileMode(mode uint32) uint32 {
	if mode&0o777 == 0 {
		return 0o644
	}
	return mode & 0o777
}

// ValidateSkillFileRefs checks the binary manifest paths.
func ValidateSkillFileRefs(refs []SkillFileRef) error {
	for _, ref := range refs {
		if err := ValidateRelativeFilePath(ref.Path); err != nil {
			return err
		}
		if ref.Size < 0 || ref.Size > SkillFileMaxBytes {
			return fmt.Errorf("skill file %s exceeds the 1MB limit", ref.Path)
		}
	}
	return nil
}

func ValidateRelativeFilePath(name string) error {
	clean := filepath.ToSlash(strings.TrimSpace(name))
	if clean == "" || clean == "." {
		return fmt.Errorf("invalid file path %q", name)
	}
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "/") || strings.Contains(clean, ":") {
		return fmt.Errorf("invalid file path %q", name)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid file path %q", name)
		}
	}
	return nil
}

func ValidateMCPServer(v MCPServerDefinition) error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Name) == "" {
		return fmt.Errorf("MCP 需要 id 和 name")
	}
	switch strings.TrimSpace(v.Transport) {
	case "http":
		if !strings.HasPrefix(v.URL, "http://") && !strings.HasPrefix(v.URL, "https://") {
			return fmt.Errorf("HTTP MCP 需要有效的 url")
		}
	case "stdio", "":
		if strings.TrimSpace(v.Command) == "" {
			return fmt.Errorf("stdio MCP 需要 command")
		}
		if strings.ContainsRune(v.InstallScript, '\x00') {
			return fmt.Errorf("MCP 安装脚本不能包含 NUL")
		}
		if cwd := strings.TrimSpace(v.WorkingDirectory); cwd != "" {
			if err := ValidateRelativeFilePath(cwd); err != nil {
				return fmt.Errorf("workingDirectory: %w", err)
			}
		}
		for _, entry := range v.PathEntries {
			if err := ValidateRelativeFilePath(entry); err != nil {
				return fmt.Errorf("pathEntries: %w", err)
			}
		}
		for _, ref := range v.FileRefs {
			if err := ValidateRelativeFilePath(ref.Path); err != nil {
				return err
			}
			if ref.Size < 0 || ref.Size > MCPFileMaxBytes {
				return fmt.Errorf("MCP file %s exceeds the 1MB limit", ref.Path)
			}
		}
	default:
		return fmt.Errorf("transport 只能是 stdio 或 http")
	}
	if v.TimeoutSec < 0 || v.TimeoutSec > 600 {
		return fmt.Errorf("timeoutSec 需在 0–600 之间")
	}
	switch strings.TrimSpace(v.Scope) {
	case "":
	case "project":
		if strings.TrimSpace(v.ProjectID) == "" {
			return fmt.Errorf("项目级 MCP 需要 projectId")
		}
		if v.OwnerUserID != 0 {
			return fmt.Errorf("项目级 MCP 不能设置 ownerUserId")
		}
	case "personal":
		if v.OwnerUserID <= 0 {
			return fmt.Errorf("个人级 MCP 需要 ownerUserId")
		}
		if strings.TrimSpace(v.ProjectID) != "" {
			return fmt.Errorf("个人级 MCP 不能设置 projectId")
		}
		if strings.TrimSpace(v.MarketID) != "" {
			return fmt.Errorf("市场 MCP 只能安装到项目，个人级 MCP 必须为自定义")
		}
	default:
		return fmt.Errorf("scope 只能为空、project 或 personal")
	}
	switch strings.TrimSpace(v.AuthType) {
	case "", "taihu-pat", "github-token":
	default:
		return fmt.Errorf("authType must be empty, taihu-pat, or github-token")
	}
	if (strings.TrimSpace(v.AuthType) == "taihu-pat" || v.AuthType == "github-token") && strings.TrimSpace(v.Transport) != "http" {
		return fmt.Errorf("太湖个人令牌认证仅支持 HTTP MCP")
	}
	if strings.TrimSpace(v.AuthType) == "taihu-pat" || v.AuthType == "github-token" {
		for key := range v.Headers {
			if strings.EqualFold(strings.TrimSpace(key), "authorization") {
				return fmt.Errorf("太湖个人令牌认证不允许在资产 Headers 中保存 Authorization")
			}
		}
	}
	return nil
}

// ParseMCPImport accepts one object, an array, {mcpServers|servers: ...},
// or a map keyed by server id (the on-disk store shape).
func ParseMCPImport(raw json.RawMessage) ([]MCPServerDefinition, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, fmt.Errorf("空的 MCP JSON")
	}
	raw = json.RawMessage(trimmed)
	var many []MCPServerDefinition
	if err := json.Unmarshal(raw, &many); err == nil {
		if len(many) == 0 {
			return nil, fmt.Errorf("空的 MCP 列表")
		}
		return many, nil
	}
	var one MCPServerDefinition
	if err := json.Unmarshal(raw, &one); err == nil && strings.TrimSpace(one.ID) != "" && strings.TrimSpace(one.Name) != "" {
		return []MCPServerDefinition{one}, nil
	}
	var envelope struct {
		MCPServers json.RawMessage `json:"mcpServers"`
		Servers    json.RawMessage `json:"servers"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		nested := envelope.MCPServers
		if len(nested) == 0 {
			nested = envelope.Servers
		}
		if len(nested) > 0 {
			return ParseMCPImport(nested)
		}
	}
	var asMap map[string]MCPServerDefinition
	if err := json.Unmarshal(raw, &asMap); err == nil && len(asMap) > 0 {
		out := make([]MCPServerDefinition, 0, len(asMap))
		for id, v := range asMap {
			if strings.TrimSpace(v.ID) == "" {
				v.ID = id
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, fmt.Errorf("JSON 不是 MCP 对象、数组或 mcpServers 映射")
}

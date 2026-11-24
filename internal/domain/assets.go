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
	FileRefs []SkillFileRef `json:"fileRefs,omitempty"`
	// Source records where an installed skill came from. A non-nil source
	// marks the files as a read-only snapshot that only the market update
	// flow may replace.
	Source    *SkillSource `json:"source,omitempty"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

// SkillSourceSkillHub is the provider of skills installed from skillhub.cn.
const SkillSourceSkillHub = "skillhub"

// SkillSource identifies the market version a skill snapshot was taken from.
// SkillHub slugs are only unique per publisher namespace, so both are kept.
type SkillSource struct {
	Provider    string    `json:"provider"`
	Namespace   string    `json:"namespace,omitempty"`
	Slug        string    `json:"slug"`
	Version     string    `json:"version"`
	Homepage    string    `json:"homepage,omitempty"`
	InstalledAt time.Time `json:"installedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
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
	AuthType         string            `json:"authType,omitempty"` // empty, github-token, credential
	// CredentialKind names the platform-managed credential (RunCredentials.Extra
	// key) bound as the Authorization Bearer header when AuthType is
	// "credential". The stored asset never holds the secret itself.
	CredentialKind string    `json:"credentialKind,omitempty"`
	TimeoutSec     int       `json:"timeoutSec"`
	UpdatedAt      time.Time `json:"updatedAt"`
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

// DockerfileDefinition is the build recipe a runner spec carries for a docker
// sandbox, derived from the profile's Dockerfile (ID and Name are the
// sandbox's). The runtime materializes a temporary build context from it at
// sandbox initialization. Run snapshots frozen before the recipe moved into
// the sandbox carry the former catalog fields.
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

type SandboxProfile struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	ProjectID      string            `json:"projectId,omitempty"`
	Shared         bool              `json:"shared,omitempty"`
	Driver         string            `json:"driver"` // native, docker (seatbelt / bwrap are resolved native values)
	ReadOnlyRootFS bool              `json:"readOnlyRootFS"`
	Environment    map[string]string `json:"environment,omitempty"`
	// Dockerfile is the image recipe of a docker sandbox.
	Dockerfile *SandboxDockerfile `json:"dockerfile,omitempty"`
	// DockerfileID names the recipe in runner specs (see RunnerSandbox);
	// stored profiles leave it empty.
	DockerfileID string `json:"dockerfileId,omitempty"`
}

// SandboxDockerfile is a Dockerfile and the files of its build context.
type SandboxDockerfile struct {
	Content string            `json:"content"`
	Files   map[string]string `json:"files,omitempty"`
}

// ValidateSandboxAssets checks the sandbox driver and the recipe it needs:
// docker requires a Dockerfile, native carries only environment variables.
func ValidateSandboxAssets(v SandboxProfile) error {
	switch strings.TrimSpace(v.Driver) {
	case SandboxDriverDocker, SandboxDriverNative, SandboxDriverSeatbelt, SandboxDriverBwrap:
	case SandboxDriverLocal:
		return fmt.Errorf("local 沙箱已移除，请改用 native 沙箱")
	default:
		return fmt.Errorf("不支持的沙箱类型 %q", v.Driver)
	}
	docker := strings.TrimSpace(v.Driver) == SandboxDriverDocker
	switch {
	case docker && (v.Dockerfile == nil || strings.TrimSpace(v.Dockerfile.Content) == ""):
		return fmt.Errorf("Docker 沙箱需要 Dockerfile 内容")
	case !docker && v.Dockerfile != nil:
		return fmt.Errorf("只有 Docker 沙箱可以配置 Dockerfile")
	}
	if v.Dockerfile != nil {
		if err := ValidateManagedFiles(v.Dockerfile.Files); err != nil {
			return fmt.Errorf("构建上下文：%w", err)
		}
	}
	return ValidateEnvironmentVariables(v.Environment)
}

// RunnerSandbox is the runner spec of a stored profile: its Dockerfile
// becomes the recipe a runner builds, named after the sandbox. The spec's
// profile drops the embedded copy.
func RunnerSandbox(profile SandboxProfile) *RunnerSandboxSpec {
	spec := &RunnerSandboxSpec{Profile: profile}
	spec.Profile.Dockerfile = nil
	if d := profile.Dockerfile; d != nil && strings.TrimSpace(d.Content) != "" {
		spec.Profile.DockerfileID = profile.ID
		spec.Dockerfile = &DockerfileDefinition{ID: profile.ID, Name: profile.Name, ProjectID: profile.ProjectID, Shared: profile.Shared, Content: d.Content, Files: d.Files}
	}
	return spec
}

// Sandbox drivers. Profiles store native or docker; native is the OS
// lightweight process sandbox and resolves on the executing machine to
// seatbelt (macOS) or bwrap (Linux). seatbelt / bwrap remain valid resolved
// values (runner task payloads, rows written before the native option).
// local is the runtime's unisolated driver for internal callers; no profile
// may store it.
const (
	SandboxDriverLocal    = "local"
	SandboxDriverNative   = "native"
	SandboxDriverDocker   = "docker"
	SandboxDriverSeatbelt = "seatbelt"
	SandboxDriverBwrap    = "bwrap"
)

// ConversationSandboxDriver reports whether a conversation workspace can run
// in a sandbox of driver: Docker, or the native lightweight sandbox (stored as
// native, or already resolved to seatbelt / bwrap).
func ConversationSandboxDriver(driver string) bool {
	switch strings.TrimSpace(driver) {
	case SandboxDriverDocker, SandboxDriverNative, SandboxDriverSeatbelt, SandboxDriverBwrap:
		return true
	default:
		return false
	}
}

// ValidateFrozenSandbox rejects the removed local driver in a sandbox frozen
// into a run before it was removed: the runtime would run it unisolated.
func ValidateFrozenSandbox(profile SandboxProfile) error {
	if strings.TrimSpace(profile.Driver) == SandboxDriverLocal {
		return fmt.Errorf("沙箱 %s 使用已移除的 local 类型，请改用 native 沙箱后重新发起", profile.ID)
	}
	return nil
}

// ResolveSandboxDriver maps native onto the lightweight sandbox of goos and
// returns every other driver unchanged. An empty goos (a runner that never
// reported its OS) leaves native for that machine to resolve itself.
func ResolveSandboxDriver(driver, goos string) (string, error) {
	driver = strings.TrimSpace(driver)
	if driver != SandboxDriverNative {
		return driver, nil
	}
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case "darwin":
		return SandboxDriverSeatbelt, nil
	case "linux":
		return SandboxDriverBwrap, nil
	case "":
		return driver, nil
	default:
		return "", fmt.Errorf("系统轻量隔离（native）仅支持 macOS 与 Linux，当前执行机器为 %s", goos)
	}
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
	if err := validateMCPTransport(v); err != nil {
		return err
	}
	if v.TimeoutSec < 0 || v.TimeoutSec > 600 {
		return fmt.Errorf("timeoutSec 需在 0–600 之间")
	}
	if err := validateMCPScope(v); err != nil {
		return err
	}
	return validateMCPAuth(v)
}

// validateMCPTransport checks the launch settings required by v.Transport.
func validateMCPTransport(v MCPServerDefinition) error {
	switch strings.TrimSpace(v.Transport) {
	case "http":
		if !strings.HasPrefix(v.URL, "http://") && !strings.HasPrefix(v.URL, "https://") {
			return fmt.Errorf("HTTP MCP 需要有效的 url")
		}
		return nil
	case "stdio", "":
		return validateMCPStdio(v)
	default:
		return fmt.Errorf("transport 只能是 stdio 或 http")
	}
}

// validateMCPStdio checks the command, install script and workspace-relative
// paths of a stdio MCP server.
func validateMCPStdio(v MCPServerDefinition) error {
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
	return nil
}

// validateMCPScope checks that project and personal servers carry exactly the
// owner fields their scope allows.
func validateMCPScope(v MCPServerDefinition) error {
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
	return nil
}

// validateMCPAuth checks v.AuthType and keeps token auth on HTTP
// servers without a stored Authorization header.
func validateMCPAuth(v MCPServerDefinition) error {
	switch strings.TrimSpace(v.AuthType) {
	case "", "github-token", "credential":
	default:
		return fmt.Errorf("authType must be empty, github-token or credential")
	}
	if v.AuthType == "github-token" && strings.TrimSpace(v.Transport) != "http" {
		return fmt.Errorf("GitHub 令牌认证仅支持 HTTP MCP")
	}
	if v.AuthType == "credential" {
		if strings.TrimSpace(v.Transport) != "http" {
			return fmt.Errorf("credential authentication requires an HTTP MCP")
		}
		if strings.TrimSpace(v.CredentialKind) == "" {
			return fmt.Errorf("credential authentication requires credentialKind")
		}
	}
	if v.AuthType == "github-token" {
		for key := range v.Headers {
			if strings.EqualFold(strings.TrimSpace(key), "authorization") {
				return fmt.Errorf("GitHub 令牌认证不允许在资产 Headers 中保存 Authorization")
			}
		}
	}
	if v.AuthType == "credential" {
		for key := range v.Headers {
			if strings.EqualFold(strings.TrimSpace(key), "authorization") {
				return fmt.Errorf("credential authentication must not store an Authorization header on the asset")
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

package runtime

import (
	"agentflow/internal/domain"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type compiledConfig struct {
	Args        []string
	AgentRoot   string
	RuntimeRoot string
	HookFile    string
	HookOutput  string
	Hooks       []compiledHook
	HasSkills   bool
	HasMCP      bool
}

func materialize(workspace, invocationID string, ex domain.ExecutorDefinition, a domain.AgentDefinition, assets domain.AgentAssetBundle) (compiledConfig, error) {
	return materializeInvocation(workspace, invocationID, ex, a, assets, InvocationOptions{})
}

func materializeInvocation(workspace, invocationID string, ex domain.ExecutorDefinition, a domain.AgentDefinition, assets domain.AgentAssetBundle, options InvocationOptions) (compiledConfig, error) {
	return materializeInvocationForDriver(workspace, invocationID, ex, a, assets, options, "local")
}

func materializeInvocationForDriver(workspace, invocationID string, ex domain.ExecutorDefinition, a domain.AgentDefinition, assets domain.AgentAssetBundle, options InvocationOptions, driver string) (compiledConfig, error) {
	invocationID = safeID(invocationID)
	if invocationID == "" {
		return compiledConfig{}, fmt.Errorf("empty invocation id")
	}
	agentID := safeID(a.ID)
	if agentID == "" {
		return compiledConfig{}, fmt.Errorf("agent id is required for runtime isolation")
	}
	if reservedAgentID(agentID) {
		return compiledConfig{}, fmt.Errorf("agent id %q conflicts with an AgentFlow control directory", agentID)
	}
	effectiveHookDefinitions, err := mergeAgentHookDefinitions(providerForKind(ex.Kind).SystemHooks(), a.Hooks)
	if err != nil {
		return compiledConfig{}, err
	}
	if options.Concurrent {
		agentID += "-" + invocationID
	}
	agentRel := filepath.Join(".agentflow", agentID)
	runtimeRel := filepath.Join(".agentflow", "runtime", invocationID)
	compiled := compiledConfig{AgentRoot: filepath.ToSlash(agentRel), RuntimeRoot: filepath.ToSlash(runtimeRel), HasSkills: len(assets.Skills) > 0}
	// run-context.json is a debugging artifact; keep binary skill payloads
	// out of it (they travel with the bundle, not the workspace dump).
	slimAssets := assets
	slimAssets.SkillFiles = nil
	slimAssets.MCPFiles = nil
	pkg := map[string]any{"agent": a, "assets": slimAssets, "executor": ex, "compiledAt": time.Now().UTC()}
	raw, _ := json.MarshalIndent(pkg, "", "  ")
	root, err := prepareRuntimeRoot(workspace, invocationID)
	if err != nil {
		return compiled, err
	}
	if err := os.WriteFile(filepath.Join(root, "run-context.json"), raw, 0o600); err != nil {
		return compiled, err
	}
	if len(effectiveHookDefinitions) > 0 || ex.Kind == "codebuddy" {
		observationRoot := filepath.Join(root, "observability")
		if err := os.MkdirAll(observationRoot, 0o700); err != nil {
			return compiled, err
		}
		compiled.HookFile = filepath.Join(observationRoot, "hooks.ndjson")
		compiled.HookOutput = filepath.ToSlash(filepath.Join(runtimeRel, "observability", "hooks.ndjson"))
	}
	for _, dir := range []string{"skills", "rules", "mcp", "hooks", "config", "bindings"} {
		if _, err := EnsureControlDirectory(workspace, dir); err != nil {
			return compiled, err
		}
	}
	bindingRoot, err := resetControlDirectory(workspace, "bindings", agentID)
	if err != nil {
		return compiled, err
	}
	configRoot, err := resetControlDirectory(workspace, "config", agentID)
	if err != nil {
		return compiled, err
	}
	compiled.Hooks, err = materializeAgentHooks(configRoot, agentRel, ex.Kind, effectiveHookDefinitions)
	if err != nil {
		return compiled, err
	}
	agentRoot, err := resetControlDirectory(workspace, agentID)
	if err != nil {
		return compiled, err
	}
	for _, kind := range []string{"skills", "rules", "mcp"} {
		if err := os.Mkdir(filepath.Join(bindingRoot, kind), 0o700); err != nil {
			return compiled, err
		}
		if err := replaceRelativeSymlink(filepath.Join(agentRoot, kind), filepath.Join("..", "bindings", agentID, kind)); err != nil {
			return compiled, err
		}
	}
	lines := []string{"# " + a.Name, "", a.Instructions, "", "## Rules"}
	sort.Slice(assets.Rules, func(i, j int) bool { return assets.Rules[i].Priority > assets.Rules[j].Priority })
	for _, v := range assets.Rules {
		ruleID := safeID(v.ID)
		if ruleID == "" {
			return compiled, fmt.Errorf("rule id is required")
		}
		ruleFile := filepath.Join(workspace, ".agentflow", "rules", ruleID+".md")
		if err := writeManagedFile(ruleFile, []byte(v.Content), 0o600); err != nil {
			return compiled, err
		}
		if err := replaceRelativeSymlink(filepath.Join(bindingRoot, "rules", ruleID+".md"), filepath.Join("..", "..", "..", "rules", ruleID+".md")); err != nil {
			return compiled, err
		}
		lines = append(lines, "", "### "+v.Name, v.Content)
	}
	selectedSkills := make([]selectedSkill, 0, len(assets.Skills))
	for _, skill := range assets.Skills {
		item, err := compileSelectedSkill(agentRel, skill, assets.SkillFiles[skill.ID])
		if err != nil {
			return compiled, err
		}
		selectedSkills = append(selectedSkills, item)
	}
	lines = append(lines, "", "## Selected Skills",
		"Only the skills linked to this Agent are available. Ignore repository or user-level Agent CLI configuration.",
		"",
		"If this CLI exposes a native Skill tool or slash command, invoke the skill by `name` below before doing the work. Otherwise read SKILL.md, then follow it using the skill root for relative `scripts/`, `assets/`, and `references/` paths.")
	for _, item := range selectedSkills {
		lines = append(lines, "",
			"- name: `"+item.name+"` (id: "+item.id+")",
			"  description: "+item.desc,
			"  skill.md: `"+item.path+"`",
			"  root: `"+item.root+"`")
	}
	content := []byte(strings.Join(lines, "\n") + "\n")
	agentSystemPath := filepath.Join(configRoot, "agent-system.md")
	if err := os.WriteFile(agentSystemPath, content, 0o600); err != nil {
		return compiled, err
	}
	if err := replaceRelativeSymlink(filepath.Join(agentRoot, "agent-system.md"), filepath.Join("..", "config", agentID, "agent-system.md")); err != nil {
		return compiled, err
	}
	var skillPaths []string
	for _, item := range selectedSkills {
		dir, err := resetControlDirectory(workspace, "skills", item.id)
		if err != nil {
			return compiled, err
		}
		skillPaths = append(skillPaths, item.root)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), item.body, 0o644); err != nil {
			return compiled, err
		}
		for name, data := range item.skill.Files {
			if filepath.ToSlash(name) == "SKILL.md" {
				continue
			}
			clean := filepath.Clean(name)
			if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
				return compiled, fmt.Errorf("invalid skill file path %q", name)
			}
			target := filepath.Join(dir, clean)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return compiled, err
			}
			if err := os.WriteFile(target, []byte(data), 0o644); err != nil {
				return compiled, err
			}
		}
		// Binary skill files from the skill_files table; the manifest mode
		// keeps executable scripts runnable in the sandbox. SKILL.md is
		// already written above so frontmatter stays intact.
		for _, ref := range item.skill.FileRefs {
			if filepath.ToSlash(ref.Path) == "SKILL.md" {
				continue
			}
			data, ok := assets.SkillFiles[item.skill.ID][ref.Path]
			if !ok {
				continue
			}
			clean := filepath.Clean(ref.Path)
			if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
				return compiled, fmt.Errorf("invalid skill file path %q", ref.Path)
			}
			target := filepath.Join(dir, clean)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return compiled, err
			}
			if err := os.WriteFile(target, data, os.FileMode(domain.NormalizeSkillFileMode(ref.Mode))); err != nil {
				return compiled, err
			}
		}
		if err := replaceRelativeSymlink(filepath.Join(bindingRoot, "skills", item.id), filepath.Join("..", "..", "..", "skills", item.id)); err != nil {
			return compiled, err
		}
	}
	// Repository-local MCP configuration is intentionally not discovered. The
	// Agent definition is the sole allowlist for tools in unattended runs.
	mergedMCP := mergeMCPServers(assets.MCPServers, nil)
	if strings.TrimSpace(options.GateResultPath) != "" {
		gateServer, err := materializeGateMCP(configRoot, filepath.Join(".agentflow", "config", agentID, "gate-mcp"), options.GateResultPath, driver)
		if err != nil {
			return compiled, err
		}
		mergedMCP = append(mergedMCP, gateServer)
	}
	compiled.HasMCP = len(mergedMCP) > 0
	if hint := mcpUsageHint(mergedMCP); hint != "" {
		content = append(content, []byte("\n"+hint)...)
		if err := os.WriteFile(agentSystemPath, content, 0o600); err != nil {
			return compiled, err
		}
	}
	for _, server := range mergedMCP {
		serverID := safeID(server.ID)
		if serverID == "" {
			return compiled, fmt.Errorf("MCP server id is required")
		}
		definition, _ := json.MarshalIndent(server, "", "  ")
		if err := writeManagedFile(filepath.Join(workspace, ".agentflow", "mcp", serverID+".json"), definition, 0o600); err != nil {
			return compiled, err
		}
		if err := replaceRelativeSymlink(filepath.Join(bindingRoot, "mcp", serverID+".json"), filepath.Join("..", "..", "..", "mcp", serverID+".json")); err != nil {
			return compiled, err
		}
	}
	runtimeMCP := prepareMCPServersForDriver(mergedMCP, driver)
	entries, err := mcpConfigEntries(runtimeMCP)
	if err != nil {
		return compiled, err
	}
	mcpRaw, _ := json.MarshalIndent(map[string]any{"mcpServers": entries}, "", "  ")
	if err := os.WriteFile(filepath.Join(configRoot, "mcp.json"), mcpRaw, 0o600); err != nil {
		return compiled, err
	}
	if err := replaceRelativeSymlink(filepath.Join(agentRoot, "mcp.json"), filepath.Join("..", "config", agentID, "mcp.json")); err != nil {
		return compiled, err
	}
	provider := providerForKind(ex.Kind)
	err = provider.Materialize(&providerMaterializeContext{
		Workspace: workspace, AgentID: agentID, AgentRel: agentRel, RuntimeRel: runtimeRel,
		ConfigRoot: configRoot, AgentRoot: agentRoot, Content: content, SkillPaths: skillPaths,
		MCPServers: runtimeMCP, Assets: assets, Compiled: &compiled,
	})
	return compiled, err
}

func ensureDirectoryNoSymlink(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("runtime path is not a real directory: %s", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return os.Mkdir(path, mode)
}

func reservedAgentID(id string) bool {
	switch id {
	case "skills", "rules", "mcp", "hooks", "config", "bindings", "runtime", "inputs", "isolation", "environment", "docker":
		return true
	default:
		return false
	}
}

func resetControlDirectory(workspace string, elements ...string) (string, error) {
	if len(elements) == 0 {
		return "", fmt.Errorf("missing AgentFlow control directory")
	}
	parent, err := EnsureControlDirectory(workspace, elements[:len(elements)-1]...)
	if err != nil {
		return "", err
	}
	name := elements[len(elements)-1]
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", fmt.Errorf("invalid AgentFlow control path segment %q", name)
	}
	target := filepath.Join(parent, name)
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("managed path is not a real directory: %s", target)
		}
		if err := os.RemoveAll(target); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return "", err
	}
	return target, nil
}

func writeManagedFile(path string, content []byte, mode os.FileMode) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("managed path is not a regular file: %s", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, content, mode)
}

func replaceRelativeSymlink(path, target string) error {
	if filepath.IsAbs(target) {
		return fmt.Errorf("AgentFlow link target must be relative: %s", target)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("Agent binding path is not a symlink: %s", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(target, path)
}

// EnsureControlRoot validates the only repository-adjacent directory the
// control plane writes before the sandboxed executor starts.
func EnsureControlRoot(workspace string) error {
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	if err := ensureDirectoryNoSymlink(filepath.Join(workspace, ".agentflow"), 0o700); err != nil {
		return err
	}
	return EnsureAgentFlowGitExcludes(workspace)
}

// EnsureControlDirectory creates a directory below .agentflow while rejecting
// symlinks in every control-plane-owned path component.
func EnsureControlDirectory(workspace string, elements ...string) (string, error) {
	if err := EnsureControlRoot(workspace); err != nil {
		return "", err
	}
	current := filepath.Join(workspace, ".agentflow")
	for _, element := range elements {
		if element == "" || element == "." || element == ".." || filepath.Base(element) != element {
			return "", fmt.Errorf("invalid AgentFlow control path segment %q", element)
		}
		current = filepath.Join(current, element)
		if err := ensureDirectoryNoSymlink(current, 0o700); err != nil {
			return "", err
		}
	}
	return current, nil
}

func prepareRuntimeRoot(workspace, invocationID string) (string, error) {
	return resetControlDirectory(workspace, "runtime", invocationID)
}

func mergeMCPServers(assets, local []domain.MCPServerDefinition) []domain.MCPServerDefinition {
	merged := append([]domain.MCPServerDefinition{}, assets...)
	seen := map[string]bool{}
	for _, m := range merged {
		if m.ID != "" {
			seen[m.ID] = true
		}
	}
	for _, m := range local {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		merged = append(merged, m)
	}
	for _, m := range assets {
		for _, alias := range mcpAliases(m) {
			if seen[alias] {
				continue
			}
			seen[alias] = true
			cp := m
			cp.ID = alias
			merged = append(merged, cp)
		}
	}
	return merged
}

func mcpAliases(m domain.MCPServerDefinition) []string {
	if m.Transport != "http" {
		return nil
	}
	key := strings.ToLower(m.ID + " " + m.URL)
	var out []string
	if m.ID == "zhiyan-pipeline" || strings.Contains(key, "zhiyan") || strings.Contains(key, "/qci") {
		out = append(out, "coding-mcp")
	}
	if m.ID == "gongfeng-delivery" || strings.Contains(key, "gongfeng") {
		out = append(out, "gongfeng")
	}
	return out
}

func mcpConfigEntries(servers []domain.MCPServerDefinition) (map[string]any, error) {
	out := map[string]any{}
	for _, m := range servers {
		if m.ID == "" {
			continue
		}
		entry := map[string]any{}
		if m.Transport == "http" {
			entry["type"] = "http"
			entry["url"] = m.URL
			headers, err := httpMCPHeaders(m)
			if err != nil {
				return nil, err
			}
			if len(headers) > 0 {
				entry["headers"] = headers
			}
		} else {
			entry["command"] = m.Command
			entry["args"] = m.Args
			if cwd := MCPRuntimeWorkingDirectory(m); cwd != "" {
				entry["cwd"] = cwd
			}
			if env := resolveMap(m.Environment); len(env) > 0 {
				entry["env"] = env
			}
		}
		out[m.ID] = entry
	}
	return out, nil
}

// ResolveMCPHeaders expands ${secret:} placeholders in an HTTP MCP's Headers.
func ResolveMCPHeaders(m domain.MCPServerDefinition) (map[string]string, error) {
	return httpMCPHeaders(m)
}

func httpMCPHeaders(m domain.MCPServerDefinition) (map[string]string, error) {
	for k, v := range m.Headers {
		if strings.Contains(v, "${secret:") {
			if _, ok := resolveSecretString(v); !ok {
				return nil, fmt.Errorf("MCP %s header %s: ${secret:} is empty (put the Bearer token in this MCP's Headers JSON)", m.ID, k)
			}
		}
	}
	out := resolveMap(m.Headers)
	if _, ok := headerLookup(out, "X-Username"); !ok {
		if user := strings.TrimSpace(os.Getenv("AGENTFLOW_MCP_USERNAME")); user != "" {
			if out == nil {
				out = map[string]string{}
			}
			out["X-Username"] = user
		}
	}
	return out, nil
}

func headerLookup(headers map[string]string, name string) (string, bool) {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

func mcpUsageHint(servers []domain.MCPServerDefinition) string {
	var ids []string
	hasQCI, hasGongfeng := false, false
	for _, m := range servers {
		if strings.TrimSpace(m.ID) != "" {
			ids = append(ids, m.ID)
		}
		if m.Transport != "http" {
			continue
		}
		key := strings.ToLower(m.ID + " " + m.URL)
		if m.ID == "coding-mcp" || m.ID == "zhiyan-pipeline" || strings.Contains(key, "zhiyan") || strings.Contains(key, "/qci") {
			hasQCI = true
		}
		if m.ID == "gongfeng" || m.ID == "gongfeng-delivery" || strings.Contains(key, "gongfeng") {
			hasGongfeng = true
		}
	}
	if len(ids) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## MCP 怎么用\n\n")
	b.WriteString("本节点的平台 MCP 配置已按 Agent allowlist 严格注入：")
	b.WriteString(strings.Join(ids, "、"))
	b.WriteString("。别名：zhiyan-pipeline=coding-mcp，gongfeng-delivery=gongfeng。\n")
	b.WriteString("这些 MCP 已写入 `--mcp-config .agentflow/config/<agent>/mcp.json`，会以原生工具出现（名称常为 mcp__<server>__*）。直接调原生工具。HTTP MCP 禁止用 curl 打网关，禁止 Bash mcporter：JSON body 容易丢，QueryErrorLog 会变成 taskId=0，报 query old totalresult 0 failed。\n")
	if hasGongfeng {
		b.WriteString("\n### 工蜂：看 MR / Checkers\n")
		b.WriteString("真值是 commit status，不是审查报告是否 SUCCESS。\n")
		b.WriteString("1. 读 mr-body.md，记下 project（group/repo）、iid、head SHA。\n")
		b.WriteString("2. 原生 `get_commit_combined_status`（sha=当前 HEAD）。这是 Checkers 列表。轮询最多 20 轮、间隔 90s，直到应等项终态。\n")
		b.WriteString("3. 失败项再 `search_merge_request_notes` 翻页对齐建议；`get_review_intelligent_result` 只表示审查意见，**不等于** MR审查 pipeline 绿。\n")
		b.WriteString("4. 创建 MR 用 `create_merge_request`；更新已有本轮 MR 才 `update_merge_request`；讨论区回评用 `create_merge_request_note`。\n")
	}
	if hasQCI {
		b.WriteString("\n### 智研 QCI：看流水线失败原因\n")
		b.WriteString("从 check 的 target_url 抽出 pipeline_id 和 build_id。近 30 天构建用原生 **coding_mcp**：\n")
		b.WriteString("`GET /pipeline/{pipeline_id}/totalresult/{build_id}`\n")
		b.WriteString("**禁止** `/pipeline/{id}/oldtotalresult`（冷库，会 0 条，不是「MCP 没数据」）。\n")
		b.WriteString("从 totalresult 取失败 task 的真实 taskId（数字，禁止 0）。拉错误日志只能用原生 **coding_skill**（不要 coding_mcp，那条路径 404）：\n")
		b.WriteString("`POST /QueryErrorLog` body=`{\"pipelineId\":...,\"buildId\":...,\"taskId\":<上一步的id>}` 三个字段都必填。\n")
		b.WriteString("若返回 `query old totalresult 0 failed`：MCP 是通的，是 ID 丢了。用 totalresult 里的数字重试。\n")
		b.WriteString("自动代码审查类检查失败若是 401 / no permission 等鉴权错误且没有代码审查结论，记 SKIP_INFRA，不要当成业务缺陷打回。\n")
	}
	return b.String()
}

var secretPlaceholder = regexp.MustCompile(`\$\{secret:([^}]+)\}`)

func resolveMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		resolved, ok := resolveSecretString(v)
		if !ok {
			continue
		}
		out[k] = resolved
	}
	return out
}

// resolveSecretString expands ${secret:NAME} anywhere in the value using the
// process environment. Headers are often "Bearer ${secret:FOO}", not just
// "${secret:FOO}". Empty or unresolved placeholders are omitted so mcp.json
// never ships the literal ${secret:...} string (that 401s mcpgw).
func resolveSecretString(value string) (string, bool) {
	if !strings.Contains(value, "${secret:") {
		return value, true
	}
	missing := false
	resolved := secretPlaceholder.ReplaceAllStringFunc(value, func(match string) string {
		parts := secretPlaceholder.FindStringSubmatch(match)
		if len(parts) < 2 {
			missing = true
			return ""
		}
		actual := strings.TrimSpace(os.Getenv(parts[1]))
		if actual == "" {
			missing = true
			return ""
		}
		return actual
	})
	if missing || strings.Contains(resolved, "${secret:") {
		return "", false
	}
	return resolved, true
}

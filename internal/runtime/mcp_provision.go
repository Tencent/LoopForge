package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agentflow/internal/domain"
)

// MCPRuntimeWorkingDirectory returns a workspace-relative cwd. Empty means
// the workspace root; uploaded programs default to their managed MCP folder.
func MCPRuntimeWorkingDirectory(server domain.MCPServerDefinition) string {
	if len(server.FileRefs) > 0 {
		root := filepath.Join(".agentflow", "mcp", safeID(server.ID))
		if cwd := strings.TrimSpace(server.WorkingDirectory); cwd != "" {
			root = filepath.Join(root, filepath.Clean(cwd))
		}
		return filepath.ToSlash(root)
	}
	if cwd := strings.TrimSpace(server.WorkingDirectory); cwd != "" {
		return filepath.ToSlash(filepath.Clean(cwd))
	}
	return ""
}

func managedMCPProcessRuntime(server domain.MCPServerDefinition, driver string) bool {
	driver = strings.ToLower(strings.TrimSpace(driver))
	transport := strings.ToLower(strings.TrimSpace(server.Transport))
	return (driver == "seatbelt" || driver == "bwrap") &&
		(transport == "" || transport == "stdio") &&
		(len(server.FileRefs) > 0 || strings.TrimSpace(server.InstallScript) != "")
}

func mcpRuntimeRootRel(server domain.MCPServerDefinition) string {
	return filepath.ToSlash(filepath.Join(".agentflow", "mcp-runtime", safeID(server.ID)))
}

func mcpRuntimeEnvironment(prefix string) map[string]string {
	return map[string]string{
		"PREFIX":               prefix,
		"HOME":                 filepath.Join(prefix, "home"),
		"TMPDIR":               filepath.Join(prefix, "tmp"),
		"XDG_CACHE_HOME":       filepath.Join(prefix, "cache"),
		"XDG_CONFIG_HOME":      filepath.Join(prefix, "config"),
		"XDG_DATA_HOME":        filepath.Join(prefix, "data"),
		"NPM_CONFIG_PREFIX":    prefix,
		"NPM_CONFIG_CACHE":     filepath.Join(prefix, "cache", "npm"),
		"COMPOSER_HOME":        filepath.Join(prefix, "composer"),
		"COMPOSER_CACHE_DIR":   filepath.Join(prefix, "cache", "composer"),
		"COMPOSER_BIN_DIR":     filepath.Join(prefix, "bin"),
		"PYTHONUSERBASE":       prefix,
		"PIP_USER":             "1",
		"PIP_CACHE_DIR":        filepath.Join(prefix, "cache", "pip"),
		"GOBIN":                filepath.Join(prefix, "bin"),
		"GOPATH":               filepath.Join(prefix, "go"),
		"CARGO_HOME":           filepath.Join(prefix, "cargo"),
		"GEM_HOME":             filepath.Join(prefix, "gems"),
		"BUNDLE_PATH":          filepath.Join(prefix, "bundle"),
		"AGENTFLOW_MCP_PREFIX": prefix,
	}
}

// mcpRuntimePathDirs returns the private command search path used by process
// sandboxes. User entries are relative to PREFIX; the remaining entries cover
// the standard layouts of the package managers redirected above.
func mcpRuntimePathDirs(workspace, prefix string, server domain.MCPServerDefinition) []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(path string) {
		path = filepath.Clean(path)
		if path == "" || path == "." || seen[path] {
			return
		}
		seen[path] = true
		dirs = append(dirs, path)
	}
	for _, entry := range server.PathEntries {
		if strings.TrimSpace(entry) == "" || filepath.IsAbs(entry) {
			continue
		}
		add(filepath.Join(prefix, filepath.FromSlash(entry)))
	}
	for _, entry := range []string{
		"bin",
		"sbin",
		filepath.Join(".local", "bin"),
		filepath.Join("home", ".local", "bin"),
		".",
		filepath.Join("node_modules", ".bin"),
		filepath.Join("cargo", "bin"),
		filepath.Join("gems", "bin"),
		filepath.Join("venv", "bin"),
	} {
		if entry == "." {
			add(prefix)
		} else {
			add(filepath.Join(prefix, entry))
		}
	}
	cwd := MCPRuntimeWorkingDirectory(server)
	if cwd == "" {
		cwd = "."
	}
	add(filepath.Join(workspace, filepath.FromSlash(cwd), "node_modules", ".bin"))
	return dirs
}

func mcpRuntimePATH(workspace, prefix string, server domain.MCPServerDefinition) string {
	return strings.Join(mcpRuntimePathDirs(workspace, prefix, server), string(os.PathListSeparator))
}

func prepareMCPServersForDriver(servers []domain.MCPServerDefinition, driver string) []domain.MCPServerDefinition {
	out := append([]domain.MCPServerDefinition(nil), servers...)
	for i := range out {
		if !managedMCPProcessRuntime(out[i], driver) {
			continue
		}
		out[i].Command = filepath.ToSlash(filepath.Join(mcpRuntimeRootRel(out[i]), "launch"))
		out[i].Args = nil
		out[i].WorkingDirectory = ""
		out[i].FileRefs = nil
	}
	return out
}

// ProvisionMCPAssets materializes uploaded programs and executes install
// scripts inside the selected sandbox. The completion marker is sandbox-bound:
// a reused Run container installs once, while a fresh runner task container
// cannot accidentally reuse a host-workspace marker for global installs.
func ProvisionMCPAssets(ctx context.Context, sandbox Sandbox, workspace string, assets domain.AgentAssetBundle, sink EventSink) error {
	for _, server := range assets.MCPServers {
		if !strings.EqualFold(strings.TrimSpace(server.Transport), "stdio") && strings.TrimSpace(server.Transport) != "" {
			continue
		}
		serverID := safeID(server.ID)
		if serverID == "" {
			return fmt.Errorf("MCP server id %q cannot be used as a runtime path", server.ID)
		}
		hash := mcpProvisionHash(server, assets.MCPFiles[server.ID])
		if err := materializeMCPFiles(workspace, server, serverID, assets.MCPFiles[server.ID], hash); err != nil {
			return err
		}
		if managedMCPProcessRuntime(server, sandboxDriver(sandbox)) {
			if err := prepareMCPProcessRuntime(workspace, server); err != nil {
				return err
			}
		}
		script := strings.TrimSpace(server.InstallScript)
		if script == "" {
			continue
		}
		if sandbox == nil || sandboxDriver(sandbox) == "local" {
			return fmt.Errorf("MCP %s 安装脚本要求使用 docker、bwrap 或 seatbelt 隔离沙箱", server.Name)
		}
		cwd := MCPRuntimeWorkingDirectory(server)
		if cwd == "" {
			cwd = "."
		}
		marker := filepath.ToSlash(filepath.Join(".agentflow", "runtime", "mcp-provision", serverID+"-"+hash+"-"+safeID(sandbox.ID())+".done"))
		if sandboxDriver(sandbox) == "docker" {
			marker = "/tmp/agentflow-mcp-provision/" + serverID + "-" + hash + ".done"
		}
		command := "if [ -f " + shellSingleQuote(marker) + " ]; then exit 0; fi\n"
		environment := resolveMap(server.Environment)
		if managedMCPProcessRuntime(server, sandboxDriver(sandbox)) {
			prefix := filepath.Join(workspace, filepath.FromSlash(mcpRuntimeRootRel(server)))
			for key, value := range mcpRuntimeEnvironment(prefix) {
				environment[key] = value
			}
			command += "PATH=" + shellSingleQuote(mcpRuntimePATH(workspace, prefix, server)) + ":\"$PATH\"\nexport PATH\n"
		}
		command +=
			"cd " + shellSingleQuote(cwd) + "\n" +
				"sh -c " + shellSingleQuote(script) + "\n" +
				"mkdir -p " + shellSingleQuote(filepath.ToSlash(filepath.Dir(marker))) + "\n" +
				"touch " + shellSingleQuote(marker)
		spec := WithCommandEnvironment(CommandSpec{Argv: []string{"sh", "-lc", command}, Provider: "mcp-provision"}, environment)
		if _, err := sandbox.Exec(ctx, spec, sink); err != nil {
			return fmt.Errorf("provision MCP %s: %w", server.Name, err)
		}
	}
	return nil
}

func prepareMCPProcessRuntime(workspace string, server domain.MCPServerDefinition) error {
	root, err := EnsureControlDirectory(workspace, "mcp-runtime", safeID(server.ID))
	if err != nil {
		return err
	}
	for _, dir := range []string{"bin", "home", "tmp", "cache", "config", "data"} {
		if _, err := EnsureControlDirectory(workspace, "mcp-runtime", safeID(server.ID), dir); err != nil {
			return err
		}
	}
	cwd := MCPRuntimeWorkingDirectory(server)
	if cwd == "" {
		cwd = "."
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\nset -e\n")
	b.WriteString("PREFIX=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" && pwd)\n")
	b.WriteString("WORKSPACE=$(CDPATH= cd -- \"$PREFIX/../../..\" && pwd)\n")
	b.WriteString("export PREFIX AGENTFLOW_MCP_PREFIX=\"$PREFIX\"\n")
	b.WriteString("export HOME=\"$PREFIX/home\" TMPDIR=\"$PREFIX/tmp\"\n")
	b.WriteString("export XDG_CACHE_HOME=\"$PREFIX/cache\" XDG_CONFIG_HOME=\"$PREFIX/config\" XDG_DATA_HOME=\"$PREFIX/data\"\n")
	b.WriteString("export NPM_CONFIG_PREFIX=\"$PREFIX\" NPM_CONFIG_CACHE=\"$PREFIX/cache/npm\"\n")
	b.WriteString("export COMPOSER_HOME=\"$PREFIX/composer\" COMPOSER_CACHE_DIR=\"$PREFIX/cache/composer\" COMPOSER_BIN_DIR=\"$PREFIX/bin\"\n")
	b.WriteString("export PYTHONUSERBASE=\"$PREFIX\" PIP_USER=1 PIP_CACHE_DIR=\"$PREFIX/cache/pip\"\n")
	b.WriteString("export GOBIN=\"$PREFIX/bin\" GOPATH=\"$PREFIX/go\" CARGO_HOME=\"$PREFIX/cargo\" GEM_HOME=\"$PREFIX/gems\" BUNDLE_PATH=\"$PREFIX/bundle\"\n")
	b.WriteString("PATH=" + shellSingleQuote(mcpRuntimePATH(workspace, root, server)) + ":\"$PATH\"\nexport PATH\n")
	b.WriteString("cd \"$WORKSPACE\"/" + shellSingleQuote(filepath.ToSlash(cwd)) + "\n")
	b.WriteString("exec " + shellSingleQuote(server.Command))
	for _, arg := range server.Args {
		b.WriteString(" " + shellSingleQuote(arg))
	}
	b.WriteString("\n")
	return writeManagedFile(workspace, filepath.Join(root, "launch"), []byte(b.String()), 0o700)
}

func materializeMCPFiles(workspace string, server domain.MCPServerDefinition, serverID string, files map[string][]byte, hash string) error {
	if len(server.FileRefs) == 0 {
		return nil
	}
	if serverID == "" || safeID(serverID) != serverID {
		return fmt.Errorf("invalid MCP runtime id %q", serverID)
	}
	for _, ref := range server.FileRefs {
		if err := domain.ValidateRelativeFilePath(ref.Path); err != nil {
			return err
		}
		if _, ok := files[ref.Path]; !ok {
			return fmt.Errorf("MCP %s missing uploaded file %s", server.ID, ref.Path)
		}
	}
	target := filepath.Join(workspace, ".agentflow", "mcp", serverID)
	base, root, err := openWorkspacePath(workspace, target)
	if err != nil {
		return err
	}
	defer base.Close()
	state := filepath.Join(".agentflow", "runtime", "mcp-sources", serverID+".sha256")
	if err := rejectWorkspaceSymlinks(base, state); err != nil {
		return err
	}
	raw, err := base.ReadFile(state)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && strings.TrimSpace(string(raw)) == hash {
		return nil
	}
	if err := base.RemoveAll(root); err != nil {
		return err
	}
	if err := base.MkdirAll(root, 0o700); err != nil {
		return err
	}
	for _, ref := range server.FileRefs {
		name := filepath.Join(root, filepath.FromSlash(ref.Path))
		if err := base.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return err
		}
		if err := writeManagedFile(workspace, filepath.Join(workspace, name), files[ref.Path], os.FileMode(domain.NormalizeSkillFileMode(ref.Mode))); err != nil {
			return err
		}
	}
	if err := base.MkdirAll(filepath.Dir(state), 0o700); err != nil {
		return err
	}
	return writeManagedFile(workspace, filepath.Join(workspace, state), []byte(hash+"\n"), 0o600)
}

func mcpProvisionHash(server domain.MCPServerDefinition, files map[string][]byte) string {
	h := sha256.New()
	h.Write([]byte("mcp-process-runtime-v1"))
	h.Write([]byte(server.InstallScript))
	h.Write([]byte{0})
	h.Write([]byte(server.WorkingDirectory))
	for _, entry := range server.PathEntries {
		h.Write([]byte{0})
		h.Write([]byte(entry))
	}
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	for _, name := range paths {
		h.Write([]byte{0})
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(files[name])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

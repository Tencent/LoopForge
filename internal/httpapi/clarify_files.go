package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"
)

// The API model receives code through the existing authenticated MCP gateway;
// repository paths and credentials stay on the platform, with no write tool.
func clarifyFileTools(session *AgentToolSession) []agentToolDescriptor {
	if session.Mode != AgentToolClarify || session.ClarifyWorkspace == "" {
		return nil
	}
	var out []agentToolDescriptor
	for _, tool := range []struct{ name, description string }{
		{"clarify_list_files", "列出本次澄清仓库中的文件，相对路径；path 可指定子目录，最多返回 200 项。"},
		{"clarify_read_file", "只读本次澄清仓库中的 UTF-8 文件，path 为相对路径；单文件最多 512 KiB。"},
	} {
		schema, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"path": map[string]any{"type": "string"}}})
		out = append(out, agentToolDescriptor{Name: tool.name, Description: tool.description, InputSchema: schema, Kind: "clarify-files", Annotations: json.RawMessage(`{"readOnlyHint":true}`)})
	}
	return out
}

func callClarifyFiles(session *AgentToolSession, name string, args map[string]any) (string, error) {
	if session.Mode != AgentToolClarify || session.ClarifyWorkspace == "" {
		return "", errAgentToolForbidden
	}
	root, err := os.OpenRoot(session.ClarifyWorkspace)
	if err != nil {
		return "", err
	}
	defer root.Close()
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	switch name {
	case "clarify_list_files":
		var paths []string
		err := fs.WalkDir(root.FS(), path, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == ".agentflow") {
				return fs.SkipDir
			}
			if entry.Type().IsRegular() {
				paths = append(paths, path)
			}
			if len(paths) >= 200 {
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		raw, _ := json.Marshal(map[string]any{"files": paths, "limit": 200})
		return string(raw), nil
	case "clarify_read_file":
		file, err := root.Open(path)
		if err != nil {
			return "", err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Size() > 512<<10 {
			return "", fmt.Errorf("只支持不超过 512 KiB 的普通文本文件")
		}
		data, err := io.ReadAll(io.LimitReader(file, (512<<10)+1))
		if err != nil {
			return "", err
		}
		if len(data) > 512<<10 || !utf8.Valid(data) || strings.ContainsRune(string(data), '\x00') {
			return "", fmt.Errorf("文件过大或不是 UTF-8 文本")
		}
		return string(data), nil
	}
	return "", errAgentToolUnknown
}

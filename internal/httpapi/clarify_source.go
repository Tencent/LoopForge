package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

var clarifySourceTools = []agentToolDescriptor{
	{Name: "clarify_list_files", Kind: "clarify-source", Description: "List available repositories, or list one directory in a repository. Use offset to page through long directories.", InputSchema: json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string"},"path":{"type":"string"},"offset":{"type":"integer","minimum":0}}}`)},
	{Name: "clarify_read_file", Kind: "clarify-source", Description: "Read a text file from this turn's analysis checkout. Use the returned nextOffset (byte offset) to continue.", InputSchema: json.RawMessage(`{"type":"object","required":["repository","path"],"properties":{"repository":{"type":"string"},"path":{"type":"string"},"offset":{"type":"integer","minimum":0}}}`)},
}

func readClarifySource(session *AgentToolSession, tool string, args map[string]any) (string, error) {
	if session.Mode != AgentToolClarify {
		return "", errAgentToolForbidden
	}
	repository, _ := args["repository"].(string)
	session.mu.Lock()
	repos := session.clarifyRepos
	session.mu.Unlock()
	if repository == "" && tool == "clarify_list_files" {
		body, err := json.Marshal(slices.Sorted(maps.Keys(repos)))
		return string(body), err
	}
	dir, ok := repos[repository]
	if !ok {
		return "", fmt.Errorf("repository is not in the analysis scope")
	}
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("analysis repository unavailable")
	}
	defer root.Close()
	list := tool == "clarify_list_files"
	if !clarifySourcePathAllowed(root, path, list) {
		return "", fmt.Errorf("path is not available in the analysis checkout")
	}
	file, err := root.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot open analysis path")
	}
	defer file.Close()
	offset := int64(0)
	if value, ok := args["offset"].(json.Number); ok {
		var parseErr error
		offset, parseErr = value.Int64()
		if parseErr != nil || offset < 0 || offset > 4<<20 {
			return "", fmt.Errorf("invalid offset")
		}
	}
	if list {
		return listClarifySource(file, int(offset))
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", fmt.Errorf("cannot seek analysis file")
	}
	const limit = 16 << 10
	body, err := io.ReadAll(io.LimitReader(file, limit+utf8.UTFMax))
	if err != nil || bytes.IndexByte(body, 0) >= 0 {
		return "", fmt.Errorf("analysis file must be readable text")
	}
	result := map[string]any{"text": string(body)}
	if len(body) > limit {
		end := limit
		for end < len(body) && !utf8.RuneStart(body[end]) {
			end++
		}
		result["text"] = string(body[:end])
		result["nextOffset"] = offset + int64(end)
	}
	data, err := json.Marshal(result)
	return string(data), err
}

func clarifySourcePathAllowed(root *os.Root, path string, directory bool) bool {
	if !fs.ValidPath(path) || workspacePathSensitive(path) {
		return false
	}
	component := ""
	parts := strings.Split(path, "/")
	for i, part := range parts {
		component = filepath.Join(component, part)
		info, err := root.Lstat(component)
		if err != nil {
			return false
		}
		if directory || i < len(parts)-1 {
			if !info.IsDir() {
				return false
			}
		} else if !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func listClarifySource(dir *os.File, offset int) (string, error) {
	// Bound directory scanning independently of the result page size.
	const scanLimit = 4096
	if offset >= scanLimit {
		return "", fmt.Errorf("directory listing limit reached")
	}
	entries, err := dir.ReadDir(scanLimit)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("cannot list analysis directory")
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if workspaceTreeSkipped(entry.Name(), entry) || (!entry.IsDir() && !entry.Type().IsRegular()) {
			continue
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	slices.Sort(names)
	offset = min(offset, len(names))
	end := min(offset+200, len(names))
	page := map[string]any{"entries": names[offset:end]}
	if end < len(names) {
		page["nextOffset"] = end
	}
	if len(entries) == scanLimit {
		page["truncated"] = true
	}
	data, err := json.Marshal(page)
	return string(data), err
}

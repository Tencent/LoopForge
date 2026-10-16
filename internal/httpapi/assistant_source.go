package httpapi

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type assistantSource struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func assistantSourceRoot() string {
	root := strings.TrimSpace(os.Getenv("AGENTFLOW_ASSISTANT_SOURCE_DIR"))
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return root
}

func assistantSourceAllowed(path string) bool {
	path = filepath.ToSlash(path)
	if !filepath.IsLocal(path) {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, ".") || part == "vendor" || part == "node_modules" {
			return false
		}
	}
	switch filepath.Ext(path) {
	case ".go", ".js", ".css", ".html", ".md", ".sql", ".yaml", ".yml":
		return true
	}
	return path == "go.mod"
}

func assistantSourceFiles(root string) ([]string, error) {
	if root == "" {
		return nil, errors.New("source snapshot not configured")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, errors.New("source snapshot unavailable")
	}
	files := []string{}
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(abs, path)
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if assistantSourceAllowed(rel) {
			files = append(files, filepath.ToSlash(rel))
		}
		if len(files) > 10000 {
			return errors.New("source snapshot too large")
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

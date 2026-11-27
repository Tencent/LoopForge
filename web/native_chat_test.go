package web

import (
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
)

// The workbench shares the host document and keeps an accessible label.
func TestNativeChatMountIsInMainDocument(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	tag := regexp.MustCompile(`<div[^>]*id="chatRoot"[^>]*>`).Find(html)
	if tag == nil || !strings.Contains(string(tag), `aria-label="LoopForge 工作台"`) {
		t.Fatal("missing labelled workbench mount")
	}
	if strings.Contains(string(html), `id="chatNextFrame"`) {
		t.Fatal("home must not embed the chat in an iframe")
	}
}

// Mermaid loads additional chunks on demand. Go's default directory embed
// excludes underscore-prefixed chunks, so verify every emitted module import.
func TestNativeChatLazyImportsAreEmbedded(t *testing.T) {
	imports := regexp.MustCompile(`(?:from\s*|import\s*\(?\s*)["'](\./[^"']+\.js)["']`)
	err := fs.WalkDir(Files, "native-chat/assets", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".js") {
			return nil
		}
		source, err := fs.ReadFile(Files, name)
		if err != nil {
			return err
		}
		for _, match := range imports.FindAllSubmatch(source, -1) {
			dependency := path.Join(path.Dir(name), string(match[1]))
			if _, err := fs.Stat(Files, dependency); err != nil {
				t.Errorf("%s imports missing module %s: %v", name, dependency, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

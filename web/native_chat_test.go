package web

import (
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
)

// Browsers fall back to the host iframe's title as a tooltip for any element
// inside the frame without its own title, so the chat frame must not set one.
func TestNativeChatFrameHasNoTitleTooltip(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	tag := regexp.MustCompile(`<iframe[^>]*id="chatNextFrame"[^>]*>`).Find(html)
	if tag == nil {
		t.Fatal("index.html is missing the chatNextFrame iframe")
	}
	if regexp.MustCompile(`\stitle=`).Match(tag) {
		t.Errorf("chatNextFrame must not have a title attribute: %s", tag)
	}
	if !strings.Contains(string(tag), `aria-label="LoopForge 工作台"`) {
		t.Errorf("chatNextFrame should keep an aria-label for accessibility: %s", tag)
	}
	bundle, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bundle), "frame.title=") {
		t.Error("app bundle must not assign frame.title on the chat frame")
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

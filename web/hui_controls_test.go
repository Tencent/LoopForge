package web

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestHuiControlBehaviour(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is required in CI")
		}
		t.Skip("node not installed")
	}
	for _, script := range []string{"hui_select_auto_test.js", "hui_date_test.js", "hui_upload_test.js", "hui_tooltip_test.js", "hui_dialog_test.js"} {
		t.Run(strings.TrimSuffix(script, "_test.js"), func(t *testing.T) {
			out, err := exec.Command(node, script).CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v\n%s", script, err, out)
			}
			// A promise that never settles lets node exit 0 before the last line.
			if want := strings.ReplaceAll(strings.TrimSuffix(script, "_test.js"), "_", " ") + " ok"; !strings.Contains(string(out), want) {
				t.Fatalf("%s did not finish (missing %q):\n%s", script, want, out)
			}
		})
	}
}

func TestHuiControlsStylesheet(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`<link rel="stylesheet" href="([^"?]+)`).FindAllStringSubmatch(string(html), -1)
	if len(links) == 0 || links[0][1] != "/app/hui-controls.css" {
		t.Fatalf("/app/hui-controls.css must load before the page stylesheets so views can override it, got %v", links)
	}
	css, err := os.ReadFile("app/hui-controls.css")
	if err != nil {
		t.Fatal(err)
	}
	text := string(css)
	for _, want := range []string{
		`html[data-theme="dark"]`, "var(--hui-z-popover)",
		".hui-select-panel", `input[type="checkbox"]`, `[role="switch"]`, "#confirmModal",
		".hui-date-panel", ".hui-upload", ".hui-tooltip", "[data-native]",
		"@media (prefers-reduced-motion: reduce)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("hui-controls.css is missing %q", want)
		}
	}
}

// Browser confirm/prompt/alert block the page and ignore the Harness theme;
// use afConfirm / afPrompt / afAlert (web) or an in-app dialog (chatbot).
func TestNoNativeDialogs(t *testing.T) {
	call := regexp.MustCompile(`(^|[^.\w$])(window\.)?(confirm|prompt|alert)\(`)
	var files []string
	for _, pattern := range []string{"*.js", "app/*.js"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	err := filepath.WalkDir("../chatbot/src", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ext := filepath.Ext(path); !d.IsDir() && (ext == ".js" || ext == ".jsx" || ext == ".mjs") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.js") || strings.HasSuffix(name, ".test.mjs") || filepath.Base(name) == "hui_fake_dom.js" {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if call.MatchString(line) {
				t.Errorf("%s:%d calls a native dialog: %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

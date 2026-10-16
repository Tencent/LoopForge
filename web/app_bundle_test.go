package web

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// appScriptRef matches the SPA's classic scripts in index.html. They share one
// global scope and must load in document order.
var appScriptRef = regexp.MustCompile(`<script src="/(app\.js|app/[^"?]+\.js)`)

func appBundleNames() ([]string, error) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range appScriptRef.FindAllStringSubmatch(string(html), -1) {
		names = append(names, m[1])
	}
	return names, nil
}

func appBundleFiles(t testing.TB) []string {
	t.Helper()
	names, err := appBundleNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 2 || names[0] != "app.js" {
		t.Fatalf("index.html should load app.js first, got %v", names)
	}
	return names
}

// readAppBundle returns the app scripts concatenated in load order, so tests
// can search or slice the SPA source as one file.
func readAppBundle() ([]byte, error) {
	names, err := appBundleNames()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		buf.Write(data)
	}
	return buf.Bytes(), nil
}

func appBundlePath(t testing.TB) string {
	t.Helper()
	data, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "app.bundle.js")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAppBundleFilesAreEmbeddedAndListed(t *testing.T) {
	listed := map[string]bool{}
	for _, name := range appBundleFiles(t) {
		listed[name] = true
		if _, err := Files.Open(name); err != nil {
			t.Fatalf("%s is loaded by index.html but not embedded: %v", name, err)
		}
	}
	files, err := filepath.Glob("app/*.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if !listed[filepath.ToSlash(name)] {
			t.Fatalf("%s exists but index.html never loads it", name)
		}
	}
	if names := appBundleFiles(t); names[len(names)-1] != "app/start.js" {
		t.Fatalf("app/start.js calls boot() and must load last, got %v", names)
	}
}

package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsolatedDockerfileMatchesPublicSeed(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test file path unavailable")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "migrations", "seed", "001_public_catalog.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "INSERT IGNORE INTO dockerfiles")
	if start < 0 {
		t.Fatal("generic dockerfile seed not found")
	}
	rest := text[start:]
	open := strings.Index(rest, `'{"id":"generic-backend"`)
	if open < 0 {
		t.Fatal("generic-backend payload not found")
	}
	closeAt := strings.Index(rest[open+1:], `')`)
	if closeAt < 0 {
		t.Fatal("generic-backend payload is not terminated")
	}
	var payload struct {
		Content string `json:"content"`
	}
	// The seed is a MySQL string: \\n is stored as the JSON newline escape.
	stored := strings.ReplaceAll(rest[open+1:open+1+closeAt], `\\`, `\`)
	if err := json.Unmarshal([]byte(stored), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Content != IsolatedDockerfile {
		t.Fatalf("seed dockerfile differs from sandbox.Dockerfile:\nseed:\n%s\nembedded:\n%s", payload.Content, IsolatedDockerfile)
	}
}

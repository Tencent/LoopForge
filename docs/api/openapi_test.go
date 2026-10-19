package apidocs

import (
	"bufio"
	"bytes"
	"regexp"
	"strings"
	"testing"

	"agentflow/internal/application"
)

var pathParameterPattern = regexp.MustCompile(`\{[^}]+\}`)

func TestOpenAPICoversEveryPublicOperation(t *testing.T) {
	covered := map[string]bool{}
	operationIDs := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(OpenAPI))
	currentPath := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":") {
			currentPath = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		trimmed := strings.TrimSpace(line)
		if currentPath != "" && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "      ") {
			method := strings.TrimSuffix(trimmed, ":")
			switch method {
			case "get", "post", "put", "delete":
				covered[strings.ToUpper(method)+" "+normalizePath(currentPath)] = true
			}
		}
		if strings.HasPrefix(trimmed, "operationId:") {
			id := strings.TrimSpace(strings.TrimPrefix(trimmed, "operationId:"))
			if id == "" || operationIDs[id] {
				t.Fatalf("missing or duplicate operationId %q", id)
			}
			operationIDs[id] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for _, op := range application.PublicOperations() {
		key := op.Method + " " + normalizePath(op.Path)
		if !covered[key] {
			t.Errorf("OpenAPI does not cover %s (%s)", op.Name, key)
		}
	}
	if len(operationIDs) < len(application.PublicOperations()) {
		t.Fatalf("OpenAPI operations = %d, public manifest = %d", len(operationIDs), len(application.PublicOperations()))
	}
}

func TestOpenAPIExcludesInternalControlPlane(t *testing.T) {
	text := string(OpenAPI)
	for _, forbidden := range []string{"/signals:", "/api/otel/", "/leases", "sandbox-reclaim"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("OpenAPI contains internal control-plane marker %q", forbidden)
		}
	}
}

func normalizePath(path string) string {
	return pathParameterPattern.ReplaceAllString(path, "{}")
}

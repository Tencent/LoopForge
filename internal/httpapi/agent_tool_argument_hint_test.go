package httpapi

import (
	"strings"
	"testing"

	"agentflow/internal/application"
)

// Issue #1084: a read-only tool description said "Query: projectId" while the
// schema only accepts arguments inside a query object, so models passed a
// top-level projectId and hit "unknown field" on the first call.
func TestPlatformToolArgumentHintNamesTheWrapper(t *testing.T) {
	ops := map[string]application.Operation{}
	for _, op := range application.PublicOperations() {
		ops[op.Name] = op
	}

	list, ok := ops["workflows_list"]
	if !ok {
		t.Fatal("workflows_list missing from public operations")
	}
	hint := platformToolArgumentHint(list)
	if !strings.Contains(hint, "query") {
		t.Fatalf("GET tool hint does not name query: %q", hint)
	}
	if !strings.Contains(hint, `"query": {...}`) {
		t.Fatalf("GET tool hint does not show the wrapped shape: %q", hint)
	}
	if strings.Contains(hint, "body") {
		t.Fatalf("GET tool hint offers a body wrapper it does not accept: %q", hint)
	}

	save, ok := ops["workflows_save"]
	if !ok {
		t.Fatal("workflows_save missing from public operations")
	}
	saveHint := platformToolArgumentHint(save)
	if !strings.Contains(saveHint, "body") {
		t.Fatalf("PUT tool hint does not name body: %q", saveHint)
	}

	// Routing tools carry their id in the path, so the hint must name it too.
	get, ok := ops["workflows_get"]
	if !ok {
		t.Fatal("workflows_get missing from public operations")
	}
	if getHint := platformToolArgumentHint(get); !strings.Contains(getHint, "path") {
		t.Fatalf("path-parameterised tool hint does not name path: %q", getHint)
	}
}

// A tool whose schema has no wrappers must not grow a hint that names them.
func TestPlatformToolArgumentHintIsEmptyWithoutWrappers(t *testing.T) {
	op := application.Operation{Name: "noargs", Method: "GET", Path: "/api/noargs", ReadOnly: true}
	if hint := platformToolArgumentHint(op); hint != "" {
		t.Fatalf("hint for a tool with no wrapper arguments: %q", hint)
	}
}

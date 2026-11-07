package httpapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

type exportedRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// TestRouteInventoryExport writes every registered gin route to
// $AGENTFLOW_ROUTES_OUT; e2e/coverage/route-coverage.mjs uses it as the
// denominator of the e2e route coverage.
func TestRouteInventoryExport(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	handler := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "user", "pass")

	seen := map[exportedRoute]bool{}
	var routes []exportedRoute
	for _, r := range handler.Routes() {
		route := exportedRoute{Method: r.Method, Path: r.Path}
		if !seen[route] {
			seen[route] = true
			routes = append(routes, route)
		}
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	if len(routes) < 100 {
		t.Fatalf("only %d routes registered", len(routes))
	}
	out := os.Getenv("AGENTFLOW_ROUTES_OUT")
	if out == "" {
		return
	}
	data, err := json.MarshalIndent(routes, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

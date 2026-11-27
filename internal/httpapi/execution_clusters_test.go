package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestExecutionClustersKeepCredentialsAndPlacementSeparate(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.ConfigureOpenSandboxEncryption("test shared platform secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.GrantPlatformAdmin(f.user.LoginName); err != nil {
		t.Fatal(err)
	}
	member := domain.User{ID: 97, LoginName: "runner-user"}
	if err := f.s.PutUser(member); err != nil {
		t.Fatal(err)
	}
	project := domain.Project{ID: "cluster-project", Name: "Cluster project", OwnerID: f.user.ID}
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	provider := func(key string, active int) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/sandboxes" || r.Header.Get("OPEN-SANDBOX-API-KEY") != key {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"items":[],"pagination":{"totalItems":%d}}`, active)
		}))
		t.Cleanup(server.Close)
		return server
	}
	a := provider("first-fixture-key", 5)
	b := provider("second-fixture-key", 1)

	const path = "/api/execution-clusters"
	call := func(user domain.User, method, suffix, origin string, body any, want int) []byte {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		requestPath := path + suffix
		if strings.HasPrefix(suffix, "/api/") {
			requestPath = suffix
		}
		req := httptest.NewRequest(method, "http://localhost"+requestPath, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req = authedRequest(t, req, user)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		if rec.Code != want || strings.Contains(rec.Body.String(), "fixture-key") {
			t.Fatalf("%s %s got %d, want %d, or exposed a credential", method, suffix, rec.Code, want)
		}
		return rec.Body.Bytes()
	}
	const origin = "http://localhost"
	first := map[string]any{"name": "Cluster A", "url": a.URL + "/v1/", "apiKey": "first-fixture-key"}
	second := map[string]any{"name": "Cluster B", "url": b.URL, "apiKey": "second-fixture-key", "projectIds": []string{project.ID}}
	call(member, http.MethodPost, "", origin, first, http.StatusForbidden)
	call(f.user, http.MethodPost, "", "https://other.test", first, http.StatusForbidden)
	var clusterA, clusterB domain.ExecutionCluster
	if err := json.Unmarshal(call(f.user, http.MethodPost, "", origin, first, 200), &clusterA); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(call(f.user, http.MethodPost, "", origin, second, 200), &clusterB); err != nil {
		t.Fatal(err)
	}
	if clusterA.ID == clusterB.ID || clusterA.URL != a.URL || !clusterA.HasAPIKey || !clusterB.HasAPIKey {
		t.Fatal("expected distinct clusters with normalized addresses and masked credentials")
	}
	call(f.user, http.MethodPost, "/test", origin, map[string]string{"id": clusterA.ID, "url": a.URL}, 200)
	call(f.user, http.MethodPost, "/test", origin, map[string]string{"id": clusterB.ID, "url": a.URL}, 502)

	choice := map[string]string{"clusterId": clusterB.ID}
	call(member, http.MethodPut, "/default?projectId="+project.ID, origin, choice, 403)
	call(f.user, http.MethodPut, "/default?projectId="+project.ID, origin, choice, 200)
	options := call(member, http.MethodGet, "?manage=1", origin, nil, 200)
	if bytes.Contains(options, []byte(clusterB.Name)) || bytes.Contains(options, []byte(a.URL)) {
		t.Fatal("unprivileged callers should see only names and IDs of available clusters")
	}
	options = call(f.user, http.MethodGet, "?projectId="+project.ID, origin, nil, 200)
	if !bytes.Contains(options, []byte(`"defaultClusterId":"`+clusterB.ID+`"`)) {
		t.Fatal("expected the project's selected default")
	}
	policyPath := "/api/projects/" + project.ID + "/execution"
	auto := domain.ExecutionTarget{Pool: domain.ExecutionTargetClusterAuto}
	call(member, http.MethodPut, policyPath, origin, auto, 403)
	call(f.user, http.MethodPut, policyPath, origin, auto, 200)
	options = call(f.user, http.MethodGet, "?projectId="+project.ID, origin, nil, 200)
	if !bytes.Contains(options, []byte(`"projectTarget":{"pool":"cluster-auto"}`)) || !bytes.Contains(options, []byte(`"defaultClusterId":""`)) {
		t.Fatal("automatic scheduling did not replace the fixed cluster")
	}
	if assigned, err := f.e.PickExecutionCluster(t.Context(), project.ID); err != nil || assigned.ID != clusterB.ID {
		t.Fatalf("expected least-loaded cluster %s, got %s: %v", clusterB.ID, assigned.ID, err)
	}
	if assigned, err := f.e.PickExecutionCluster(t.Context(), ""); err != nil || assigned.ID != clusterA.ID {
		t.Fatalf("a personal workspace cannot use the project-only cluster: %+v %v", assigned, err)
	}
	b.Close()
	if assigned, err := f.e.PickExecutionCluster(t.Context(), project.ID); err != nil || assigned.ID != clusterA.ID {
		t.Fatalf("expected working cluster when the other is unreachable: %+v %v", assigned, err)
	}
	call(f.user, http.MethodPut, policyPath, origin, domain.ExecutionTarget{Pool: domain.ExecutionTargetMachineAuto}, 200)
	options = call(f.user, http.MethodGet, "?projectId="+project.ID, origin, nil, 200)
	if !bytes.Contains(options, []byte(`"projectTarget":{"pool":"machine-auto"}`)) {
		t.Fatal("machine scheduling did not replace cluster scheduling")
	}
	first["enabled"] = false
	delete(first, "apiKey")
	call(f.user, http.MethodPut, "/"+clusterA.ID, origin, first, 200)
	if _, err := f.s.AllowedExecutionCluster(t.Context(), clusterA.ID, "", true); err == nil {
		t.Fatal("a disabled cluster must not be offered for new work")
	}
	config, err := f.s.OpenSandboxClusterConnection(t.Context(), clusterA.ID, "")
	if err != nil || config.APIKey != "first-fixture-key" {
		t.Fatal("an existing workspace must retain the connection and its key")
	}
}

package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// TODO(knowledge-legacy-compat): 覆盖历史平台级知识库与沉淀审核队列兼容路由；
// 待旧路由整体下线后随 internal/httpapi/knowledge_legacy.go 一并移除。
func TestLegacyKnowledgeCompatRoutes(t *testing.T) {
	st, handler, _, _ := newKnowledgeTestServer(t)
	if err := st.PutUser(domain.User{LoginName: "alice", ChineseName: "Alice"}); err != nil {
		t.Fatal(err)
	}
	alice, _ := st.UserByLoginName("alice")

	// Unconfigured settings & sources (503 with 知识库 reason).
	rec := doJSONAsUser(t, handler, alice, http.MethodGet, "/api/knowledge/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/knowledge/settings = %d %s", rec.Code, rec.Body.String())
	}
	var settings struct {
		Enabled bool `json:"enabled"`
		Ready   bool `json:"ready"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Enabled || settings.Ready {
		t.Fatalf("expected unconfigured legacy settings, got %+v", settings)
	}

	rec = doJSONAsUser(t, handler, alice, http.MethodGet, "/api/knowledge/sources", "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "知识库") {
		t.Fatalf("GET /api/knowledge/sources = %d %s", rec.Code, rec.Body.String())
	}

	// Validation edges for check, search, document, candidates.
	rec = doJSONAsUser(t, handler, alice, http.MethodPost, "/api/knowledge/check", `{}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("POST /api/knowledge/check = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSONAsUser(t, handler, alice, http.MethodPost, "/api/knowledge/search", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/knowledge/search empty = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSONAsUser(t, handler, alice, http.MethodGet, "/api/knowledge/document", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("GET /api/knowledge/document empty = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSONAsUser(t, handler, alice, http.MethodGet, "/api/knowledge/candidates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/knowledge/candidates = %d %s", rec.Code, rec.Body.String())
	}

	// Submissions review-queue CRUD + publish (503 when unconfigured) + reject + delete.
	rec = doJSONAsUser(t, handler, alice, http.MethodPost, "/api/knowledge/submissions", `{"title":"compat probe","content":"hello"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/knowledge/submissions = %d %s", rec.Code, rec.Body.String())
	}
	var created domain.KnowledgeSubmission
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.CreatedBy != "alice" {
		t.Fatalf("unexpected submission: %+v", created)
	}

	rec = doJSONAsUser(t, handler, alice, http.MethodGet, "/api/knowledge/submissions", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
		t.Fatalf("GET /api/knowledge/submissions = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSONAsUser(t, handler, alice, http.MethodPut, "/api/knowledge/submissions/"+created.ID, `{"title":"edited","content":"updated"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"title":"edited"`) {
		t.Fatalf("PUT /api/knowledge/submissions/:id = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSONAsUser(t, handler, alice, http.MethodPost, "/api/knowledge/submissions/"+created.ID+"/publish", `{}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/knowledge/submissions/:id/publish = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSONAsUser(t, handler, alice, http.MethodPost, "/api/knowledge/submissions/"+created.ID+"/reject", `{"reviewNote":"nope"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"rejected"`) {
		t.Fatalf("POST /api/knowledge/submissions/:id/reject = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSONAsUser(t, handler, alice, http.MethodDelete, "/api/knowledge/submissions/"+created.ID, "")
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/knowledge/submissions/:id = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSONAsUser(t, handler, alice, http.MethodGet, "/api/knowledge/submissions/"+created.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET deleted submission = %d %s", rec.Code, rec.Body.String())
	}
}

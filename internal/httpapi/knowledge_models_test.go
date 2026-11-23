package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestCreateKnowledgeModelContractAndSecurity(t *testing.T) {
	var upstreamPayloads []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, managementSentinel) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"success":false,"error":"invalid payload"}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/models" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			upstreamPayloads = append(upstreamPayloads, body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"emb-model-1","name":"text-embedding-3-small","type":"Embedding","source":"remote"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":1003,"message":"not found"}}`))
	}))
	t.Cleanup(upstream.Close)

	denyUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"error":"forbidden"}`))
	}))
	t.Cleanup(denyUpstream.Close)

	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.PutKnowledgeSettings(domain.KnowledgeSettings{
		Enabled:      true,
		WriteEnabled: true,
		Provider:     domain.KnowledgeProviderWeKnora,
		Connections: []domain.KnowledgeConnection{
			{ID: "wk-1", Name: "内置 WeKnora", Provider: domain.KnowledgeProviderWeKnora, URL: upstream.URL, Token: "wk-token-1", Enabled: true},
			{ID: "wk-deny", Name: "受限连接", Provider: domain.KnowledgeProviderWeKnora, URL: denyUpstream.URL, Token: "wk-token-2", Enabled: true},
		},
	}); err != nil {
		t.Fatal(err)
	}

	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")

	// 1. Valid camelCase request through real Gin decode forwards baseUrl, apiKey, embeddingDimensions to upstream
	// and never leaks apiKey in response or activity audit records.
	secretKey := "sk-super-secret-embedding-key-999"
	reqBody := `{"connectionId":"wk-1","name":"text-embedding-3-small","type":"Embedding","source":"remote","baseUrl":"https://api.openai.com/v1","apiKey":"` + secretKey + `","embeddingDimensions":1536}`
	rec := doJSON(t, h, http.MethodPost, "/api/knowledge/models?projectId=p1", reqBody, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/knowledge/models status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secretKey) {
		t.Fatalf("response leaked apiKey: %s", rec.Body.String())
	}
	if len(upstreamPayloads) != 1 {
		t.Fatalf("expected 1 upstream POST /api/v1/models call, got %d", len(upstreamPayloads))
	}
	params, _ := upstreamPayloads[0]["parameters"].(map[string]any)
	if params["base_url"] != "https://api.openai.com/v1" || params["api_key"] != secretKey || params["embedding_dimensions"] != float64(1536) {
		t.Fatalf("upstream parameters mismatch: %+v", params)
	}

	// Verify activity log recorded admin:model.create and does not contain secretKey.
	usageRec := doJSON(t, h, http.MethodGet, "/api/knowledge/usage?projectId=p1", "", nil)
	if usageRec.Code != http.StatusOK {
		t.Fatalf("GET /api/knowledge/usage status = %d", usageRec.Code)
	}
	if strings.Contains(usageRec.Body.String(), secretKey) {
		t.Fatalf("usage activity leaked apiKey: %s", usageRec.Body.String())
	}
	if !strings.Contains(usageRec.Body.String(), `"tool":"admin:model.create"`) {
		t.Fatalf("expected admin:model.create in usage activity: %s", usageRec.Body.String())
	}

	// 2. Snake_case fields (base_url / api_key / embedding_dimensions) are rejected by Gin decode(DisallowUnknownFields).
	snakeRec := doJSON(t, h, http.MethodPost, "/api/knowledge/models?projectId=p1", `{"connectionId":"wk-1","name":"text-embedding-3-small","type":"Embedding","source":"remote","base_url":"https://api.openai.com/v1"}`, nil)
	if snakeRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for snake_case payload, got %d %s", snakeRec.Code, snakeRec.Body.String())
	}

	// 3. CapKBManage unavailable connection returns 503 capability_unavailable.
	denyRec := doJSON(t, h, http.MethodPost, "/api/knowledge/models?projectId=p1", `{"connectionId":"wk-deny","name":"bge-m3","type":"Embedding","source":"local"}`, nil)
	if denyRec.Code != http.StatusServiceUnavailable || !strings.Contains(denyRec.Body.String(), `"code":"capability_unavailable"`) {
		t.Fatalf("expected 503 capability_unavailable, got %d %s", denyRec.Code, denyRec.Body.String())
	}
}

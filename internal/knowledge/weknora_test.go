package knowledge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestNormalizeWeKnoraBaseURL(t *testing.T) {
	cases := map[string]string{
		"http://weknora-app:8080/":       "http://weknora-app:8080",
		"http://weknora-app:8080/api/v1": "http://weknora-app:8080",
		"https://wk.example.com/api/v1/": "https://wk.example.com",
	}
	for in, want := range cases {
		if got := NormalizeWeKnoraBaseURL(in); got != want {
			t.Fatalf("NormalizeWeKnoraBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWeKnoraClient_SearchFetchAndWrite(t *testing.T) {
	var seenAPIKey string
	var seenManualBody map[string]any
	var seenUploadFilename string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAPIKey = r.Header.Get("X-API-Key")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge-bases":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": []map[string]any{
					{"id": "kb-1", "name": "研发规范", "type": "document", "knowledge_count": 3},
					{"id": "kb-2", "name": "运维手册", "type": "document", "knowledge_count": 2},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-1/hybrid-search":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": []map[string]any{
					{"id": "c1", "knowledge_id": "doc-1", "knowledge_title": "Go 规范", "content": "使用 gofmt", "score": 0.72, "chunk_index": 0},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-2/hybrid-search":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": []map[string]any{
					{"id": "c2", "knowledge_id": "doc-2", "knowledge_title": "K8s 排障", "content": "查看 Pod 日志", "score": 0.91, "chunk_index": 1},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/chunks/doc-2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"total":   1,
				"data": []map[string]any{
					{"id": "c2", "content": "第一步：kubectl logs", "chunk_index": 0},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-1/knowledge/manual":
			_ = json.NewDecoder(r.Body).Decode(&seenManualBody)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"id": "doc-new", "title": seenManualBody["title"], "parse_status": "completed"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-1/knowledge/file":
			mr, err := r.MultipartReader()
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			for {
				p, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if p.FormName() == "file" {
					seenUploadFilename = p.FileName()
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"id": "doc-upload", "title": seenUploadFilename, "parse_status": "pending"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	settings := domain.KnowledgeSettings{
		Provider: domain.KnowledgeProviderWeKnora,
		Enabled:  true,
		URL:      srv.URL + "/api/v1/",
		Token:    "sk-weknora-secret",
	}
	client, err := NewWeKnoraClient(settings)
	if err != nil {
		t.Fatalf("NewWeKnoraClient: %v", err)
	}

	sources, err := client.ListSources(context.Background())
	if err != nil || len(sources) != 2 {
		t.Fatalf("ListSources = %#v, %v", sources, err)
	}
	if seenAPIKey != "sk-weknora-secret" {
		t.Fatalf("expected X-API-Key header, got %q", seenAPIKey)
	}

	res, err := client.Search(context.Background(), "排障", nil)
	if err != nil || len(res.Results) != 2 {
		t.Fatalf("Search = %#v, %v", res, err)
	}
	if res.Results[0].Path != "doc-2" || res.Results[0].Score < res.Results[1].Score {
		t.Fatalf("expected merged results sorted by score desc, got %#v", res.Results)
	}

	doc, err := client.Fetch(context.Background(), "kb-2", "doc-2", 0, 0)
	if err != nil || len(doc.Lines) != 1 || doc.Lines[0].Text != "第一步：kubectl logs" {
		t.Fatalf("Fetch = %#v, %v", doc, err)
	}

	wr, err := client.WriteDocument(context.Background(), "kb-1", "", "# 新经验\n\n内容")
	if err != nil || wr.Commit != "doc-new" {
		t.Fatalf("WriteDocument = %#v, %v", wr, err)
	}
	if seenManualBody["status"] != "publish" || seenManualBody["title"] != "新经验" {
		t.Fatalf("unexpected manual payload: %#v", seenManualBody)
	}

	uploaded, err := client.UploadDocument(context.Background(), "kb-1", "guide.md", strings.NewReader("# hello"))
	if err != nil || uploaded.ID != "doc-upload" || seenUploadFilename != "guide.md" {
		t.Fatalf("UploadDocument = %#v, %v (file=%q)", uploaded, err, seenUploadFilename)
	}
}

func TestService_WeKnoraSettingsCheckAndPublish(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge-bases":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    []map[string]any{{"id": "kb-1", "name": "团队库", "knowledge_count": 1}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-1/knowledge/manual":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"id": "wk-doc-99"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	store := newFakeStore()
	svc := NewService(store, func() string { return "1" })

	saved, err := svc.UpdateSettings(SettingsInput{
		Provider:     domain.KnowledgeProviderWeKnora,
		Enabled:      true,
		WriteEnabled: true,
		URL:          srv.URL + "/api/v1",
		Token:        "wk-key",
	}, "admin")
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if saved.EffectiveProvider() != domain.KnowledgeProviderWeKnora || saved.URL != srv.URL {
		t.Fatalf("unexpected saved settings: %#v", saved)
	}

	check := svc.CheckWithProvider(context.Background(), domain.KnowledgeProviderWeKnora, srv.URL, "wk-key")
	if !check.OK || len(check.Sources) != 1 || check.Sources[0].ID != "kb-1" {
		t.Fatalf("CheckWithProvider = %#v", check)
	}

	sub, err := svc.Submit(SubmissionInput{
		ProjectID: "p1",
		Title:     "发布沉淀",
		Source:    "kb-1",
		Content:   "这是总结内容",
	}, "alice")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	pub, err := svc.Publish(context.Background(), sub.ID, "admin")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if pub.Status != domain.KnowledgeSubmissionPublished || pub.Commit != "wk-doc-99" {
		t.Fatalf("unexpected published item: %#v", pub)
	}
}

func TestProvisionWeKnora_ExternalWithLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"token":   "jwt-tok",
				"user":    map[string]any{"tenant_id": 7},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/tenants/7/api-keys":
			if r.Header.Get("Authorization") != "Bearer jwt-tok" {
				http.Error(w, "unauthorized", 401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"token": "sk-issued-key"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge-bases":
			if r.Header.Get("X-API-Key") != "sk-issued-key" {
				http.Error(w, "invalid api key", 401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	store := newFakeStore()
	svc := NewService(store, func() string { return "1" })
	res, err := svc.ProvisionWeKnora(context.Background(), WeKnoraProvisionInput{
		Mode:         "external",
		URL:          srv.URL,
		Email:        "u@example.com",
		Password:     "secret",
		WriteEnabled: true,
	}, "admin")
	if err != nil {
		t.Fatalf("ProvisionWeKnora: %v", err)
	}
	if !res.Settings.Enabled || !res.Settings.TokenConfigured || res.Settings.URL != srv.URL {
		t.Fatalf("unexpected provisioned settings: %#v", res.Settings)
	}
	if store.KnowledgeSettings().Token != "sk-issued-key" {
		t.Fatalf("expected issued API key stored, got %q", store.KnowledgeSettings().Token)
	}
}

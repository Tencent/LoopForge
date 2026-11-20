package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agentflow/internal/apperr"
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

func TestActiveConnections_AllDisabledDoesNotFallThrough(t *testing.T) {
	s := domain.KnowledgeSettings{
		Provider: domain.KnowledgeProviderWeKnora,
		Enabled:  true,
		URL:      "http://legacy-mirror:8080",
		Token:    "legacy-token",
		Connections: []domain.KnowledgeConnection{
			{ID: "wk-1", Provider: domain.KnowledgeProviderWeKnora, URL: "http://wk-1:8080", Token: "tok-1", Enabled: false},
			{ID: "wk-2", Provider: domain.KnowledgeProviderWeKnora, URL: "http://wk-2:8080", Token: "tok-2", Enabled: false},
		},
	}
	if got := s.ActiveConnections(); len(got) != 0 {
		t.Fatalf("expected 0 active connections when all disabled, got %#v", got)
	}
	if s.Ready() {
		t.Fatalf("expected Ready() == false when all connections are disabled")
	}
}

func TestScopedSource_DeleteConnBlocksCrossConnAndConflictOnPublish(t *testing.T) {
	var srvBRequests atomic.Int32
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/knowledge-bases" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []any{}})
			return
		}
		srvBRequests.Add(1)
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer srvB.Close()

	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{
		Provider:       domain.KnowledgeProviderWeKnora,
		Enabled:        true,
		WriteEnabled:   true,
		URL:            "http://conn-a.invalid:8080",
		Token:          "tok-a",
		DefaultSources: []string{"kb-a1"},
		ProjectSources: map[string][]string{"p1": {"kb-a1"}},
	}
	store.submissions["sub-1"] = domain.KnowledgeSubmission{
		ID:        "sub-1",
		ProjectID: "p1",
		Title:     "Legacy pending",
		Source:    "kb-a1",
		Content:   "important body",
		Status:    domain.KnowledgeSubmissionPending,
	}

	svc := NewService(store, func() string { return "id-1" })

	// Upgrade single -> multi by provisioning Connection B.
	prov, err := svc.ProvisionWeKnora(context.Background(), WeKnoraProvisionInput{
		Mode:  "external",
		Name:  "ConnB",
		URL:   srvB.URL,
		Token: "tok-b",
	}, "admin")
	if err != nil {
		t.Fatalf("ProvisionWeKnora: %v", err)
	}
	if len(prov.Settings.Connections) != 2 {
		t.Fatalf("expected 2 connections after upgrade, got %d", len(prov.Settings.Connections))
	}
	connAID := prov.Settings.Connections[0].ID
	scopedA := connAID + "::kb-a1"

	// Verify single->multi migration prefixed DefaultSources, ProjectSources, and pending submission.
	if got := store.settings.DefaultSources; len(got) != 1 || got[0] != scopedA {
		t.Fatalf("expected DefaultSources migrated to %q, got %#v", scopedA, got)
	}
	if got := store.settings.ProjectSources["p1"]; len(got) != 1 || got[0] != scopedA {
		t.Fatalf("expected ProjectSources migrated to %q, got %#v", scopedA, got)
	}
	if got := store.submissions["sub-1"].Source; got != scopedA || store.submissions["sub-1"].Content != "important body" {
		t.Fatalf("expected submission migrated with content preserved, got %#v", store.submissions["sub-1"])
	}

	// Reset counter after ProvisionWeKnora's initial check.
	srvBRequests.Store(0)

	// Delete Connection A.
	if _, err := svc.DeleteConnection(connAID, "admin"); err != nil {
		t.Fatalf("DeleteConnection: %v", err)
	}
	if len(store.deletedConnToks) != 1 || store.deletedConnToks[0] != connAID {
		t.Fatalf("expected kbconn token deleted for %q, got %#v", connAID, store.deletedConnToks)
	}

	// Search on Connection A's bound source must fail as Unavailable with 0 requests to B.
	_, err = svc.Search(context.Background(), "p1", "hello", nil)
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Kind != apperr.Unavailable {
		t.Fatalf("expected Unavailable on Search, got %v", err)
	}

	// Document read on Connection A's bound source must fail as Unavailable with 0 requests to B.
	_, err = svc.Document(context.Background(), "p1", scopedA, "doc-1", 0, 0)
	if !errors.As(err, &appErr) || appErr.Kind != apperr.Unavailable {
		t.Fatalf("expected Unavailable on Document, got %v", err)
	}

	// Publish on pending submission bound to Connection A must fail with 409 Conflict and 0 requests to B.
	_, err = svc.Publish(context.Background(), "sub-1", "admin")
	if !errors.As(err, &appErr) || appErr.Kind != apperr.Conflict {
		t.Fatalf("expected Conflict on Publish, got %v", err)
	}

	if got := srvBRequests.Load(); got != 0 {
		t.Fatalf("expected 0 requests to Connection B, got %d", got)
	}
}

func TestWeKnora_DualConnectionsManagementRouting(t *testing.T) {
	var srv1Hits atomic.Int32
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv1Hits.Add(1)
		http.Error(w, "conn1 should not receive conn2 management requests", http.StatusInternalServerError)
	}))
	defer srv1.Close()

	var seenPaths []string
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPaths = append(seenPaths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []any{map[string]any{"id": "m2", "name": "Model2", "type": "Embedding"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"id": "kb-raw-2", "name": "KB2"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-raw-2/knowledge/file":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"id": "doc-file-2", "knowledge_base_id": "kb-raw-2", "title": "a.txt"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-raw-2/knowledge/url":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"id": "doc-url-2", "knowledge_base_id": "kb-raw-2", "title": "u"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-raw-2/knowledge/manual":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"id": "doc-man-2", "knowledge_base_id": "kb-raw-2", "title": "m"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge/doc-man-2":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"id": "doc-man-2", "knowledge_base_id": "kb-raw-2"}})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/knowledge/manual/doc-man-2":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"id": "doc-man-2", "knowledge_base_id": "kb-raw-2", "title": "m2"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge-bases/kb-raw-2/knowledge":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []any{map[string]any{"id": "doc-man-2", "knowledge_base_id": "kb-raw-2", "title": "m2"}}, "total": 1, "page": 1, "page_size": 20})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/knowledge/doc-man-2":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/knowledge-bases/kb-raw-2":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv2.Close()

	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{
		Provider: domain.KnowledgeProviderWeKnora,
		Enabled:  true,
		Connections: []domain.KnowledgeConnection{
			{ID: "wk-1", Name: "One", Provider: domain.KnowledgeProviderWeKnora, URL: srv1.URL, Token: "tok-1", Enabled: true},
			{ID: "wk-2", Name: "Two", Provider: domain.KnowledgeProviderWeKnora, URL: srv2.URL, Token: "tok-2", Enabled: true},
		},
		ProjectSources: map[string][]string{"p1": {"wk-1::kb-1"}},
	}
	svc := NewService(store, func() string { return "id-1" })
	ctx := context.Background()

	models, err := svc.WeKnoraModels(ctx, "wk-2")
	if err != nil || len(models) != 1 || models[0].ID != "m2" {
		t.Fatalf("WeKnoraModels(wk-2) = %#v, err=%v", models, err)
	}

	kb, err := svc.CreateWeKnoraKB(ctx, "p1", WeKnoraCreateKBInput{ConnectionID: "wk-2", Name: "KB2"})
	if err != nil {
		t.Fatalf("CreateWeKnoraKB: %v", err)
	}
	if kb.ID != "wk-2::kb-raw-2" {
		t.Fatalf("expected scoped kb.ID wk-2::kb-raw-2, got %q", kb.ID)
	}
	if !SourceAllowed(store.settings.SourcesForProject("p1"), "wk-2::kb-raw-2") {
		t.Fatalf("expected ProjectSources auto-authorized with wk-2::kb-raw-2, got %#v", store.settings.ProjectSources["p1"])
	}

	docFile, err := svc.UploadDocument(ctx, "p1", kb.ID, "a.txt", strings.NewReader("hello"))
	if err != nil || docFile.ID != "wk-2::doc-file-2" {
		t.Fatalf("UploadDocument = %#v, err=%v", docFile, err)
	}
	docURL, err := svc.CreateWeKnoraURLDoc(ctx, "p1", kb.ID, "https://example.com")
	if err != nil || docURL.ID != "wk-2::doc-url-2" {
		t.Fatalf("CreateWeKnoraURLDoc = %#v, err=%v", docURL, err)
	}
	docMan, err := svc.CreateWeKnoraManualDoc(ctx, "p1", kb.ID, "m", "body", true)
	if err != nil || docMan.ID != "wk-2::doc-man-2" {
		t.Fatalf("CreateWeKnoraManualDoc = %#v, err=%v", docMan, err)
	}
	docUpd, err := svc.UpdateWeKnoraManualDoc(ctx, "p1", docMan.ID, "m2", "body2", true)
	if err != nil || docUpd.ID != "wk-2::doc-man-2" {
		t.Fatalf("UpdateWeKnoraManualDoc = %#v, err=%v", docUpd, err)
	}
	page, err := svc.Documents(ctx, "p1", kb.ID, "", 1, 20)
	if err != nil || len(page.Documents) != 1 || page.Documents[0].ID != "wk-2::doc-man-2" {
		t.Fatalf("Documents = %#v, err=%v", page, err)
	}
	if err := svc.DeleteWeKnoraDoc(ctx, "p1", docMan.ID); err != nil {
		t.Fatalf("DeleteWeKnoraDoc: %v", err)
	}
	if err := svc.DeleteWeKnoraKB(ctx, "p1", kb.ID); err != nil {
		t.Fatalf("DeleteWeKnoraKB: %v", err)
	}
	if hits := srv1Hits.Load(); hits != 0 {
		t.Fatalf("expected 0 requests to connection 1, got %d", hits)
	}
	if len(seenPaths) < 8 {
		t.Fatalf("expected all management calls routed to connection 2, got %#v", seenPaths)
	}
}

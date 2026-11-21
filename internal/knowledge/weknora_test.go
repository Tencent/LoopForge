package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	// Search fans out to both knowledge bases, so handlers run concurrently.
	var seenMu sync.Mutex
	var seenAPIKey string
	var seenManualBody map[string]any
	var seenUploadFilename string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMu.Lock()
		seenAPIKey = r.Header.Get("X-API-Key")
		seenMu.Unlock()
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
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			seenMu.Lock()
			seenManualBody = body
			seenMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"id": "doc-new", "title": body["title"], "parse_status": "completed"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-1/knowledge/file":
			mr, err := r.MultipartReader()
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			var filename string
			for {
				p, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				if p.FormName() == "file" {
					filename = p.FileName()
				}
				_ = p.Close()
			}
			seenMu.Lock()
			seenUploadFilename = filename
			seenMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"id": "doc-upload", "title": filename, "parse_status": "pending"},
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
	seenMu.Lock()
	apiKey := seenAPIKey
	seenMu.Unlock()
	if apiKey != "sk-weknora-secret" {
		t.Fatalf("expected X-API-Key header, got %q", apiKey)
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
	seenMu.Lock()
	manualBody := seenManualBody
	seenMu.Unlock()
	if manualBody["status"] != "publish" || manualBody["title"] != "新经验" {
		t.Fatalf("unexpected manual payload: %#v", manualBody)
	}

	uploaded, err := client.UploadDocument(context.Background(), "kb-1", "guide.md", strings.NewReader("# hello"))
	seenMu.Lock()
	uploadedName := seenUploadFilename
	seenMu.Unlock()
	if err != nil || uploaded.ID != "doc-upload" || uploadedName != "guide.md" {
		t.Fatalf("UploadDocument = %#v, %v (file=%q)", uploaded, err, uploadedName)
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

func TestWeKnora_HybridSearchBoundedConcurrencyAndCancel(t *testing.T) {
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	var totalStarted atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		totalStarted.Add(1)
		for {
			prev := maxInFlight.Load()
			if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		inFlight.Add(-1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data":    []map[string]any{{"id": "c1", "knowledge_id": "d1", "content": "ok", "score": 0.8}},
		})
	}))
	defer srv.Close()

	wc, err := NewWeKnoraClient(domain.KnowledgeSettings{URL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	kbs := make([]string, 10)
	for i := range kbs {
		kbs[i] = fmt.Sprintf("kb-%d", i)
	}
	hits, partialErrs, err := wc.HybridSearchDetailed(context.Background(), "q", kbs, 10)
	if err != nil || len(partialErrs) != 0 || len(hits) == 0 {
		t.Fatalf("HybridSearchDetailed failed: hits=%d partial=%v err=%v", len(hits), partialErrs, err)
	}
	if max := maxInFlight.Load(); max > weKnoraSearchConcurrency || max < 2 {
		t.Fatalf("expected maxInFlight in [2, %d], got %d", weKnoraSearchConcurrency, max)
	}

	// Cancel context mid-flight: queued KBs must abort without running.
	totalStarted.Store(0)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, partialErrs, _ = wc.HybridSearchDetailed(ctx, "q", kbs, 10)
	if len(partialErrs) == 0 {
		t.Fatal("expected cancellation errors in partialErrs")
	}
	if started := totalStarted.Load(); started >= 10 {
		t.Fatalf("expected queued requests to cancel before all 10 started, got %d", started)
	}
}

func TestExecuteReadDocument_InPageOffsetAndCrossPageFill(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/chunks/doc-1") {
			http.NotFound(w, r)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
		if page <= 0 {
			page = 1
		}
		if pageSize <= 0 {
			pageSize = 20
		}
		const total = 50
		start := (page - 1) * pageSize
		end := start + pageSize
		if end > total {
			end = total
		}
		chunks := make([]map[string]any, 0, max(0, end-start))
		for idx := start; idx < end; idx++ {
			chunks = append(chunks, map[string]any{
				"id":          fmt.Sprintf("chunk-%d", idx),
				"chunk_index": idx,
				"content":     fmt.Sprintf("body-%d", idx),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"total":   total,
			"data":    chunks,
		})
	}))
	defer srv.Close()

	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{
		Enabled: true,
		Connections: []domain.KnowledgeConnection{{
			ID: "wk-1", Name: "WK", Provider: domain.KnowledgeProviderWeKnora, URL: srv.URL, Token: "tok", Enabled: true,
		}},
	}
	svc := NewService(store, nil)

	res, err := svc.ExecuteReadDocument(context.Background(), "p1", "wk-1::doc-1", "", 19, 20, 0, 0, "", false, ExecuteOptions{})
	if err != nil {
		t.Fatalf("ExecuteReadDocument: %v", err)
	}
	if res.Status != domain.KnowledgeStatusHit || res.Hits != 20 {
		t.Fatalf("expected 20 hits with status %q, got status=%q hits=%d", domain.KnowledgeStatusHit, res.Status, res.Hits)
	}
	var parsed struct {
		Chunks []struct {
			ID         string `json:"id"`
			ChunkIndex int    `json:"chunk_index"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal([]byte(res.Text), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Chunks) != 20 || parsed.Chunks[0].ChunkIndex != 19 || parsed.Chunks[19].ChunkIndex != 38 {
		t.Fatalf("expected chunk indices [19..38], got len=%d first=%d last=%d", len(parsed.Chunks), parsed.Chunks[0].ChunkIndex, parsed.Chunks[len(parsed.Chunks)-1].ChunkIndex)
	}
}

func TestExecuteSearch_MixedProvidersGlobalScoreSortAndLimit(t *testing.T) {
	// LightCloud MCP server (JSON-RPC)
	lcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"lc","version":"1.0"}}}`))
		case "tools/call":
			payload := `{"query":"q","mode":"hybrid","results":[{"source":"lc-kb","path":"a.md","title":"LC-Top","snippet":"s1","score":0.95},{"source":"lc-kb","path":"b.md","title":"LC-Low","snippet":"s2","score":0.40}]}`
			resp, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]any{"content": []map[string]any{{"type": "text", "text": payload}}},
			})
			_, _ = w.Write(resp)
		}
	}))
	defer lcSrv.Close()

	// WeKnora REST server
	wkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/knowledge-bases":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    []map[string]any{{"id": "wk-kb", "name": "WK KB"}},
			})
		case strings.HasSuffix(r.URL.Path, "/hybrid-search"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": []map[string]any{
					{"id": "c1", "knowledge_id": "d1", "knowledge_title": "WK-High", "content": "w1", "score": 0.85},
					{"id": "c2", "knowledge_id": "d2", "knowledge_title": "WK-Mid", "content": "w2", "score": 0.70},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wkSrv.Close()

	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{
		Enabled: true,
		Connections: []domain.KnowledgeConnection{
			{ID: "lc-1", Name: "LightCloud", Provider: domain.KnowledgeProviderLightCloud, URL: lcSrv.URL, Token: "tok1", Enabled: true},
			{ID: "wk-1", Name: "WeKnora", Provider: domain.KnowledgeProviderWeKnora, URL: wkSrv.URL, Token: "tok2", Enabled: true},
		},
	}
	svc := NewService(store, nil)

	out, execRes, err := svc.ExecuteSearch(context.Background(), "p1", "q", nil, 3, ExecuteOptions{})
	if err != nil {
		t.Fatalf("ExecuteSearch: %v", err)
	}
	if execRes.Status != domain.KnowledgeStatusHit || out.Status != domain.KnowledgeStatusHit {
		t.Fatalf("expected status %q, got exec=%q out=%q", domain.KnowledgeStatusHit, execRes.Status, out.Status)
	}
	if len(out.Result.Results) != 3 {
		t.Fatalf("expected limit=3 results, got %d", len(out.Result.Results))
	}
	gotTitles := []string{out.Result.Results[0].Title, out.Result.Results[1].Title, out.Result.Results[2].Title}
	wantTitles := []string{"LC-Top", "WK-High", "WK-Mid"}
	for i := range wantTitles {
		if gotTitles[i] != wantTitles[i] {
			t.Fatalf("sorted titles = %v, want %v", gotTitles, wantTitles)
		}
	}
}

func TestExecuteSearch_FourStatesConsistency(t *testing.T) {
	okEmptySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []any{}})
	}))
	defer okEmptySrv.Close()

	okHitSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data":    []map[string]any{{"id": "c1", "knowledge_id": "d1", "knowledge_title": "Hit", "content": "ok", "score": 0.9}},
		})
	}))
	defer okHitSrv.Close()

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer failSrv.Close()

	// 1. True empty set -> 无匹配
	s1 := newFakeStore()
	s1.settings = domain.KnowledgeSettings{
		Enabled:     true,
		Connections: []domain.KnowledgeConnection{{ID: "wk-1", Provider: domain.KnowledgeProviderWeKnora, URL: okEmptySrv.URL, Token: "t", Enabled: true}},
	}
	svcEmpty := NewService(s1, nil)
	out, res, err := svcEmpty.ExecuteSearch(context.Background(), "p1", "q", []string{"wk-1::kb1"}, 10, ExecuteOptions{})
	if err != nil || res.Status != domain.KnowledgeStatusEmpty || out.Status != domain.KnowledgeStatusEmpty {
		t.Fatalf("empty state mismatch: out=%q res=%q err=%v", out.Status, res.Status, err)
	}

	// 2. Partial failure (1 ok + 1 fail) -> 部分完成
	s2 := newFakeStore()
	s2.settings = domain.KnowledgeSettings{
		Enabled: true,
		Connections: []domain.KnowledgeConnection{
			{ID: "wk-1", Name: "OK", Provider: domain.KnowledgeProviderWeKnora, URL: okHitSrv.URL, Token: "t", Enabled: true},
			{ID: "wk-2", Name: "Fail", Provider: domain.KnowledgeProviderWeKnora, URL: failSrv.URL, Token: "t", Enabled: true},
		},
	}
	svcPartial := NewService(s2, nil)
	out, res, err = svcPartial.ExecuteSearch(context.Background(), "p1", "q", []string{"wk-1::kb1", "wk-2::kb2"}, 10, ExecuteOptions{})
	if err != nil || res.Status != domain.KnowledgeStatusPartial || !out.Result.Degraded || res.Warning == "" {
		t.Fatalf("partial state mismatch: out=%+v res=%+v err=%v", out, res, err)
	}

	// 3. All fail -> 不可用
	s3 := newFakeStore()
	s3.settings = domain.KnowledgeSettings{
		Enabled:     true,
		Connections: []domain.KnowledgeConnection{{ID: "wk-2", Name: "Fail", Provider: domain.KnowledgeProviderWeKnora, URL: failSrv.URL, Token: "t", Enabled: true}},
	}
	svcFail := NewService(s3, nil)
	_, res, err = svcFail.ExecuteSearch(context.Background(), "p1", "q", []string{"wk-2::kb2"}, 10, ExecuteOptions{})
	if err == nil || res.Status != domain.KnowledgeStatusUnavailable {
		t.Fatalf("all-fail state mismatch: res=%+v err=%v", res, err)
	}

	// 4. Bound to deleted connection (even with no project allowlist) -> 不可用 (never empty results)
	_, res, err = svcEmpty.ExecuteSearch(context.Background(), "p1", "q", []string{"wk-deleted::kb1"}, 10, ExecuteOptions{})
	if err == nil || res.Status != domain.KnowledgeStatusUnavailable {
		t.Fatalf("deleted-connection state mismatch: res=%+v err=%v", res, err)
	}
}

// ---------- REST transport rewrite (Step 1) ----------

// repeatReader streams n bytes without allocating the whole payload, standing
// in for a multi-megabyte upload body.
type repeatReader struct{ remaining int }

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > r.remaining {
		n = r.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = 'a'
	}
	r.remaining -= n
	return n, nil
}

func TestWeKnoraClientTimeoutsAndSharedTransport(t *testing.T) {
	var gotMu sync.Mutex
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMu.Lock()
		gotHeaders = r.Header.Clone()
		gotMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true,"data":[{"id":"kb-1","name":"库","type":"document","knowledge_count":2}]}`)
	}))
	defer srv.Close()

	c, err := NewWeKnoraClientWithConfig(WeKnoraClientConfig{
		BaseURL:     srv.URL + "/api/v1",
		APIKey:      "key-1",
		TenantID:    "42",
		BearerToken: "bearer-1",
	})
	if err != nil {
		t.Fatalf("NewWeKnoraClientWithConfig: %v", err)
	}
	if c.http.Timeout != weknoraRequestTimeout || c.stream.Timeout != weknoraStreamTimeout {
		t.Fatalf("timeouts = %v/%v, want %v/%v", c.http.Timeout, c.stream.Timeout, weknoraRequestTimeout, weknoraStreamTimeout)
	}
	if c.http.Transport != c.stream.Transport {
		t.Fatal("json and stream clients must share one Transport pointer")
	}
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok || tr.MaxIdleConnsPerHost < 8 {
		t.Fatalf("custom transport pool not configured: %#v", c.http.Transport)
	}

	kbs, err := c.ListKnowledgeBases(context.Background())
	if err != nil || len(kbs) != 1 || kbs[0].ID != "kb-1" {
		t.Fatalf("ListKnowledgeBases = %+v, %v", kbs, err)
	}
	gotMu.Lock()
	headers := gotHeaders
	gotMu.Unlock()
	if headers.Get("X-API-Key") != "key-1" || headers.Get("X-Tenant-ID") != "42" || headers.Get("Authorization") != "Bearer bearer-1" {
		t.Fatalf("auth headers = %v", headers)
	}
	if headers.Get("Accept") != "application/json" {
		t.Fatalf("Accept = %q", headers.Get("Accept"))
	}

	if _, err := NewWeKnoraClient(domain.KnowledgeSettings{URL: srv.URL}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("missing token error = %v, want ErrNotConfigured", err)
	}
}

func TestWeKnoraBearerOnlyOmitsAPIKeyHeader(t *testing.T) {
	var mu sync.Mutex
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true,"data":[]}`)
	}))
	defer srv.Close()

	c, err := NewWeKnoraClientWithConfig(WeKnoraClientConfig{BaseURL: srv.URL, BearerToken: "bearer-only"})
	if err != nil {
		t.Fatalf("NewWeKnoraClientWithConfig: %v", err)
	}
	if _, err := c.ListKnowledgeBases(context.Background()); err != nil {
		t.Fatalf("ListKnowledgeBases: %v", err)
	}
	mu.Lock()
	got := headers
	mu.Unlock()
	if key := got.Get("X-API-Key"); key != "" {
		t.Fatalf("bearer-only client must not send X-API-Key, got %q", key)
	}
	if auth := got.Get("Authorization"); auth != "Bearer bearer-only" {
		t.Fatalf("Authorization = %q", auth)
	}
	if _, err := NewWeKnoraClientWithConfig(WeKnoraClientConfig{BaseURL: srv.URL}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials error = %v, want ErrNotConfigured", err)
	}
}

func TestWeKnoraValidateOutboundURL(t *testing.T) {
	valid := map[string]string{
		"http://weknora-app:8080/":           "http://weknora-app:8080",
		"https://wk.example.com/api/v1":      "https://wk.example.com",
		"http://10.1.2.3:8080/prefix/api/v1": "http://10.1.2.3:8080/prefix",
		"http://wk.local#fragment":           "http://wk.local",
	}
	for in, want := range valid {
		got, err := ValidateOutboundWeKnoraURL(in)
		if err != nil || got != want {
			t.Fatalf("ValidateOutboundWeKnoraURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	invalid := []string{
		"",
		"ftp://wk.example.com",
		"http://user:pass@wk.example.com",
		"http://",
		"wk.example.com/api/v1",
		"http://wk.example.com/\x7f",
		"http://wk.example.com/\x01",
	}
	for _, in := range invalid {
		if got, err := ValidateOutboundWeKnoraURL(in); err == nil {
			t.Fatalf("ValidateOutboundWeKnoraURL(%q) = %q, want error", in, got)
		}
	}
	if _, err := ValidateOutboundWeKnoraURL(" "); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("blank url error = %v, want ErrNotConfigured", err)
	}
}

func TestWeKnoraParseUpstreamError(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantMsg    string
		wantCode   string
		structured bool
	}{
		{"gate string", 403, `{"error":"invalid api key"}`, "invalid api key", "", true},
		{"app error object", 404, `{"error":{"code":"not_found","message":"knowledge base not found"}}`, "knowledge base not found", "not_found", true},
		{"numeric code", 400, `{"error":{"code":40001,"message":"bad payload"}}`, "bad payload", "40001", true},
		{"code only", 409, `{"error":{"code":"conflict"}}`, http.StatusText(http.StatusConflict), "conflict", true},
		{"gateway message", 502, `{"message":"bad gateway"}`, "bad gateway", "", true},
		{"bare 404", 404, "404 page not found", "404 page not found", "", false},
		{"empty body", 500, "", http.StatusText(http.StatusInternalServerError), "", false},
		{"empty object", 500, `{}`, http.StatusText(http.StatusInternalServerError), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ue := parseUpstreamError(tc.status, []byte(tc.body))
			if ue.Status != tc.status || ue.Message != tc.wantMsg || ue.Code != tc.wantCode || ue.Structured != tc.structured {
				t.Fatalf("parseUpstreamError(%d, %q) = %+v", tc.status, tc.body, ue)
			}
			if !strings.Contains(ue.Error(), strconv.Itoa(tc.status)) {
				t.Fatalf("Error() = %q", ue.Error())
			}
		})
	}

	huge := strings.Repeat("x", 900<<10)
	ue := parseUpstreamError(500, []byte(`{"error":"`+huge+`"}`))
	if !ue.Structured || len([]rune(ue.Message)) > 600 {
		t.Fatalf("oversized message not clipped: structured=%v len=%d", ue.Structured, len([]rune(ue.Message)))
	}
}

// TestWeKnoraUpstreamErrorBodyCappedAtOneMiB proves the 1 MiB read cap through
// a real request: the JSON envelope closes past the cap, so a truncated body
// can no longer parse as a structured error.
func TestWeKnoraUpstreamErrorBodyCappedAtOneMiB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"`+strings.Repeat("x", 2<<20)+`"}`)
	}))
	defer srv.Close()

	c, err := NewWeKnoraClient(domain.KnowledgeSettings{URL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	_, err = c.ListKnowledgeBases(context.Background())
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Status != http.StatusBadGateway {
		t.Fatalf("ListKnowledgeBases error = %#v", err)
	}
	if ue.Structured {
		t.Fatalf("error body over 1 MiB must be truncated before its envelope closes: %+v", ue)
	}
	if n := len([]rune(ue.Message)); n == 0 || n > 600 {
		t.Fatalf("truncated message length = %d", n)
	}
}

func TestWeKnoraSameOriginRedirectLimit(t *testing.T) {
	var hops int32 = weknoraMaxRedirects
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/hop/"))
		if n < int(atomic.LoadInt32(&hops)) {
			http.Redirect(w, r, fmt.Sprintf("/hop/%d", n+1), http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	c, err := NewWeKnoraClient(domain.KnowledgeSettings{URL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	resp, err := c.Open(context.Background(), "/hop/0", nil)
	if err != nil {
		t.Fatalf("%d same-origin redirects should be allowed: %v", weknoraMaxRedirects, err)
	}
	_ = resp.Body.Close()

	atomic.StoreInt32(&hops, weknoraMaxRedirects+1)
	if _, err := c.Open(context.Background(), "/hop/0", nil); err == nil || !strings.Contains(err.Error(), "重定向次数") {
		t.Fatalf("redirect %d error = %v", weknoraMaxRedirects+1, err)
	}
}

func TestWeKnoraRejectsCrossOriginRedirect(t *testing.T) {
	var targetHits int32
	var leakMu sync.Mutex
	var leakedKey string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		leakMu.Lock()
		leakedKey = r.Header.Get("X-API-Key")
		leakMu.Unlock()
		_, _ = io.WriteString(w, `{"success":true,"data":[]}`)
	}))
	defer target.Close()

	sameOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/knowledge-bases" {
			http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer sameOrigin.Close()

	c, err := NewWeKnoraClient(domain.KnowledgeSettings{URL: sameOrigin.URL, Token: "secret-key"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	_, err = c.ListKnowledgeBases(context.Background())
	if err == nil {
		t.Fatal("cross-origin redirect must fail")
	}
	if !strings.Contains(err.Error(), "跨源重定向") {
		t.Fatalf("redirect error = %v", err)
	}
	if hits := atomic.LoadInt32(&targetHits); hits != 0 {
		leakMu.Lock()
		key := leakedKey
		leakMu.Unlock()
		t.Fatalf("third party received %d requests, api key %q", hits, key)
	}
}

func TestWeKnoraStreamSanitizesActiveContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/preview"):
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Header().Set("Content-Length", "11")
			_, _ = io.WriteString(w, "<svg></svg>")
		case strings.HasSuffix(r.URL.Path, "/download"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<html></html>")
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "404 page not found")
		}
	}))
	defer srv.Close()

	c, err := NewWeKnoraClient(domain.KnowledgeSettings{URL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	preview, err := c.OpenPreview(context.Background(), "doc-1")
	if err != nil {
		t.Fatalf("OpenPreview: %v", err)
	}
	defer preview.Body.Close()
	if preview.ContentType != "text/plain; charset=utf-8" {
		t.Fatalf("preview content type = %q", preview.ContentType)
	}
	if preview.ContentLength != 11 {
		t.Fatalf("preview content length = %d", preview.ContentLength)
	}
	body, _ := io.ReadAll(preview.Body)
	if string(body) != "<svg></svg>" {
		t.Fatalf("preview body = %q", body)
	}

	download, err := c.OpenDownload(context.Background(), "doc-1")
	if err != nil {
		t.Fatalf("OpenDownload: %v", err)
	}
	defer download.Body.Close()
	if download.ContentType != "text/plain; charset=utf-8" {
		t.Fatalf("download content type = %q", download.ContentType)
	}
	if !strings.HasPrefix(download.ContentDisposition, "attachment") {
		t.Fatalf("download disposition = %q", download.ContentDisposition)
	}
}

func TestWeKnoraSanitizeStreamContentType(t *testing.T) {
	active := []string{
		"text/html",
		"text/html; charset=utf-8",
		"application/xhtml+xml",
		"image/svg+xml",
		"text/xml",
		"application/xml",
		"text/javascript",
		"application/javascript",
		"application/x-javascript",
		"not a media type",
		"",
	}
	for _, in := range active {
		if got := SanitizeStreamContentType(in, false); got != "text/plain; charset=utf-8" {
			t.Fatalf("SanitizeStreamContentType(%q) = %q", in, got)
		}
	}
	if got := SanitizeStreamContentType("", true); got != "application/octet-stream" {
		t.Fatalf("attachment fallback = %q", got)
	}
	if got := SanitizeStreamContentType("application/pdf", false); got != "application/pdf" {
		t.Fatalf("safe type = %q", got)
	}
	if got := SanitizeStreamContentType("image/png; name=a.png", false); got != "image/png" {
		t.Fatalf("image png = %q", got)
	}
}

func TestWeKnoraTruncateLogPath(t *testing.T) {
	short := "/api/v1/knowledge-bases/kb-1/knowledge/file"
	if got := truncateLogPath(short); got != short {
		t.Fatalf("short path = %q", got)
	}
	long := "/api/v1/" + strings.Repeat("a", 400)
	got := truncateLogPath(long)
	if !strings.HasSuffix(got, "...") || len([]rune(got)) != weknoraLogPathMax+3 {
		t.Fatalf("long path = %q (len %d)", got, len([]rune(got)))
	}
}

func TestWeKnoraPostFileUnblocksOnEarlyUpstreamError(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = io.WriteString(w, `{"error":{"code":"file_too_large","message":"too large"}}`)
	}))
	defer srv.Close()

	c, err := NewWeKnoraClient(domain.KnowledgeSettings{URL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	baseline := runtime.NumGoroutine()
	start := time.Now()
	postErr := c.PostFile(context.Background(), "/api/v1/knowledge-bases/kb-1/knowledge/file", map[string]string{"mode": "publish"}, "file", "big.bin", &repeatReader{remaining: 8 << 20}, nil)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("PostFile blocked for %v after early 413", elapsed)
	}
	var ue *UpstreamError
	if !errors.As(postErr, &ue) || ue.Status != http.StatusRequestEntityTooLarge || !ue.Structured || ue.Code != "file_too_large" {
		t.Fatalf("PostFile error = %#v", postErr)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("upstream hits = %d", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > baseline+1 {
		if time.Now().After(deadline) {
			t.Fatalf("upload goroutine leaked: baseline=%d now=%d", baseline, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWeKnoraPatchKnowledgeBaseAndGetKnowledge(t *testing.T) {
	var patchMu sync.Mutex
	var patchMethod string
	var patchBody map[string]any
	var docHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/knowledge-bases/kb-1":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			patchMu.Lock()
			patchMethod, patchBody = r.Method, body
			patchMu.Unlock()
			_, _ = io.WriteString(w, `{"success":true,"data":{"id":"kb-1","name":"renamed","description":"desc"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge/doc-1":
			atomic.AddInt32(&docHits, 1)
			_, _ = io.WriteString(w, `{"success":true,"data":{"id":"doc-1","knowledge_base_id":"kb-1","title":"t"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "404 page not found")
		}
	}))
	defer srv.Close()

	c, err := NewWeKnoraClient(domain.KnowledgeSettings{URL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	kb, err := c.PatchKnowledgeBase(context.Background(), "kb-1", " renamed ", "desc")
	if err != nil || kb.Name != "renamed" {
		t.Fatalf("PatchKnowledgeBase = %+v, %v", kb, err)
	}
	patchMu.Lock()
	method, body := patchMethod, patchBody
	patchMu.Unlock()
	if method != http.MethodPatch || body["name"] != "renamed" || body["description"] != "desc" {
		t.Fatalf("patch method/body = %s %#v", method, body)
	}
	if _, err := c.UpdateKnowledgeBase(context.Background(), "kb-1", "renamed", "desc"); err != nil {
		t.Fatalf("UpdateKnowledgeBase alias: %v", err)
	}

	doc, err := c.GetKnowledge(context.Background(), "doc-1")
	if err != nil || doc.KnowledgeBaseID != "kb-1" {
		t.Fatalf("GetKnowledge = %+v, %v", doc, err)
	}
	if kbID, err := c.GetDocumentKBID(context.Background(), "doc-1"); err != nil || kbID != "kb-1" {
		t.Fatalf("GetDocumentKBID = %q, %v", kbID, err)
	}
	if kbID, err := c.GetDocumentKBID(context.Background(), "doc-1"); err != nil || kbID != "kb-1" {
		t.Fatalf("cached GetDocumentKBID = %q, %v", kbID, err)
	}
	if got := atomic.LoadInt32(&docHits); got != 2 {
		t.Fatalf("document lookups = %d, want 2 (one direct, one uncached reverse lookup)", got)
	}
}

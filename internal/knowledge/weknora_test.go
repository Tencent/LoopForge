package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

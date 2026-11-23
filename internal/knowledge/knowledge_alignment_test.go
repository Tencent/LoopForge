package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"agentflow/internal/domain"
)

func TestWeKnoraDocumentManualMetadata(t *testing.T) {
	cases := []struct {
		name     string
		metadata string
		content  string
		status   string
	}{
		{name: "object", metadata: `{"content":"# 标题\n\n正文","format":"markdown","status":"Draft"}`, content: "# 标题\n\n正文", status: "draft"},
		{name: "json-string-wrapped", metadata: `"{\"content\":\"正文\",\"status\":\"publish\"}"`, content: "正文", status: "publish"},
		{name: "empty", metadata: "", content: "", status: ""},
		{name: "null", metadata: "null", content: "", status: ""},
		{name: "malformed", metadata: "{not-json", content: "", status: ""},
		{name: "uploaded-file", metadata: `{"format":"markdown"}`, content: "", status: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := WeKnoraDocument{Metadata: json.RawMessage(tc.metadata)}
			content, status := doc.ManualMetadata()
			if content != tc.content || status != tc.status {
				t.Fatalf("ManualMetadata() = (%q, %q), want (%q, %q)", content, status, tc.content, tc.status)
			}
		})
	}
}

func TestAuthorizeCreatedKBAppendsToProjectAllowlist(t *testing.T) {
	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{
		DefaultSources: []string{"shared"},
		ProjectSources: map[string][]string{"p1": {"own"}},
	}
	svc := newTestService(store, nil)
	if err := svc.authorizeCreatedKB("p1", "p1::new"); err != nil {
		t.Fatalf("authorizeCreatedKB() error = %v", err)
	}
	if got := store.settings.ProjectSources["p1"]; !slices.Equal(got, []string{"own", "p1::new"}) {
		t.Fatalf("project allowlist = %v", got)
	}
	if got := store.settings.DefaultSources; !slices.Equal(got, []string{"shared"}) {
		t.Fatalf("default allowlist must be untouched, got %v", got)
	}
}

func TestAuthorizeCreatedKBMaterializesInheritedDefaults(t *testing.T) {
	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{DefaultSources: []string{"shared"}}
	svc := newTestService(store, nil)
	if err := svc.authorizeCreatedKB("p2", "p2::new"); err != nil {
		t.Fatalf("authorizeCreatedKB() error = %v", err)
	}
	if got := store.settings.ProjectSources["p2"]; !slices.Equal(got, []string{"shared", "p2::new"}) {
		t.Fatalf("materialized allowlist = %v", got)
	}
	if got := store.settings.DefaultSources; !slices.Equal(got, []string{"shared"}) {
		t.Fatalf("default allowlist must stay restricted, got %v", got)
	}
}

func TestAuthorizeCreatedKBGlobalAndUnrestricted(t *testing.T) {
	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{DefaultSources: []string{"shared"}}
	svc := newTestService(store, nil)
	if err := svc.authorizeCreatedKB("", "global::new"); err != nil {
		t.Fatalf("authorizeCreatedKB() error = %v", err)
	}
	if got := store.settings.DefaultSources; !slices.Equal(got, []string{"shared", "global::new"}) {
		t.Fatalf("global allowlist = %v", got)
	}
	// 空列表代表不限制：不应写入任何授权。
	empty := newFakeStore()
	if err := newTestService(empty, nil).authorizeCreatedKB("p3", "p3::new"); err != nil {
		t.Fatalf("authorizeCreatedKB() error = %v", err)
	}
	if empty.settings.ProjectSources != nil || empty.settings.DefaultSources != nil {
		t.Fatalf("unrestricted settings must not be materialized: %+v", empty.settings)
	}
	// 重复授权幂等。
	if err := svc.authorizeCreatedKB("", "global::new"); err != nil {
		t.Fatalf("authorizeCreatedKB() second call error = %v", err)
	}
	if got := store.settings.DefaultSources; !slices.Equal(got, []string{"shared", "global::new"}) {
		t.Fatalf("duplicate authorization appended twice: %v", got)
	}
}

func TestAuthorizeCreatedKBPersistErrorPropagates(t *testing.T) {
	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{DefaultSources: []string{"shared"}}
	store.mutateErr = errors.New("db down")
	if err := newTestService(store, nil).authorizeCreatedKB("p1", "p1::new"); err == nil {
		t.Fatal("authorizeCreatedKB() must surface persistence failures")
	}
}

func TestPruneSourceAllowlistsKeepsNonEmptyLists(t *testing.T) {
	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{
		DefaultSources: []string{"kb-1"},
		ProjectSources: map[string][]string{
			"single": {"kb-1"},
			"multi":  {"kb-1", "kb-2"},
		},
	}
	svc := newTestService(store, nil)
	if err := svc.pruneSourceAllowlists("kb-1"); err != nil {
		t.Fatalf("pruneSourceAllowlists() error = %v", err)
	}
	// 删空会退化为「不限制」，因此单元素列表必须保留原 ID。
	if got := store.settings.ProjectSources["single"]; !slices.Equal(got, []string{"kb-1"}) {
		t.Fatalf("single-entry project list = %v, want untouched", got)
	}
	if got := store.settings.ProjectSources["multi"]; !slices.Equal(got, []string{"kb-2"}) {
		t.Fatalf("multi-entry project list = %v", got)
	}
	if got := store.settings.DefaultSources; !slices.Equal(got, []string{"kb-1"}) {
		t.Fatalf("single-entry default list = %v, want untouched", got)
	}

	store.mutateErr = errors.New("db down")
	if err := svc.pruneSourceAllowlists("kb-2"); err == nil {
		t.Fatal("pruneSourceAllowlists() must surface persistence failures")
	}
}

func TestSourcesForConnectionExplicitDisabledAndAllowlist(t *testing.T) {
	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{
		Enabled:  true,
		Provider: "weknora",
		URL:      "http://kb/mcp",
		Token:    "tok",
		Connections: []domain.KnowledgeConnection{
			{ID: "off", Name: "停用", Provider: "weknora", URL: "http://kb/mcp", Token: "tok", Enabled: false},
		},
	}
	up := &fakeUpstream{sources: []Source{{ID: "kb-1"}, {ID: "kb-2"}}}
	svc := newTestService(store, up)

	views, err := svc.SourcesForConnection(context.Background(), "", "off")
	if err != nil {
		t.Fatalf("SourcesForConnection() error = %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("got %d sources, want 2", len(views))
	}
	if !views[0].Allowed || !views[1].Allowed {
		t.Fatalf("unrestricted project must allow every source")
	}

	scoped := EncodeScopedSourceID(store.settings, "off", "kb-1")
	store.settings.DefaultSources = []string{scoped}
	views, err = svc.SourcesForConnection(context.Background(), "", "off")
	if err != nil {
		t.Fatalf("SourcesForConnection() error = %v", err)
	}
	for _, view := range views {
		if want := view.ID == scoped; view.Allowed != want {
			t.Fatalf("source %q allowed = %v, want %v", view.ID, view.Allowed, want)
		}
	}

	if _, err := svc.SourcesForConnection(context.Background(), "", "missing"); err == nil {
		t.Fatal("unknown connection must return an error")
	}
	if _, err := svc.SourcesForConnection(context.Background(), "", ""); err == nil {
		t.Fatal("missing connectionId must be rejected")
	}
}

func TestSourcesExcludesDisabledConnections(t *testing.T) {
	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{
		Enabled:  true,
		Provider: "weknora",
		URL:      "http://kb/mcp",
		Token:    "tok",
		Connections: []domain.KnowledgeConnection{
			{ID: "off", Provider: "weknora", URL: "http://kb/mcp", Token: "tok", Enabled: false},
		},
	}
	up := &fakeUpstream{sources: []Source{{ID: "kb-1"}}}
	if _, err := newTestService(store, up).Sources(context.Background(), ""); err == nil {
		t.Fatal("default Sources() must not browse disabled connections")
	}
}

func TestEnsureDefaultManagedWeKnoraAutoProvisionsOnFreshInstall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/api/v1/auth/register":
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"success":true,"token":"jwt-1","active_tenant":{"id":10000}}`))
		case "/api/v1/tenants/10000/api-keys":
			_, _ = w.Write([]byte(`{"success":true,"data":{"token":"sk-auto-managed"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("LOOPFORGE_WEKNORA_URL", srv.URL)
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD", "test-pass-123")

	store := newFakeStore()
	up := &fakeUpstream{sources: []Source{{ID: "kb-1", Name: "Default KB"}}}
	svc := newTestService(store, up)

	settings, ok := svc.EnsureDefaultManagedWeKnora(context.Background())
	if !ok {
		t.Fatal("expected EnsureDefaultManagedWeKnora to auto-provision on fresh install")
	}
	if !settings.Ready() || len(settings.Connections) != 1 {
		t.Fatalf("expected 1 ready connection, got ready=%v conns=%d", settings.Ready(), len(settings.Connections))
	}
	if settings.Connections[0].Name != "内置 WeKnora" || !settings.Connections[0].Enabled || !settings.WriteEnabled {
		t.Fatalf("unexpected connection/settings: conn=%+v writeEnabled=%v", settings.Connections[0], settings.WriteEnabled)
	}

	// Second call must be a no-op once configured.
	if _, ok := svc.EnsureDefaultManagedWeKnora(context.Background()); ok {
		t.Fatal("expected EnsureDefaultManagedWeKnora to be a no-op after initial provision")
	}

	// If an administrator explicitly cleared connections (UpdatedBy != ""), do not override.
	store.settings = domain.KnowledgeSettings{UpdatedBy: "admin"}
	if _, ok := svc.EnsureDefaultManagedWeKnora(context.Background()); ok {
		t.Fatal("expected EnsureDefaultManagedWeKnora to respect admin-touched settings")
	}
}

func TestCreateWeKnoraModelCreatesEmbeddingModel(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/models":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"emb-1","name":"text-embedding-3-small","type":"Embedding","source":"remote"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":1003,"message":"not found"}}`))
		}
	}))
	defer srv.Close()

	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{
		Enabled:      true,
		WriteEnabled: true,
		Provider:     "weknora",
		Connections: []domain.KnowledgeConnection{
			{ID: "wk-1", Name: "内置 WeKnora", Provider: "weknora", URL: srv.URL, Token: "sk-1", Enabled: true},
		},
	}
	svc := newTestService(store, &fakeUpstream{})
	model, err := svc.CreateWeKnoraModel(context.Background(), WeKnoraCreateModelInput{
		ConnectionID:        "wk-1",
		Name:                "text-embedding-3-small",
		Type:                "Embedding",
		Source:              "remote",
		BaseURL:             "https://api.openai.com/v1",
		APIKey:              "sk-openai",
		EmbeddingDimensions: 1536,
	})
	if err != nil {
		t.Fatalf("CreateWeKnoraModel() error = %v", err)
	}
	if model.ID != "emb-1" || model.Type != "Embedding" {
		t.Fatalf("unexpected model: %+v", model)
	}
	params, _ := gotBody["parameters"].(map[string]any)
	if gotBody["name"] != "text-embedding-3-small" || gotBody["type"] != "Embedding" || params["base_url"] != "https://api.openai.com/v1" {
		t.Fatalf("unexpected upstream payload: %+v", gotBody)
	}
}

func TestEnsureDefaultManagedWeKnoraDoesNotOverwriteConcurrentAdminClearAndMultiReplica(t *testing.T) {
	var apiKeyCreates int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/api/v1/auth/register":
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"success":true,"token":"jwt-1","active_tenant":{"id":10000}}`))
		case "/api/v1/tenants/10000/api-keys":
			mu.Lock()
			apiKeyCreates++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"success":true,"data":{"token":"sk-auto-managed"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("LOOPFORGE_WEKNORA_URL", srv.URL)
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD", "test-pass-123")

	// 1. Multi-replica concurrent OnlyIfFresh calls on a brand-new store:
	// only ONE replica wins the fresh-install row lock and creates an API key on WeKnora (no orphan keys).
	store := newFakeStore()
	up := &fakeUpstream{}
	replicaA := newTestService(store, up)
	replicaB := newTestService(store, up)

	var wg sync.WaitGroup
	var resA, resB WeKnoraProvisionResult
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		resA, errA = replicaA.ProvisionWeKnora(context.Background(), WeKnoraProvisionInput{
			Mode: "managed", Name: "内置 WeKnora", WriteEnabled: true, OnlyIfFresh: true,
		}, "system:auto-weknora")
	}()
	go func() {
		defer wg.Done()
		resB, errB = replicaB.ProvisionWeKnora(context.Background(), WeKnoraProvisionInput{
			Mode: "managed", Name: "内置 WeKnora", WriteEnabled: true, OnlyIfFresh: true,
		}, "system:auto-weknora")
	}()
	wg.Wait()

	if errA != nil || errB != nil {
		t.Fatalf("ProvisionWeKnora(OnlyIfFresh) errors: errA=%v errB=%v", errA, errB)
	}
	if resA.Claimed == resB.Claimed {
		t.Fatalf("expected exactly one replica to claim fresh install (resA.Claimed=%v resB.Claimed=%v)", resA.Claimed, resB.Claimed)
	}
	if apiKeyCreates != 1 {
		t.Fatalf("expected exactly 1 WeKnora API key creation across replicas, got %d", apiKeyCreates)
	}

	// 2. Administrator deletes the connection and clears settings (even in local mode where actor == "" or UpdatedBy == ""):
	// subsequent OnlyIfFresh / EnsureDefaultManagedWeKnora calls must NEVER re-inject the connection.
	for _, conn := range store.KnowledgeSettings().Connections {
		if _, err := replicaA.DeleteConnection(conn.ID, ""); err != nil {
			t.Fatalf("DeleteConnection(%s) error = %v", conn.ID, err)
		}
	}
	if _, err := replicaA.UpdateSettings(SettingsInput{Enabled: false, ClearToken: true}, ""); err != nil {
		t.Fatalf("UpdateSettings(clear) error = %v", err)
	}
	afterClear := store.KnowledgeSettings()
	if len(afterClear.Connections) != 0 || afterClear.Enabled {
		t.Fatalf("expected empty disabled connections after admin clear, got %+v", afterClear)
	}
	if _, claimed := replicaB.EnsureDefaultManagedWeKnora(context.Background()); claimed {
		t.Fatalf("expected EnsureDefaultManagedWeKnora not to claim after admin cleared connections")
	}
	resAfterClear, err := replicaB.ProvisionWeKnora(context.Background(), WeKnoraProvisionInput{
		Mode: "managed", Name: "内置 WeKnora", WriteEnabled: true, OnlyIfFresh: true,
	}, "system:auto-weknora")
	if err != nil || resAfterClear.Claimed {
		t.Fatalf("expected ProvisionWeKnora(OnlyIfFresh) not to overwrite cleared settings: claimed=%v err=%v", resAfterClear.Claimed, err)
	}
	if apiKeyCreates != 1 {
		t.Fatalf("expected no additional WeKnora API key creation after admin clear, got %d", apiKeyCreates)
	}
}

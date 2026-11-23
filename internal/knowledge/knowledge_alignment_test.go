package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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

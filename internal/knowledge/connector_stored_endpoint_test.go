package knowledge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

// storedEndpointCase describes one connector type for the upgrade scenario:
// created while its private origin was allowlisted, then managed after the
// operator cleared LOOPFORGE_KNOWLEDGE_INTERNAL_ORIGINS.
type storedEndpointCase struct {
	name      string
	typ       string
	path      string // appended to the origin to form the endpoint
	secret    domain.KnowledgeConnectorSecret
	publicURL string
}

func storedEndpointCases() []storedEndpointCase {
	return []storedEndpointCase{
		{name: "mcp", typ: domain.ConnectorTypeMCP, path: "/mcp", publicURL: "https://mcp.example.com/mcp"},
		{name: "native-siyuan", typ: domain.ConnectorTypeSiYuan, secret: domain.KnowledgeConnectorSecret{Token: "siyuan-token"}, publicURL: "https://siyuan.example.com"},
		{name: "native-obsidian", typ: domain.ConnectorTypeObsidian, secret: domain.KnowledgeConnectorSecret{Token: "obsidian-key"}, publicURL: "https://obsidian.example.com"},
		{name: "weknora", typ: domain.ConnectorTypeWeKnora, secret: domain.KnowledgeConnectorSecret{Token: "weknora-key"}, publicURL: "https://weknora.example.com"},
	}
}

func TestStoredPrivateEndpointCanBeManagedAfterAllowlistRemoved(t *testing.T) {
	for _, tc := range storedEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			for _, origin := range []string{"http://127.0.0.1:27900", "http://172.18.0.5:27900"} {
				endpoint := origin + tc.path
				t.Setenv(privateConnectorOriginsEnv, origin)
				svc := NewService(newMemoryConnectorStore(), func() string { return "legacy" })
				conn, err := svc.CreateConnector(ctx, 7, ConnectorInput{Name: "legacy", Type: tc.typ, Endpoint: endpoint, Secret: tc.secret})
				if err != nil {
					t.Fatalf("create %s under allowlist: %v", endpoint, err)
				}

				t.Setenv(privateConnectorOriginsEnv, "") // upgrade / operator narrows the allowlist

				off := false
				got, err := svc.UpdateConnector(ctx, 7, conn.ID, ConnectorInput{Enabled: &off})
				if err != nil {
					t.Fatalf("%s: disable with unchanged endpoint: %v", endpoint, err)
				}
				if got.Enabled || got.Endpoint != conn.Endpoint || got.SecretConfigured != (tc.secret.Token != "") {
					t.Fatalf("%s: unexpected disabled connector: %+v", endpoint, got)
				}
				got, err = svc.UpdateConnector(ctx, 7, conn.ID, ConnectorInput{Name: "renamed"})
				if err != nil || got.Name != "renamed" {
					t.Fatalf("%s: rename with omitted endpoint: %+v %v", endpoint, got, err)
				}
				on := true
				got, err = svc.UpdateConnector(ctx, 7, conn.ID, ConnectorInput{Name: "renamed-again", Endpoint: conn.Endpoint, Type: tc.typ, Enabled: &on})
				if err != nil || got.Name != "renamed-again" || !got.Enabled {
					t.Fatalf("%s: rename/enable with resubmitted same endpoint: %+v %v", endpoint, got, err)
				}

				// Changing to a different private address still needs the allowlist.
				other := strings.Replace(origin, ":27900", ":27901", 1) + tc.path
				_, err = svc.UpdateConnector(ctx, 7, conn.ID, ConnectorInput{Endpoint: other, Secret: tc.secret})
				if !isAppErrKind(err, apperr.Invalid) || !strings.Contains(err.Error(), privateConnectorOriginsEnv) {
					t.Fatalf("%s: changing to another private endpoint must be rejected with an allowlist hint, got %v", endpoint, err)
				}
				if saved, _ := svc.GetConnector(7, conn.ID); saved.Endpoint != conn.Endpoint {
					t.Fatalf("%s: rejected update must not persist endpoint, got %q", endpoint, saved.Endpoint)
				}

				// A stored endpoint does not bypass the check path: saved connector
				// checks and override probes still reject the private address.
				if res := svc.CheckConnector(ctx, 7, conn.ID, nil); res.OK {
					t.Fatalf("%s: check of stored private connector must fail without allowlist: %+v", endpoint, res)
				}
				if res := svc.CheckConnector(ctx, 7, conn.ID, &ConnectorInput{Endpoint: conn.Endpoint}); res.OK {
					t.Fatalf("%s: check override with private endpoint must fail: %+v", endpoint, res)
				}
				if res := svc.CheckConnector(ctx, 7, "", &ConnectorInput{Type: tc.typ, Endpoint: endpoint, Secret: tc.secret}); res.OK || !strings.Contains(res.Error, "受保护的网络网段") {
					t.Fatalf("%s: probe with private endpoint must fail: %+v", endpoint, res)
				}

				// Moving to a public endpoint works (credentials must be resent).
				got, err = svc.UpdateConnector(ctx, 7, conn.ID, ConnectorInput{Endpoint: tc.publicURL, Secret: tc.secret})
				if err != nil || got.Endpoint != tc.publicURL {
					t.Fatalf("%s: change to public endpoint: %+v %v", endpoint, got, err)
				}
				// Once moved away, the old private address is no longer "stored".
				if _, err := svc.UpdateConnector(ctx, 7, conn.ID, ConnectorInput{Endpoint: endpoint, Secret: tc.secret}); !isAppErrKind(err, apperr.Invalid) {
					t.Fatalf("%s: moving back to the private endpoint must be rejected, got %v", endpoint, err)
				}
			}
		})
	}
}

func TestStoredEndpointDoesNotBypassAlwaysBlockedRanges(t *testing.T) {
	t.Setenv(privateConnectorOriginsEnv, "")
	for _, tc := range storedEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryConnectorStore()
			endpoint := "http://169.254.169.254" + tc.path
			if err := store.PutKnowledgeConnector(domain.KnowledgeConnector{ID: "meta", OwnerUserID: 7, Name: "meta", Type: tc.typ, Endpoint: endpoint, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			svc := NewService(store, nil)
			off := false
			_, err := svc.UpdateConnector(context.Background(), 7, "meta", ConnectorInput{Enabled: &off, Secret: tc.secret})
			if !isAppErrKind(err, apperr.Invalid) {
				t.Fatalf("stored metadata endpoint must stay rejected, got %v", err)
			}
		})
	}
}

func TestStoredEndpointFlagOnlyRelaxesPrivateLiteralCheck(t *testing.T) {
	t.Setenv(privateConnectorOriginsEnv, "")
	reg := DefaultRegistry()
	for _, tc := range storedEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			f, ok := reg.Factory(tc.typ)
			if !ok {
				t.Fatalf("factory %s missing", tc.typ)
			}
			endpoint := "http://127.0.0.1:27900" + tc.path
			if _, _, _, err := f.ValidateAndNormalize(endpoint, domain.KnowledgeConnectorConfig{}, tc.secret, false, false); !isAppErrKind(err, apperr.Invalid) {
				t.Fatalf("new private endpoint must be rejected, got %v", err)
			}
			if _, _, _, err := f.ValidateAndNormalize(endpoint, domain.KnowledgeConnectorConfig{}, tc.secret, false, true); err != nil {
				t.Fatalf("stored private endpoint must pass format validation, got %v", err)
			}
			// Other validation still runs for stored endpoints.
			if _, _, _, err := f.ValidateAndNormalize(endpoint, domain.KnowledgeConnectorConfig{TimeoutSec: 301}, tc.secret, false, true); !isAppErrKind(err, apperr.Invalid) {
				t.Fatalf("stored endpoint must still validate config, got %v", err)
			}
			if _, _, _, err := f.ValidateAndNormalize("http://user:pw@127.0.0.1:27900"+tc.path, domain.KnowledgeConnectorConfig{}, tc.secret, false, true); !isAppErrKind(err, apperr.Invalid) {
				t.Fatalf("stored endpoint must still reject embedded credentials, got %v", err)
			}
		})
	}
}

// The dial-time guard is unchanged: a stored private connector that was
// allowed to be renamed/disabled still cannot reach the private address.
func TestStoredPrivateEndpointStillBlockedAtDial(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	// localhost passes the literal check but resolves to loopback at dial time.
	origin := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	t.Setenv(privateConnectorOriginsEnv, "")
	reg := DefaultRegistry()
	for _, tc := range storedEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := reg.Factory(tc.typ)
			endpoint := origin + tc.path
			norm, cfg, secret, err := f.ValidateAndNormalize(endpoint, domain.KnowledgeConnectorConfig{}, tc.secret, false, true)
			if err != nil {
				t.Fatalf("validate stored endpoint: %v", err)
			}
			conn, err := f.New(ConnectorSpec{ID: "dial-" + tc.name, OwnerUserID: 7, Name: "dial", Type: tc.typ, Endpoint: norm, Config: cfg, Secret: secret})
			if err != nil {
				t.Fatalf("new connector: %v", err)
			}
			if res := conn.Check(context.Background()); res.OK {
				t.Fatalf("dial guard must block loopback without allowlist: %+v", res)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("private server must not be reached, got %d requests", n)
	}
}

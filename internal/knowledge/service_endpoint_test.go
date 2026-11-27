package knowledge

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

func TestSameConnectorEndpoint(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "empty", a: "", b: "", want: true},
		{name: "identical", a: "https://example.com/tenant/a?version=1", b: "https://example.com/tenant/a?version=1", want: true},
		{name: "surrounding whitespace", a: " https://example.com/tenant/a ", b: "https://example.com/tenant/a", want: true},
		{name: "scheme and host case", a: "HTTPS://EXAMPLE.COM/tenant/a", b: "https://example.com/tenant/a", want: true},
		{name: "fragment is not sent upstream", a: "https://example.com/tenant/a#one", b: "https://example.com/tenant/a#two", want: true},
		{name: "scheme changed", a: "https://example.com/tenant/a", b: "http://example.com/tenant/a", want: false},
		{name: "host changed", a: "https://example.com/tenant/a", b: "https://other.example.com/tenant/a", want: false},
		{name: "port changed", a: "https://example.com/tenant/a", b: "https://example.com:8443/tenant/a", want: false},
		{name: "path changed", a: "https://example.com/tenant/a", b: "https://example.com/tenant/b", want: false},
		{name: "escaped path changed", a: "https://example.com/tenant/%61", b: "https://example.com/tenant/a", want: false},
		{name: "query changed", a: "https://example.com/tenant/a?tenant=one", b: "https://example.com/tenant/a?tenant=two", want: false},
		{name: "query order changed", a: "https://example.com/tenant/a?a=1&b=2", b: "https://example.com/tenant/a?b=2&a=1", want: false},
		{name: "force query changed", a: "https://example.com/tenant/a", b: "https://example.com/tenant/a?", want: false},
		{name: "one empty", a: "", b: "https://example.com/tenant/a", want: false},
		{name: "invalid endpoint", a: "://bad", b: "https://example.com/tenant/a", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameConnectorEndpoint(tt.a, tt.b); got != tt.want {
				t.Fatalf("sameConnectorEndpoint(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestSavedCredentialsRequireUnchangedEndpoint(t *testing.T) {
	ctx := context.Background()
	const (
		uid      = int64(41)
		endpoint = "https://example.com/tenant/a?version=1"
	)
	newService := func() (*Service, string) {
		store := newMemoryConnectorStore()
		svc := NewService(store, func() string { return "endpoint_guard" })
		conn, err := svc.CreateConnector(ctx, uid, ConnectorInput{
			Name:     "endpoint guard",
			Type:     domain.ConnectorTypeMCP,
			Endpoint: endpoint,
			Secret:   domain.KnowledgeConnectorSecret{Token: "saved-secret"},
		})
		if err != nil {
			t.Fatalf("CreateConnector: %v", err)
		}
		return svc, conn.ID
	}

	t.Run("update preserves credentials only for the same endpoint", func(t *testing.T) {
		svc, id := newService()
		before, _ := svc.store.GetKnowledgeConnector(uid, id)
		updated, err := svc.UpdateConnector(ctx, uid, id, ConnectorInput{Endpoint: endpoint})
		if err != nil {
			t.Fatalf("same endpoint update: %v", err)
		}
		after, _ := svc.store.GetKnowledgeConnector(uid, id)
		if !updated.SecretConfigured || after.SecretEnc != before.SecretEnc {
			t.Fatal("same endpoint update did not preserve the saved credential")
		}
	})

	for _, changed := range []string{
		"https://example.com/tenant/b?version=1",
		"https://example.com/tenant/a?version=2",
		"https://example.com/tenant/a?version=1&",
	} {
		t.Run("update rejects "+changed, func(t *testing.T) {
			svc, id := newService()
			_, err := svc.UpdateConnector(ctx, uid, id, ConnectorInput{Endpoint: changed})
			if !isAppErrKind(err, apperr.Invalid) || !strings.Contains(err.Error(), "重新填写凭据") {
				t.Fatalf("UpdateConnector error = %v, want credential-required Invalid", err)
			}
			saved, _ := svc.store.GetKnowledgeConnector(uid, id)
			if saved.Endpoint != endpoint || !saved.SecretConfigured || saved.SecretEnc == "" {
				t.Fatalf("rejected update mutated connector: %+v", saved)
			}
		})
	}

	t.Run("check rejects changed path before dialing", func(t *testing.T) {
		svc, id := newService()
		res := svc.CheckConnector(ctx, uid, id, &ConnectorInput{Endpoint: "https://example.com/tenant/b?version=1"})
		if res.OK || !strings.Contains(res.Error, "重新填写凭据") {
			t.Fatalf("CheckConnector = %+v, want credential-required failure", res)
		}
	})

	t.Run("clearing credentials permits an endpoint change", func(t *testing.T) {
		svc, id := newService()
		updated, err := svc.UpdateConnector(ctx, uid, id, ConnectorInput{
			Endpoint:    "https://example.com/tenant/b?version=1",
			ClearSecret: true,
		})
		if err != nil {
			t.Fatalf("clear-secret update: %v", err)
		}
		saved, _ := svc.store.GetKnowledgeConnector(uid, id)
		if updated.SecretConfigured || saved.SecretConfigured || saved.SecretEnc != "" {
			t.Fatalf("clear-secret update retained credentials: %+v", saved)
		}
	})
}

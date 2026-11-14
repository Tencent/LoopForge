package httpapi

import (
	"context"
	"strings"
	"testing"
)

func TestResolveSteerModelSharedKeyRefusesOtherModel(t *testing.T) {
	h, user := workbenchFixture(t)
	shared := []string{"steer-allowed-model"}
	// The sponsor refusal comes before the effort check, so a bogus effort
	// must not change the error.
	for _, effort := range []string{"", "bogus"} {
		model, err := h.resolveSteerModel(context.Background(), user, shared, "steer-other-model", effort)
		if err == nil || err.Error() != errAssistantSponsoredModel(shared) {
			t.Fatalf("effort %q: got (%q, %v), want sponsor refusal", effort, model, err)
		}
		if model != "" {
			t.Fatalf("effort %q: refused turn returned model %q", effort, model)
		}
	}
}

func TestResolveSteerModelEmptyRequestUsesCatalogDefault(t *testing.T) {
	h, user := workbenchFixture(t)
	ctx := context.Background()
	want := h.chatCatalog(ctx, user).Default
	model, err := h.resolveSteerModel(ctx, user, nil, "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if model != want {
		t.Fatalf("model = %q, want catalog default %q", model, want)
	}
}

func TestResolveSteerModelKeepsCatalogModel(t *testing.T) {
	h, user := workbenchFixture(t)
	ctx := context.Background()
	catalog := h.chatCatalog(ctx, user)
	if len(catalog.Models) == 0 {
		t.Skip("chat catalog has no models")
	}
	requested := catalog.Models[len(catalog.Models)-1].ID
	want := assistantAllowedModel(requested, catalog)
	model, err := h.resolveSteerModel(ctx, user, nil, requested, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if model != want {
		t.Fatalf("model = %q, want %q", model, want)
	}
}

func TestResolveSteerModelRejectsUnsupportedEffort(t *testing.T) {
	h, user := workbenchFixture(t)
	model, err := h.resolveSteerModel(context.Background(), user, nil, "", "bogus")
	if err == nil || !strings.Contains(err.Error(), "不支持思考强度") {
		t.Fatalf("got (%q, %v), want unsupported effort error", model, err)
	}
	if model != "" {
		t.Fatalf("rejected turn returned model %q", model)
	}
}

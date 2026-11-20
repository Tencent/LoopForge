package httpapi

import (
	"context"
	"testing"

	"agentflow/internal/domain"
)

// A request without a model runs on the default the picker shows: the first
// saved personal CodeBuddy model. Environment models stay accepted.
func TestCodeBuddyPersonalModelsDefaultMatchesPicker(t *testing.T) {
	user := domain.User{CodeBuddyAPIKey: "test-only", CodeBuddyModels: []domain.CLIModel{{ID: "personal-a"}, {ID: "personal-b", Name: "B"}}}
	base := assistantModelCatalog{Default: "env-default", Models: []assistantModel{{ID: "env-default"}, {ID: "env-other"}}}

	catalog := withCodeBuddyPersonalModels(base, user)
	if catalog.Default != "personal-a" {
		t.Fatalf("default = %q, want the first saved model", catalog.Default)
	}
	for _, id := range []string{"env-default", "env-other", "personal-a", "personal-b"} {
		if got := assistantAllowedUserModel(id, catalog, user); got != id {
			t.Fatalf("model %s not accepted: %q", id, got)
		}
	}
	if got := assistantAllowedUserModel("", catalog, user); got != "personal-a" {
		t.Fatalf("empty request runs on %q, want personal-a", got)
	}

	// Without saved models (or without a personal key) nothing changes.
	if got := withCodeBuddyPersonalModels(base, domain.User{CodeBuddyAPIKey: "test-only"}); got.Default != "env-default" || len(got.Models) != 2 {
		t.Fatalf("catalog without saved models changed: %+v", got)
	}
	if got := withCodeBuddyPersonalModels(base, domain.User{CodeBuddyModels: user.CodeBuddyModels}); got.Default != "env-default" {
		t.Fatalf("saved models without a key changed the default: %+v", got)
	}

	if model, err := scheduledRunModel(context.Background(), "", user); err != nil || model != "personal-a" {
		t.Fatalf("scheduled run without a model = %q, %v; want personal-a", model, err)
	}
	if model, err := scheduledRunModel(context.Background(), "personal-b", user); err != nil || model != "personal-b" {
		t.Fatalf("scheduled run with a saved model = %q, %v", model, err)
	}
}

package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"agentflow/internal/domain"
)

func TestFilterOwnedByProject(t *testing.T) {
	items := []domain.RuleDefinition{
		{ID: "a", ProjectID: "p1"},
		{ID: "b", ProjectID: "p2", Shared: true},
		{ID: "c", ProjectID: ""},
		{ID: "d", ProjectID: "p1", Shared: true},
	}
	owner := func(item domain.RuleDefinition) string { return item.ProjectID }

	got := filterOwnedByProject(items, "p1", owner)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "d" {
		t.Fatalf("filter p1 = %+v, want [a d] in order", got)
	}
	// Shared items from other projects are not admitted.
	if got := filterOwnedByProject(items, "p3", owner); got == nil || len(got) != 0 {
		t.Fatalf("filter p3 = %#v, want non-nil empty slice", got)
	}
	if got := filterOwnedByProject[domain.RuleDefinition](nil, "p1", owner); got == nil || len(got) != 0 {
		t.Fatalf("filter nil = %#v, want non-nil empty slice", got)
	}
}

func TestCatalogRulesAndSkillsListFilterByProject(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, p := range []domain.Project{
		{ID: "pf-one", Key: "PFONE", Name: "PF One", OwnerID: f.user.ID},
		{ID: "pf-two", Key: "PFTWO", Name: "PF Two", OwnerID: f.user.ID},
		{ID: "pf-empty", Key: "PFEMPTY", Name: "PF Empty", OwnerID: f.user.ID},
	} {
		if err := f.s.PutProject(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"rules", "skills"} {
		for _, body := range []map[string]any{
			{"id": "pf-" + kind + "-one", "name": "One", "projectId": "pf-one"},
			{"id": "pf-" + kind + "-two", "name": "Two", "projectId": "pf-two", "shared": true},
		} {
			if rec := cvBRequest(t, f, http.MethodPut, "/api/"+kind, body, true); rec.Code != http.StatusOK {
				t.Fatalf("put %s %v = %d: %s", kind, body["id"], rec.Code, rec.Body.String())
			}
		}

		rec := cvBRequest(t, f, http.MethodGet, "/api/"+kind+"?projectId=pf-one", nil, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s = %d: %s", kind, rec.Code, rec.Body.String())
		}
		var items []struct {
			ID        string `json:"id"`
			ProjectID string `json:"projectId"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].ID != "pf-"+kind+"-one" || items[0].ProjectID != "pf-one" {
			t.Fatalf("list %s?projectId=pf-one = %+v, want only pf-%s-one", kind, items, kind)
		}

		rec = cvBRequest(t, f, http.MethodGet, "/api/"+kind+"?projectId=pf-empty", nil, true)
		if rec.Code != http.StatusOK || rec.Body.String() != "[]" {
			t.Fatalf("list %s?projectId=pf-empty = %d %q, want 200 []", kind, rec.Code, rec.Body.String())
		}
	}
}

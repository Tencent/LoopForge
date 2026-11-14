package httpapi

import (
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// TestInsightSourceCreateAppliesMappingAndExpiryRules drives the extracted
// validators through POST /api/insights/v1/sources so the handler wiring
// (argument passing, error text, validation order, ExpiresAt) stays covered.
func TestInsightSourceCreateAppliesMappingAndExpiryRules(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "insights-validate-secret")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	for _, login := range []string{"src-owner", "src-member", "src-outsider"} {
		if err := s.PutUser(domain.User{LoginName: login}); err != nil {
			t.Fatal(err)
		}
	}
	owner, _ := s.UserByLoginName("src-owner")
	member, _ := s.UserByLoginName("src-member")
	outsider, _ := s.UserByLoginName("src-outsider")
	if err := s.PutProject(domain.Project{ID: "src-project", Name: "Src", OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: member.ID, Role: "member"}}}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	type created struct {
		Source domain.InsightSource `json:"source"`
		Token  string               `json:"token"`
	}
	const (
		path       = "/api/insights/v1/sources"
		mappingErr = "外部用户须映射到本项目的现有成员或负责人"
		expiryErr  = "有效期须为 1–365 天"
	)
	body := func(mappings map[string]int64, days any) map[string]any {
		in := map[string]any{"projectId": "src-project", "name": "CI", "userMappings": mappings}
		if days != nil {
			in["expiresInDays"] = days
		}
		return in
	}
	good := map[string]int64{"alice": member.ID, "boss": owner.ID}
	bad := map[string]int64{"carol": outsider.ID}

	for _, tc := range []struct {
		name string
		days any
		want time.Duration
	}{
		{name: "omitted uses 90 days", days: nil, want: 90 * 24 * time.Hour},
		{name: "explicit 30 days", days: 30, want: 30 * 24 * time.Hour},
		{name: "upper bound 365 days", days: 365, want: 365 * 24 * time.Hour},
		{name: "lower bound 1 day", days: 1, want: 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := requestJSONAuthed[created](t, h, owner, "POST", path, body(good, tc.days), 201)
			if d := got.Source.ExpiresAt.Sub(got.Source.CreatedAt); d != tc.want {
				t.Fatalf("ExpiresAt-CreatedAt = %s, want %s (%+v)", d, tc.want, got.Source)
			}
			if len(got.Source.UserMappings) != len(good) {
				t.Fatalf("mappings not stored: %+v", got.Source.UserMappings)
			}
		})
	}
	stored := len(requestJSONAuthed[[]domain.InsightSource](t, h, owner, "GET", path+"?projectId=src-project", nil, 200))

	for _, tc := range []struct {
		name     string
		mappings map[string]int64
		days     any
		wantErr  string
	}{
		{name: "outsider mapping", mappings: bad, wantErr: mappingErr},
		{name: "too long", mappings: good, days: 366, wantErr: expiryErr},
		{name: "negative", mappings: good, days: -1, wantErr: expiryErr},
		{name: "mapping is checked before expiry", mappings: bad, days: 400, wantErr: mappingErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := requestJSONAuthed[map[string]any](t, h, owner, "POST", path, body(tc.mappings, tc.days), 400)
			if got["error"] != tc.wantErr {
				t.Fatalf("error = %v, want %q", got["error"], tc.wantErr)
			}
		})
	}
	if after := len(requestJSONAuthed[[]domain.InsightSource](t, h, owner, "GET", path+"?projectId=src-project", nil, 200)); after != stored {
		t.Fatalf("rejected requests created sources: %d -> %d", stored, after)
	}
}

package repo

import (
	"testing"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

func TestReviewProjectJoinRequestChecksLockedProject(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*Store, domain.Project) error
		reviewer int64
		admin    bool
		approve  bool
		role     string
		want     apperr.Kind
	}{
		{name: "already viewer", reviewer: 10, approve: true, role: "member", want: apperr.Conflict,
			change: func(s *Store, p domain.Project) error {
				p.Members = append(p.Members, domain.ProjectMember{UserID: 30, Role: "viewer"})
				return s.PutProject(p)
			}},
		{name: "already owner", reviewer: 10, admin: true, approve: true, role: "member", want: apperr.Conflict,
			change: func(s *Store, p domain.Project) error { p.OwnerID = 30; return s.PutProject(p) }},
		{name: "archived approval", reviewer: 10, admin: true, approve: true, role: "member", want: apperr.NotFound,
			change: func(s *Store, p domain.Project) error { now := time.Now(); return s.SetProjectArchived(p.ID, &now, 10) }},
		{name: "archived rejection", reviewer: 10, role: "member", want: apperr.NotFound,
			change: func(s *Store, p domain.Project) error { now := time.Now(); return s.SetProjectArchived(p.ID, &now, 10) }},
		{name: "revoked maintainer approval", reviewer: 20, approve: true, role: "member", want: apperr.Forbidden,
			change: func(s *Store, p domain.Project) error { p.Members[0].Role = "member"; return s.PutProject(p) }},
		{name: "revoked maintainer rejection", reviewer: 20, role: "member", want: apperr.Forbidden,
			change: func(s *Store, p domain.Project) error { p.Members[0].Role = "member"; return s.PutProject(p) }},
		{name: "previous owner", reviewer: 10, approve: true, role: "member", want: apperr.Forbidden,
			change: func(s *Store, p domain.Project) error { p.OwnerID = 40; return s.PutProject(p) }},
		{name: "maintainer cannot promote", reviewer: 20, approve: true, role: "maintainer", want: apperr.Forbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			if err := s.PutProject(domain.Project{ID: "p", Name: "P", OwnerID: 10,
				Members: []domain.ProjectMember{{UserID: 20, Role: "maintainer"}}}); err != nil {
				t.Fatal(err)
			}
			req, err := s.CreateProjectJoinRequest("p", 30, "member", "")
			if err != nil {
				t.Fatal(err)
			}
			p, _ := s.Project("p")
			if tc.change != nil {
				if err := tc.change(s, p); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := s.Project("p")
			_, err = s.ReviewProjectJoinRequest(req.ID, tc.reviewer, tc.admin, tc.approve, tc.role)
			if app, ok := apperr.As(err); !ok || app.Kind != tc.want {
				t.Fatalf("want error kind %v, got %v", tc.want, err)
			}
			stored, _ := s.ProjectJoinRequest(req.ID)
			if stored.Status != domain.JoinRequestPending || stored.ReviewerID != 0 || stored.ReviewedAt != nil || stored.Role != "member" {
				t.Fatalf("failed review changed request: %+v", stored)
			}
			after, _ := s.Project("p")
			if after.MembersVersion != before.MembersVersion {
				t.Fatalf("failed review changed membership: %+v", after.Members)
			}
		})
	}
}

func TestProjectMembersVersionProtectsReplacement(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutProject(domain.Project{ID: "p", Name: "P", OwnerID: 10,
		Members: []domain.ProjectMember{{UserID: 20, Role: "maintainer"}}}); err != nil {
		t.Fatal(err)
	}
	stale, _ := s.Project("p")
	listed := s.Projects()
	for _, p := range listed {
		if p.ID == stale.ID && p.MembersVersion != stale.MembersVersion {
			t.Fatal("list and detail must expose the same snapshot token")
		}
	}
	req, err := s.CreateProjectJoinRequest("p", 30, "member", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewProjectJoinRequest(req.ID, 20, false, true, "viewer"); err != nil {
		t.Fatal(err)
	}
	stale.Description = "old draft"
	if err := s.PutProject(stale); err == nil {
		t.Fatal("stale member replacement was accepted")
	}
	fresh, _ := s.Project("p")
	if domain.ProjectMemberRole(fresh, 30) != "viewer" || fresh.Description != "" || fresh.MembersVersion == stale.MembersVersion {
		t.Fatalf("stale draft changed the project: %+v", fresh)
	}
	unversioned := fresh
	unversioned.MembersVersion = ""
	unversioned.Members = []domain.ProjectMember{}
	if err := s.PutProject(unversioned); err == nil {
		t.Fatal("member replacement without a snapshot token was accepted")
	}
	// Settings-only writes read the roster inside the write transaction.
	if err := s.PutProject(domain.Project{ID: "p", Name: "P", Description: "new description"}); err != nil {
		t.Fatal(err)
	}
	current, _ := s.Project("p")
	if current.MembersVersion != fresh.MembersVersion || domain.ProjectMemberRole(current, 30) != "viewer" {
		t.Fatal("settings-only save changed membership")
	}
	// A current token still permits intentional role changes and removals.
	current.Members = []domain.ProjectMember{{UserID: 20, Role: "member"}}
	if err := s.PutProject(current); err != nil {
		t.Fatal(err)
	}
	current, _ = s.Project("p")
	if domain.ProjectMemberRole(current, 30) != "" || domain.ProjectMemberRole(current, 20) != "member" {
		t.Fatalf("intentional member replacement failed: %+v", current.Members)
	}
	if err := s.PutProject(fresh); err == nil {
		t.Fatal("stale roles could overwrite a current member edit")
	}
}

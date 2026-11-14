package httpapi

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestValidInsightUserMappings(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, login := range []string{"map-owner", "map-member", "map-outsider"} {
		if err := st.PutUser(domain.User{LoginName: login}); err != nil {
			t.Fatal(err)
		}
	}
	owner, _ := st.UserByLoginName("map-owner")
	member, _ := st.UserByLoginName("map-member")
	outsider, _ := st.UserByLoginName("map-outsider")
	project := domain.Project{ID: "map-project", OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: member.ID, Role: "member"}, {UserID: 987654, Role: "member"}}}
	h := &insightsHandlers{deps: &deps{repo: st}}

	cases := []struct {
		name     string
		mappings map[string]int64
		want     bool
	}{
		{"empty", nil, true},
		{"owner and member", map[string]int64{"alice": owner.ID, "bob": member.ID}, true},
		{"128-byte external id", map[string]int64{strings.Repeat("a", 128): member.ID}, true},
		{"outsider", map[string]int64{"carol": outsider.ID}, false},
		{"member row without user", map[string]int64{"ghost": 987654}, false},
		{"zero uid", map[string]int64{"nobody": 0}, false},
		{"blank external id", map[string]int64{"  ": member.ID}, false},
		{"129-byte external id", map[string]int64{strings.Repeat("a", 129): member.ID}, false},
		{"one bad entry rejects all", map[string]int64{"alice": owner.ID, "carol": outsider.ID}, false},
	}
	for _, tc := range cases {
		if got := h.validInsightUserMappings(project, tc.mappings); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestInsightSourceExpiryDays(t *testing.T) {
	cases := []struct {
		in, want int
		ok       bool
	}{
		{0, 90, true},
		{1, 1, true},
		{30, 30, true},
		{365, 365, true},
		{366, 366, false},
		{-1, -1, false},
	}
	for _, tc := range cases {
		got, ok := insightSourceExpiryDays(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("insightSourceExpiryDays(%d) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

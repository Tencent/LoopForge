package repo

import (
	"agentflow/internal/domain"
	"reflect"
	"testing"
)

func TestNewBusinessDomainGroupsKeepsOrderAndAppendsUnclassified(t *testing.T) {
	groups, order := newBusinessDomainGroups([]domain.BusinessDomain{
		{ID: "bd-b", Name: "支付", Active: true},
		{ID: "bd-a", Name: "广告", Active: false},
	})
	if want := []string{"bd-b", "bd-a", ""}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	want := map[string]domain.BusinessDomainUsage{
		"bd-b": {ID: "bd-b", Name: "支付", Active: true, CostKnown: true},
		"bd-a": {ID: "bd-a", Name: "广告", Active: false, CostKnown: true},
		"":     {ID: "", Name: "未分类", Active: true, CostKnown: true},
	}
	for id, w := range want {
		g := groups[id]
		if g == nil || !reflect.DeepEqual(*g, w) {
			t.Fatalf("group %q = %+v, want %+v", id, g, w)
		}
	}
}

func TestNewBusinessDomainGroupsWithoutDomains(t *testing.T) {
	groups, order := newBusinessDomainGroups(nil)
	if !reflect.DeepEqual(order, []string{""}) || len(groups) != 1 || groups[""].Name != "未分类" {
		t.Fatalf("groups = %+v, order = %v", groups, order)
	}
}

func TestAddBusinessDomainProjectUsageSumsAndAndsCostKnown(t *testing.T) {
	g := &domain.BusinessDomainUsage{ID: "bd", CostKnown: true}
	addBusinessDomainProjectUsage(g, domain.AnalyticsProjectUsage{ProjectID: "p1", Runs: 3, Success: 2, Failed: 1, Tokens: 100, CostUSD: 1.5, CostCNY: 10, UniqueIssues: 2, CostKnown: true})
	addBusinessDomainProjectUsage(g, domain.AnalyticsProjectUsage{ProjectID: "p2", Runs: 1, Success: 1, Tokens: 50, CostUSD: 0.5, CostCNY: 4, UniqueIssues: 1, CostKnown: false})
	addBusinessDomainProjectUsage(g, domain.AnalyticsProjectUsage{ProjectID: "p3", CostKnown: true})
	want := domain.BusinessDomainUsage{ID: "bd", ProjectCount: 3, Runs: 4, Success: 3, Failed: 1, Tokens: 150, CostUSD: 2, CostCNY: 14, UniqueIssues: 3, CostKnown: false}
	if !reflect.DeepEqual(*g, want) {
		t.Fatalf("usage = %+v, want %+v", *g, want)
	}
}

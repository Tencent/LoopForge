package repo

import (
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// BusinessDomainReport must group project usage by the domains it is given,
// in their order, append the "未分类" group last, drop groups without projects
// and leave projects whose domain is unknown out of every group.
func TestBusinessDomainReportGroupsProjectUsageByDomain(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	prefix := fmt.Sprintf("bd-report-%d", time.Now().UnixNano())
	id := func(name string) string { return prefix + "-" + name }
	at := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	runs := []struct {
		project, status string
		tokens          int64
	}{
		{"p1", "succeeded", 10},
		{"p1", "succeeded", 20},
		{"p2", "failed", 40},
		{"p3", "succeeded", 80},
		{"p4", "succeeded", 160},
	}
	for i, r := range runs {
		runAt := at.Add(time.Duration(i) * time.Hour)
		issueID := id(fmt.Sprintf("issue-%d", i))
		run := domain.Run{ID: id(fmt.Sprintf("run-%d", i)), WorkflowID: id("wf"), Status: r.status, CreatedAt: runAt, UpdatedAt: runAt,
			ExecutionContext: domain.TelemetryContext{ProjectID: id(r.project), IssueID: issueID}, Usage: domain.TokenUsage{TotalTokens: r.tokens}}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	projects := []domain.Project{
		{ID: id("p1"), Name: "一号", BusinessDomainID: id("dom-a")},
		{ID: id("p2"), Name: "二号", BusinessDomainID: id("dom-a")},
		{ID: id("p3"), Name: "未归属"},
		{ID: id("p4"), Name: "已删业务域", BusinessDomainID: id("dom-gone")},
		{ID: id("p5"), Name: "无运行", BusinessDomainID: id("dom-z")},
	}
	domains := []domain.BusinessDomain{
		{ID: id("dom-z"), Name: "Z 业务", Active: false},
		{ID: id("dom-empty"), Name: "空业务", Active: true},
		{ID: id("dom-a"), Name: "A 业务", Active: true},
	}
	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		ids = append(ids, p.ID)
	}
	filter := domain.AnalyticsFilter{ProjectIDs: ids, From: at.Add(-time.Hour), To: at.Add(24 * time.Hour)}
	report, err := s.BusinessDomainReport(filter, projects, domains)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Projects) != len(projects) {
		t.Fatalf("every matching project must be listed, got %+v", report.Projects)
	}
	gotOrder := []string{}
	byID := map[string]domain.BusinessDomainUsage{}
	for _, g := range report.BusinessDomains {
		gotOrder = append(gotOrder, g.ID)
		byID[g.ID] = g
	}
	if want := []string{id("dom-z"), id("dom-a"), ""}; fmt.Sprint(gotOrder) != fmt.Sprint(want) {
		t.Fatalf("group order = %v, want %v (input order, empty groups dropped, 未分类 last)", gotOrder, want)
	}
	a := byID[id("dom-a")]
	if a.Name != "A 业务" || !a.Active || a.ProjectCount != 2 || a.Runs != 3 || a.Success != 2 || a.Failed != 1 || a.Tokens != 70 || a.UniqueIssues != 3 {
		t.Fatalf("dom-a must sum p1 and p2: %+v", a)
	}
	z := byID[id("dom-z")]
	if z.Name != "Z 业务" || z.Active || z.ProjectCount != 1 || z.Runs != 0 || z.Tokens != 0 || !z.CostKnown {
		t.Fatalf("a project without runs still counts towards its domain: %+v", z)
	}
	u := byID[""]
	if u.Name != "未分类" || !u.Active || u.ProjectCount != 1 || u.Runs != 1 || u.Tokens != 80 {
		t.Fatalf("project without a domain belongs to 未分类 only: %+v", u)
	}
	for _, g := range report.BusinessDomains {
		if g.Tokens >= 160 {
			t.Fatalf("project with an unknown domain must not be attributed to any group: %+v", g)
		}
	}

	only, err := s.BusinessDomainReport(domain.AnalyticsFilter{ProjectIDs: []string{id("p1")}, From: filter.From, To: filter.To}, projects, domains)
	if err != nil {
		t.Fatal(err)
	}
	if len(only.Projects) != 1 || len(only.BusinessDomains) != 1 || only.BusinessDomains[0].ID != id("dom-a") || only.BusinessDomains[0].ProjectCount != 1 || only.BusinessDomains[0].Tokens != 30 {
		t.Fatalf("filtered projects must not be counted: projects=%+v groups=%+v", only.Projects, only.BusinessDomains)
	}
}

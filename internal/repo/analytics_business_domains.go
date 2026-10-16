package repo

import (
	"agentflow/internal/domain"
)

// BusinessDomainReport reuses the underlying samples for rates and percentiles.
// It returns aggregates only; domain access does not grant run/log/user detail access.
func (r *AnalyticsRepo) BusinessDomainReport(filter domain.AnalyticsFilter, projects []domain.Project, domains []domain.BusinessDomain) (domain.BusinessDomainReport, error) {
	snap := r.Analytics(filter)
	out := domain.BusinessDomainReport{Attribution: "current", GeneratedAt: snap.Summary.GeneratedAt, From: filter.From, To: filter.To, Summary: snap.Summary, Delivery: snap.Delivery, Trend: snap.Trend, Projects: []domain.BusinessDomainProjectUsage{}, BusinessDomains: []domain.BusinessDomainUsage{}}
	groups := map[string]*domain.BusinessDomainUsage{}
	order := []string{}
	for _, d := range domains {
		groups[d.ID] = &domain.BusinessDomainUsage{ID: d.ID, Name: d.Name, Active: d.Active, CostKnown: true}
		order = append(order, d.ID)
	}
	groups[""] = &domain.BusinessDomainUsage{ID: "", Name: "未分类", Active: true, CostKnown: true}
	order = append(order, "")
	usage := map[string]domain.AnalyticsProjectUsage{}
	for _, u := range snap.ProjectUsage {
		usage[u.ProjectID] = u
	}
	projectDomains := map[string]string{}
	for _, p := range projects {
		if !filter.MatchesProject(p.ID) {
			continue
		}
		projectDomains[p.ID] = p.BusinessDomainID
		u, ok := usage[p.ID]
		if !ok {
			u = domain.AnalyticsProjectUsage{ProjectID: p.ID, CostKnown: true}
		}
		out.Projects = append(out.Projects, domain.BusinessDomainProjectUsage{AnalyticsProjectUsage: u, Name: p.Name, BusinessDomainID: p.BusinessDomainID})
		g := groups[p.BusinessDomainID]
		if g == nil {
			continue
		}
		g.ProjectCount++
		g.Runs += u.Runs
		g.Success += u.Success
		g.Failed += u.Failed
		g.Tokens += u.Tokens
		g.CostUSD += u.CostUSD
		g.UniqueIssues += u.UniqueIssues
		g.CostKnown = g.CostKnown && u.CostKnown
	}
	if err := r.addBusinessDeliveryCounts(snap.Deliveries, projectDomains, groups); err != nil {
		return domain.BusinessDomainReport{}, err
	}
	for _, id := range order {
		g := groups[id]
		if g.ProjectCount > 0 {
			out.BusinessDomains = append(out.BusinessDomains, *g)
		}
	}
	return out, nil
}

// Resolve only delivered issues, then attribute each delivery once across all query batches.
func (r *AnalyticsRepo) addBusinessDeliveryCounts(deliveries []domain.AnalyticsDelivery, projectDomains map[string]string, groups map[string]*domain.BusinessDomainUsage) error {
	ids := make([]string, 0, len(deliveries))
	seen := map[string]bool{}
	for _, d := range deliveries {
		if d.Delivered && d.IssueID != "" && !seen[d.IssueID] {
			seen[d.IssueID] = true
			ids = append(ids, d.IssueID)
		}
	}
	byIssue := make(map[string]string, len(ids))
	for start := 0; start < len(ids); start += 500 {
		var rows []issueRow
		if err := r.DB.Select([]string{"id", "project_id"}).Where("id IN ?", ids[start:min(start+500, len(ids))]).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			byIssue[row.ID] = row.ProjectID
		}
	}
	for _, d := range deliveries {
		if !d.Delivered {
			continue
		}
		projectID, found := byIssue[d.IssueID]
		if !found {
			continue
		}
		id, allowed := projectDomains[projectID]
		if allowed && groups[id] != nil {
			groups[id].Delivered++
		}
	}
	return nil
}

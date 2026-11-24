package repo

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

func (r *AnalyticsRepo) analyticsDashboard(filter domain.AnalyticsFilter, runs []domain.Run, issues map[string]domain.Issue, workflows map[string]domain.WorkflowDefinition, signals map[string]analyticsRunSignals, out *domain.AnalyticsSnapshot) error {
	d := out.Dashboard
	d.Trend = []domain.AnalyticsDashboardPoint{}
	d.HumanTrend = []domain.AnalyticsInterventionPoint{}
	d.HumanNodes = []domain.AnalyticsInterventionNode{}
	if filter.Origin == "external" {
		return nil
	}
	zone := analyticsWeekHourLocation(filter.TimeZone)
	if filter.Scope != "dashboard" {
		// Match the existing overview/activity chart buckets when embedded in
		// that response. The compact dashboard retains its requested time zone.
		zone = time.UTC
	}
	bucket := func(at time.Time) string { return analyticsTrendBucket(at.In(zone), filter.Grain) }
	points := map[string]*domain.AnalyticsDashboardPoint{}
	point := func(date string) *domain.AnalyticsDashboardPoint {
		if points[date] == nil {
			points[date] = &domain.AnalyticsDashboardPoint{Date: date}
		}
		return points[date]
	}
	human := map[string]*domain.AnalyticsInterventionPoint{}
	nodes := map[string]string{}
	byID := map[string]domain.Run{}
	eligible := map[string]bool{}
	affected := map[string]bool{}
	for _, run := range runs {
		byID[run.ID] = run
	}
	var durations []int64
	periodDurations := map[string][]int64{}
	coding := map[string]map[string]bool{}
	codingIssues := map[string]bool{}
	for _, run := range runs {
		issue := issues[strings.TrimSpace(run.ExecutionContext.IssueID)]
		if run.IsWorkspace() && issue.ID != "" && analyticsRunInWindow(run, filter) &&
			analyticsRunMatchesUser(run, filter.UserID, issue) && analyticsIssueMatchesSource(issue, filter.IssueSource) &&
			(!filter.RequirementsOnly || analyticsIsRequirementIssue(issue)) {
			date := bucket(run.CreatedAt)
			if coding[date] == nil {
				coding[date] = map[string]bool{}
			}
			coding[date][issue.ID], codingIssues[issue.ID] = true, true
		}
		if !analyticsSOPCohortRun(run, filter, issue) {
			continue
		}
		d.SOPRuns++
		eligible[run.ID] = true
		date := bucket(run.CreatedAt)
		point(date).SOPRuns++
		switch run.Status {
		case "succeeded":
			d.SucceededRuns++
			point(date).SucceededRuns++
		case "failed":
			d.FailedRuns++
			point(date).FailedRuns++
		}
		if run.Status == "succeeded" || run.Status == "failed" || run.Status == "cancelled" {
			if elapsed := runExecutionDuration(run); elapsed > 0 {
				durations = append(durations, elapsed)
				periodDurations[date] = append(periodDurations[date], elapsed)
			}
		}
	}
	// A child SOP's decisions belong to its top-level cohort, without adding a
	// second run to the denominator. Keep the originating node for drill-down.
	for _, run := range runs {
		root := run
		seen := map[string]bool{}
		for root.ExecutionContext.ParentRunID != "" && !seen[root.ID] {
			seen[root.ID] = true
			parent, exists := byID[root.ExecutionContext.ParentRunID]
			if !exists {
				break
			}
			root = parent
		}
		if !eligible[root.ID] || root.ExecutionContext.ParentRunID != "" || run.IsWorkspace() || run.HistoricalGroup() {
			continue
		}
		date := bucket(root.CreatedAt)
		signal := signals[run.ID]
		d.Performance.ToolCallsSucceeded += signal.toolSucceeded
		d.Performance.ToolCallsFailed += signal.toolFailed
		point(date).Performance.ToolCallsSucceeded += signal.toolSucceeded
		point(date).Performance.ToolCallsFailed += signal.toolFailed
		if signal.interventions > 0 {
			affected[root.ID] = true
		}
		for nodeID, counts := range signal.byNode {
			key := run.WorkflowID + "/" + nodeID
			name := nodeID
			if name == "" {
				name = "运行级决策"
			}
			workflow := run.WorkflowSnapshot
			if len(workflow.Nodes) == 0 {
				workflow = workflows[run.WorkflowID]
			}
			for _, node := range workflow.Nodes {
				if node.ID == nodeID && node.Name != "" {
					name = node.Name
					break
				}
			}
			if workflow.Name != "" {
				name = workflow.Name + " · " + name
			}
			nodes[key] = name
			index := date + "\x00" + key
			if human[index] == nil {
				human[index] = &domain.AnalyticsInterventionPoint{Date: date, Node: key}
			}
			p := human[index]
			p.Approvals += counts.Approvals
			p.Rejections += counts.Rejections
			p.Retries += counts.Retries
		}
	}
	d.HumanRuns = len(affected)
	for id := range affected {
		point(bucket(byID[id].CreatedAt)).HumanRuns++
	}
	d.P95ExecutionDurationMs = percentile(durations, 95)
	applyAnalyticsSOPDurations(&d.Performance, durations)
	for date, values := range periodDurations {
		applyAnalyticsSOPDurations(&point(date).Performance, values)
	}
	codingStates, err := r.analyticsCodingStates(codingIssues, issues)
	if err != nil {
		return err
	}
	for id := range codingIssues {
		addAnalyticsCodingOutcome(&d.Coding, codingStates[id])
	}
	for date, ids := range coding {
		for id := range ids {
			addAnalyticsCodingOutcome(&point(date).Coding, codingStates[id])
		}
	}
	if err := r.analyticsDashboardDeliveries(filter, issues, func(at time.Time, sop bool) {
		d.DeliveredIssues++
		p := point(bucket(at))
		if sop {
			d.SOPDeliveredIssues++
			p.SOPDelivered++
		} else {
			d.VibeDeliveredIssues++
			p.VibeDelivered++
		}
	}); err != nil {
		return err
	}
	for _, p := range points {
		d.Trend = append(d.Trend, *p)
	}
	for _, p := range human {
		d.HumanTrend = append(d.HumanTrend, *p)
	}
	for key, name := range nodes {
		d.HumanNodes = append(d.HumanNodes, domain.AnalyticsInterventionNode{Key: key, Name: name})
	}
	sort.Slice(d.Trend, func(i, j int) bool { return d.Trend[i].Date < d.Trend[j].Date })
	sort.Slice(d.HumanTrend, func(i, j int) bool {
		if d.HumanTrend[i].Date == d.HumanTrend[j].Date {
			return d.HumanTrend[i].Node < d.HumanTrend[j].Node
		}
		return d.HumanTrend[i].Date < d.HumanTrend[j].Date
	})
	sort.Slice(d.HumanNodes, func(i, j int) bool {
		if d.HumanNodes[i].Name == d.HumanNodes[j].Name {
			return d.HumanNodes[i].Key < d.HumanNodes[j].Key
		}
		return d.HumanNodes[i].Name < d.HumanNodes[j].Name
	})
	return nil
}

// Delivery has its own clock: a requirement is delivered when its last
// necessary PR reaches the default branch, and is counted once per issue.
// Only persisted MR and run headers are read; analytics never registers MRs.
func (r *AnalyticsRepo) analyticsDashboardDeliveries(filter domain.AnalyticsFilter, issues map[string]domain.Issue, visit func(time.Time, bool)) error {
	allTime := filter
	allTime.From, allTime.To = time.Time{}, time.Time{}
	mrs, _, err := r.mrMergeAnalytics(allTime, issues)
	if err != nil {
		return err
	}
	repositories := map[string]domain.Repository{}
	for _, repository := range r.catalog.Repositories() {
		repositories[strings.ToLower(strings.TrimSpace(repository.GongfengProjectID))] = repository
	}
	lastMerge := map[string]time.Time{}
	candidates := map[string]bool{}
	for _, mr := range mrs {
		repository := repositories[strings.ToLower(strings.TrimSpace(mr.MRProject))]
		if domain.ClassifyMRDeliveryState("merged", mr.TargetBranch, repository.DefaultBranch, repository.IntegrateBranch) != "merged" {
			continue
		}
		candidates[mr.IssueID] = true
		if mr.MergedAt.After(lastMerge[mr.IssueID]) {
			lastMerge[mr.IssueID] = mr.MergedAt
		}
	}
	states, err := r.analyticsCodingStates(candidates, issues)
	if err != nil {
		return err
	}
	ids := []string{}
	for id, at := range lastMerge {
		if state := states[id]; state.delivered && !state.missingMergeTime && analyticsActivityInWindow(at, filter) {
			ids = append(ids, id)
		}
	}
	for start := 0; start < len(ids); start += 500 {
		batch := ids[start:min(start+500, len(ids))]
		var rows []runRow
		snapshot := "json_object('collaborationMode', json_extract(snapshot, '$.collaborationMode')) AS snapshot"
		if r.isMySQL() {
			snapshot = "JSON_OBJECT('collaborationMode', JSON_UNQUOTE(JSON_EXTRACT(snapshot, '$.collaborationMode'))) AS snapshot"
		}
		// No nodes, artifacts, logs or telemetry are needed to identify the route.
		if err := r.DB.Select("id", "kind", "execution_context", snapshot, "created_at").
			Where(r.sqlExecutionContextJSONText("issueId")+" IN ?", batch).Find(&rows).Error; err != nil {
			return err
		}
		sop, workspace := map[string]bool{}, map[string]bool{}
		for _, row := range rows {
			var context domain.TelemetryContext
			if err := json.Unmarshal(row.ExecutionContext, &context); err != nil {
				return err
			}
			projectID := strings.TrimSpace(context.ProjectID)
			if projectID == "" {
				projectID = issues[context.IssueID].ProjectID
			}
			if !filter.MatchesProject(projectID) {
				continue
			}
			if context.ParentRunID != "" || row.Created == nil || row.Created.IsZero() || row.Created.After(lastMerge[context.IssueID]) {
				continue
			}
			if row.Kind == domain.RunKindWorkspace {
				workspace[context.IssueID] = true
			} else {
				var workflow domain.WorkflowDefinition
				if len(row.Snapshot) > 0 {
					if err := json.Unmarshal(row.Snapshot, &workflow); err != nil {
						return err
					}
				}
				if workflow.CollaborationMode != "group" {
					sop[context.IssueID] = true
				}
			}
		}
		for _, id := range batch {
			if sop[id] || workspace[id] {
				visit(lastMerge[id], sop[id])
			}
		}
	}
	return nil
}

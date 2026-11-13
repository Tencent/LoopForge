package repo

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

type businessDeliveryAccumulator struct {
	metrics        domain.BusinessDeliveryMetrics
	cycles         []int64
	executions     []int64
	mergeLatencies []int64
}

func (a *businessDeliveryAccumulator) complete(at time.Time, estimated bool, cycle, execution *int64, tokens int64) {
	a.metrics.CompletedIssues++
	if estimated {
		a.metrics.EstimatedCompletionIssues++
	}
	a.metrics.CompletedTokens += tokens
	if a.metrics.LatestCompletedAt == nil || at.After(*a.metrics.LatestCompletedAt) {
		value := at
		a.metrics.LatestCompletedAt = &value
	}
	if cycle != nil {
		a.cycles = append(a.cycles, *cycle)
	}
	if execution != nil {
		a.executions = append(a.executions, *execution)
	}
}

func (a *businessDeliveryAccumulator) result() domain.BusinessDeliveryMetrics {
	out := a.metrics
	out.CycleSamples = len(a.cycles)
	if out.CycleSamples > 0 {
		var total int64
		for _, value := range a.cycles {
			total += value
		}
		out.AverageCycleDurationMs = total / int64(out.CycleSamples)
		out.P50CycleDurationMs = percentile(a.cycles, 50)
		out.P95CycleDurationMs = percentile(a.cycles, 95)
	}
	if out.CompletedIssues > 0 {
		out.AverageTokensPerCompletedIssue = out.CompletedTokens / int64(out.CompletedIssues)
	}
	out.ExecutionSamples = len(a.executions)
	out.CompletionToMergeSamples = len(a.mergeLatencies)
	if len(a.mergeLatencies) > 0 {
		var total int64
		for _, value := range a.mergeLatencies {
			total += value
		}
		out.AverageCompletionToMergeMs = total / int64(len(a.mergeLatencies))
	}
	if out.ExecutionSamples > 0 {
		var total int64
		for _, value := range a.executions {
			total += value
		}
		out.AverageExecutionDurationMs = total / int64(out.ExecutionSamples)
	}
	return out
}

type businessIssueTiming struct {
	issue  domain.Issue
	start  time.Time
	latest domain.Run
	runs   []domain.Run
}

// businessIssueCompletion never uses UpdatedAt or DeliveryUpdatedAt: both can
// move on edits / MR polling. The last successful top-level Run is an explicitly
// estimated fallback for older completed issues, never for open or failed work.
func businessIssueCompletion(item businessIssueTiming) (time.Time, bool) {
	if !item.issue.Completed() {
		return time.Time{}, false
	}
	if !item.issue.CompletedAt.IsZero() {
		return item.issue.CompletedAt, false
	}
	var completedAt, reopenedAt time.Time
	for _, op := range item.issue.Operations {
		switch op.Action {
		case "complete", "close":
			if op.At.After(completedAt) {
				completedAt = op.At
			}
		case "reopen":
			if op.At.After(reopenedAt) {
				reopenedAt = op.At
			}
		}
	}
	if !completedAt.IsZero() && !completedAt.Before(reopenedAt) {
		return completedAt, false
	}
	run := item.latest
	if run.ID == "" || run.Status != "succeeded" || run.CreatedAt.IsZero() || run.UpdatedAt.IsZero() {
		return time.Time{}, false
	}
	end := runEndedAt(run, run.UpdatedAt)
	if run.Timing != nil {
		end = run.CreatedAt.Add(time.Duration(run.Timing.WallMs) * time.Millisecond)
	}
	if end.Before(run.CreatedAt) || end.Before(reopenedAt) {
		return time.Time{}, false
	}
	return end, true
}

// businessDeliveryIssues projects only lifecycle evidence. The response remains
// aggregate-only, and SQL is restricted to the already authorized project IDs.
func (r *AnalyticsRepo) businessDeliveryIssues(projectIDs []string) ([]domain.Issue, error) {
	if len(projectIDs) == 0 {
		return nil, nil
	}
	extract := func(path string) string {
		if r.isMySQL() {
			return "JSON_UNQUOTE(JSON_EXTRACT(payload, '$." + path + "'))"
		}
		return "json_extract(payload, '$." + path + "')"
	}
	type lifecycleRow struct {
		ID                     string
		ProjectID              string
		Status                 string
		Kind                   string
		Source                 string
		CompletedAt            string
		ClarificationStartedAt string
		Operations             string
	}
	var rows []lifecycleRow
	err := r.DB.Table("issues").Select("id", "project_id", "status", "kind", "source",
		extract("completedAt")+" AS completed_at",
		extract("clarification.startedAt")+" AS clarification_started_at",
		extract("operations")+" AS operations",
	).Where("project_id IN ?", projectIDs).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	issues := make([]domain.Issue, 0, len(rows))
	for _, row := range rows {
		issue := domain.Issue{ID: row.ID, ProjectID: row.ProjectID, Status: row.Status, Kind: row.Kind, Source: row.Source}
		issue.CompletedAt, _ = time.Parse(time.RFC3339Nano, row.CompletedAt)
		issue.Clarification.StartedAt, _ = time.Parse(time.RFC3339Nano, row.ClarificationStartedAt)
		if row.Operations != "" && row.Operations != "null" {
			if err := json.Unmarshal([]byte(row.Operations), &issue.Operations); err != nil {
				return nil, err
			}
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

func businessTimeInWindow(at time.Time, filter domain.AnalyticsFilter) bool {
	return !at.IsZero() && (filter.From.IsZero() || !at.Before(filter.From)) && (filter.To.IsZero() || !at.After(filter.To))
}

// businessExecutionDuration unions actual execution spans across all attempts
// and parallel Runs. SOP wrapper nodes are replaced by their child Run evidence
// so the child's approval/queue waits do not become parent execution time.
func businessExecutionDuration(item businessIssueTiming, completedAt time.Time, events map[string][]domain.RunEvent) *int64 {
	if item.start.IsZero() || completedAt.Before(item.start) {
		return nil
	}
	eligible := map[string]bool{}
	for _, run := range item.runs {
		if !run.CreatedAt.After(completedAt) {
			eligible[run.ID] = true
		}
	}
	var intervals []domain.RunExecutionInterval
	observed := false
	for _, run := range item.runs {
		if !eligible[run.ID] {
			continue
		}
		wrappers := map[string]bool{}
		nodes := map[string]*domain.NodeRun{}
		for id, node := range run.Nodes {
			if node != nil && node.ChildRunID != "" {
				if !eligible[node.ChildRunID] {
					return nil // incomplete child timing is not zero execution
				}
				wrappers[id] = true
			} else {
				nodes[id] = node
			}
		}
		run.Nodes = nodes
		filtered := make([]domain.RunEvent, 0, len(events[run.ID]))
		hasExecutionEvents := false
		for _, event := range events[run.ID] {
			if !wrappers[event.NodeID] {
				filtered = append(filtered, event)
				if event.Type == "node.started" && event.NodeID != "" && !event.Timestamp.IsZero() {
					hasExecutionEvents = true
				}
			}
		}
		if !hasExecutionEvents {
			if len(wrappers) > 0 && len(nodes) == 0 {
				continue
			}
			// Legacy node rows retain only their last attempt and can include
			// approval waits. They cannot establish a wait-free duration.
			return nil
		}
		spans, known := domain.RunExecutionIntervals(run, filtered, completedAt)
		if !known {
			if len(wrappers) > 0 && len(nodes) == 0 {
				continue // a pure SOP wrapper contributes only its children
			}
			return nil
		}
		observed = true
		for _, span := range spans {
			if span.StartedAt.Before(item.start) {
				span.StartedAt = item.start
			}
			if span.FinishedAt.After(completedAt) {
				span.FinishedAt = completedAt
			}
			if span.FinishedAt.After(span.StartedAt) {
				intervals = append(intervals, span)
			}
		}
	}
	if !observed {
		return nil
	}
	value := unionDuration(intervals).Milliseconds()
	return &value
}

// unionDuration returns the total length of the union of intervals: spans
// that overlap or touch are counted once. intervals is sorted in place.
func unionDuration(intervals []domain.RunExecutionInterval) time.Duration {
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].StartedAt.Before(intervals[j].StartedAt) })
	var total time.Duration
	for i := 0; i < len(intervals); {
		span := intervals[i]
		i++
		for i < len(intervals) && !intervals[i].StartedAt.After(span.FinishedAt) {
			if intervals[i].FinishedAt.After(span.FinishedAt) {
				span.FinishedAt = intervals[i].FinishedAt
			}
			i++
		}
		total += span.FinishedAt.Sub(span.StartedAt)
	}
	return total
}

// populateBusinessDeliveryMetrics 计算业务域交付指标。prefetchedRuns/prefetchedEvents
// 可复用调用方已经取回的同一批 run 摘要与计时事件（业务域报表路径），传 nil 则
// 自行查询，两种方式的入参与结果完全一致。
func (r *AnalyticsRepo) populateBusinessDeliveryMetrics(out *domain.BusinessDomainReport, filter domain.AnalyticsFilter, prefetchedRuns []domain.Run, prefetchedEvents map[string][]domain.RunEvent) error {
	location, err := time.LoadLocation(filter.TimeZone)
	if filter.TimeZone == "" {
		location, err = time.UTC, nil
	}
	if err != nil {
		return err
	}
	out.TimeZone = location.String()
	out.Grain = filter.Grain
	if out.Grain == "" {
		out.Grain = "day"
	}
	out.DeliveryTrend = []domain.BusinessDeliveryTrendPoint{}
	projectIDs := make([]string, 0, len(out.Projects))
	projects := map[string]*businessDeliveryAccumulator{}
	groups := map[string]*businessDeliveryAccumulator{}
	for _, p := range out.Projects {
		projectIDs = append(projectIDs, p.ProjectID)
		projects[p.ProjectID] = &businessDeliveryAccumulator{}
		groups[p.BusinessDomainID] = &businessDeliveryAccumulator{}
	}
	if len(projectIDs) == 0 {
		return nil
	}
	issues, err := r.businessDeliveryIssues(projectIDs)
	if err != nil {
		return err
	}
	items := map[string]*businessIssueTiming{}
	for _, issue := range issues {
		items[issue.ID] = &businessIssueTiming{issue: issue, start: issue.Clarification.StartedAt}
	}
	// The completion window must not truncate earlier attempts or their tokens.
	// RunSummariesFor omits heavy outputs/logs and retains legacy issue -> project
	// fallback. It also supplies terminal event timing for the historical fallback.
	runs := prefetchedRuns
	if runs == nil {
		runs, err = r.runs.RunSummariesFor(domain.AnalyticsFilter{ProjectIDs: projectIDs}, "")
		if err != nil {
			return err
		}
	}
	timingEvents := prefetchedEvents
	if timingEvents == nil {
		runIDs := make([]string, 0, len(runs))
		for _, run := range runs {
			runIDs = append(runIDs, run.ID)
		}
		timingEvents, err = r.runs.runTimingEvents(runIDs)
		if err != nil {
			return err
		}
	}
	for _, run := range runs {
		item := items[run.ExecutionContext.IssueID]
		if item == nil || domain.RunProjectID(run, item.issue.ProjectID) != item.issue.ProjectID {
			continue
		}
		item.runs = append(item.runs, run)
		if run.ExecutionContext.ParentRunID != "" {
			continue
		}
		if !run.CreatedAt.IsZero() && (item.start.IsZero() || run.CreatedAt.Before(item.start)) {
			item.start = run.CreatedAt
		}
		if item.latest.ID == "" || run.CreatedAt.After(item.latest.CreatedAt) || (run.CreatedAt.Equal(item.latest.CreatedAt) && run.ID > item.latest.ID) {
			item.latest = run
		}
	}
	projectDomains := map[string]string{}
	for _, p := range out.Projects {
		projectDomains[p.ProjectID] = p.BusinessDomainID
	}
	total := &businessDeliveryAccumulator{}
	trends := map[string]*businessDeliveryAccumulator{}
	trendAt := func(at time.Time) *businessDeliveryAccumulator {
		key := analyticsTrendBucket(at.In(location), filter.Grain)
		if trends[key] == nil {
			trends[key] = &businessDeliveryAccumulator{}
		}
		return trends[key]
	}
	for _, item := range items {
		p := projects[item.issue.ProjectID]
		g := groups[projectDomains[item.issue.ProjectID]]
		if !item.issue.Completed() {
			continue
		}
		at, estimated := businessIssueCompletion(*item)
		if at.IsZero() {
			for _, acc := range []*businessDeliveryAccumulator{total, p, g} {
				acc.metrics.CompletionTimeMissingIssues++
			}
			continue
		}
		if !businessTimeInWindow(at, filter) {
			continue
		}
		var cycle *int64
		if !item.start.IsZero() && !at.Before(item.start) {
			value := at.Sub(item.start).Milliseconds()
			cycle = &value
		}
		var tokens int64
		for _, run := range item.runs {
			if !run.CreatedAt.After(at) {
				tokens += run.Usage.TotalTokens
			}
		}
		execution := businessExecutionDuration(*item, at, timingEvents)
		for _, acc := range []*businessDeliveryAccumulator{total, p, g, trendAt(at)} {
			acc.complete(at, estimated, cycle, execution, tokens)
		}
	}
	var mrs []mrDeliveryStateRow
	q := r.DB.Table("mr_delivery_states AS mr").Select("mr.*").Joins("JOIN issues AS issue ON issue.id = mr.issue_id").
		Where("issue.project_id IN ? AND mr.state = ? AND mr.issue_id <> ?", projectIDs, "merged", domain.BaselinePromoteIssueID)
	if err := q.Find(&mrs).Error; err != nil {
		return err
	}
	for _, mr := range mrs {
		item := items[mr.IssueID]
		if item == nil {
			continue
		}
		accs := []*businessDeliveryAccumulator{total, projects[item.issue.ProjectID], groups[projectDomains[item.issue.ProjectID]]}
		if mr.MergedAt == nil || mr.MergedAt.IsZero() {
			for _, acc := range accs {
				acc.metrics.MergedMRsMissingTime++
			}
			continue
		}
		if businessTimeInWindow(*mr.MergedAt, filter) {
			for _, acc := range append(accs, trendAt(*mr.MergedAt)) {
				acc.metrics.MergedMRs++
			}
		}
	}
	statesByIssue := map[string]map[domain.MRDeliveryKey]domain.MRDeliveryState{}
	candidates := map[string]bool{}
	for _, mr := range mrs {
		if statesByIssue[mr.IssueID] == nil {
			statesByIssue[mr.IssueID] = map[domain.MRDeliveryKey]domain.MRDeliveryState{}
		}
		statesByIssue[mr.IssueID][domain.MRDeliveryKey{Project: mr.MRProject, IID: mr.MRIID}] = mr.toDomain()
		if mr.MergedAt != nil && businessTimeInWindow(*mr.MergedAt, filter) {
			candidates[mr.IssueID] = true
		}
	}
	candidateRuns := []domain.Run{}
	for _, run := range runs {
		if candidates[run.ExecutionContext.IssueID] {
			candidateRuns = append(candidateRuns, run)
		}
	}
	refs, err := r.businessDeliveryMRs(candidateRuns)
	if err != nil {
		return err
	}
	for _, item := range items {
		if !candidates[item.issue.ID] {
			continue
		}
		latencies := businessCompletionToMerge(*item, refs, statesByIssue[item.issue.ID], timingEvents, filter)
		accs := []*businessDeliveryAccumulator{total, projects[item.issue.ProjectID], groups[projectDomains[item.issue.ProjectID]]}
		for _, acc := range accs {
			missing, before := false, false
			for _, latency := range latencies {
				switch {
				case latency.missing:
					acc.metrics.CompletionToMergeMissingMRs++
					missing = true
				case latency.beforeCompletion:
					acc.metrics.MergedBeforeCompletionMRs++
					before = true
				default:
					acc.mergeLatencies = append(acc.mergeLatencies, latency.duration)
				}
			}
			if missing {
				acc.metrics.CompletionToMergeMissingIssues++
			}
			if before {
				acc.metrics.MergedBeforeCompletionIssues++
			}
		}
	}
	out.DeliveryMetrics = total.result()
	for i := range out.Projects {
		out.Projects[i].DeliveryMetrics = projects[out.Projects[i].ProjectID].result()
	}
	for i := range out.BusinessDomains {
		out.BusinessDomains[i].DeliveryMetrics = groups[out.BusinessDomains[i].ID].result()
	}
	for date, acc := range trends {
		metrics := acc.result()
		out.DeliveryTrend = append(out.DeliveryTrend, domain.BusinessDeliveryTrendPoint{Date: date, CompletedIssues: metrics.CompletedIssues, MergedMRs: metrics.MergedMRs, CycleSamples: metrics.CycleSamples, AverageCycleDurationMs: metrics.AverageCycleDurationMs, ExecutionSamples: metrics.ExecutionSamples, AverageExecutionDurationMs: metrics.AverageExecutionDurationMs})
	}
	sort.Slice(out.DeliveryTrend, func(i, j int) bool { return strings.Compare(out.DeliveryTrend[i].Date, out.DeliveryTrend[j].Date) < 0 })
	return nil
}

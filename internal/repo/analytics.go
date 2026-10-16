package repo

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
)

type analyticsAccumulator struct {
	item          domain.AnalyticsBreakdown
	durations     []int64
	duration      int64
	execDurations []int64
	execDuration  int64

	inputTokens      int64
	outputTokens     int64
	cacheReadTokens  int64
	cacheWriteTokens int64
	toolCalls        int
	nodeRuns         int
	retriedNodes     int
	reworkNodes      int
	models           map[string]int
	versions         map[string]int
	nodeTypes        map[string]int
	nodeCount        int
	issueIDs         map[string]struct{}
}

type analyticsVersionAccumulator struct {
	item          domain.AnalyticsWorkflowVersion
	durations     []int64
	duration      int64
	execDurations []int64
	execDuration  int64
}

type analyticsUserAccumulator struct {
	item domain.AnalyticsUserUsage
}

type analyticsProjectAccumulator struct {
	item   domain.AnalyticsProjectUsage
	issues map[string]struct{}
}

type analyticsRunSignals struct {
	toolSucceeded int
	toolFailed    int
	interventions int
}

// Analytics returns a read-only, vendor-neutral operational view across runs.
func (r *AnalyticsRepo) Analytics(filter domain.AnalyticsFilter) domain.AnalyticsSnapshot {
	overview := filter.Scope == "overview"
	now := time.Now().UTC()
	out := domain.AnalyticsSnapshot{
		Filter:   filter,
		Summary:  domain.AnalyticsSummary{GeneratedAt: now},
		Coverage: domain.AnalyticsCoverage{AdapterEvents: map[string]int{}, LogLevels: map[string]int{}},
	}
	trends := map[string]*domain.AnalyticsTrendPoint{}
	trendIssues := map[string]map[string]struct{}{}
	workflows, agents, executors, models, nodes, categories, sources := map[string]*analyticsAccumulator{}, map[string]*analyticsAccumulator{}, map[string]*analyticsAccumulator{}, map[string]*analyticsAccumulator{}, map[string]*analyticsAccumulator{}, map[string]*analyticsAccumulator{}, map[string]*analyticsAccumulator{}
	versions := map[string]*analyticsVersionAccumulator{}
	users := map[string]*analyticsUserAccumulator{}
	projects := map[string]*analyticsProjectAccumulator{}
	failures := map[string]*domain.AnalyticsFailure{}
	var terminalDurations, terminalExecDurations []int64
	var terminalDuration, terminalExecDuration, terminalWaitDuration int64
	terminal, firstPassSucceeded := 0, 0
	// 需求维度：每个需求的执行次数/失败次数/节点求和耗时。
	issueRuns, issueFailures, issueExec := map[string]int{}, map[string]int{}, map[string]int64{}
	// 成本识别与缓存命中率的累计基数。
	costKnownRuns, cacheReadTotal, cacheDenominator := 0, int64(0), int64(0)
	var includedRunIDs []string
	runTraceIndex := map[string]int{}
	var agentList []domain.AgentDefinition
	var executorList []domain.ExecutorDefinition
	var workflowList []domain.WorkflowDefinition
	var issueList []domain.Issue
	var runList []domain.Run
	if overview {
		// 概览用轻量需求列 + 当前 SOP 目录，避免拉全量 payload / 每条 Run 的冻结快照。
		_ = parallel(
			func() error { r, _ := r.runs.RunSummariesFor(filter, ""); runList = r; return nil },
			func() error { issueList = r.issues.IssueAnalyticsViews(); return nil },
			func() error { workflowList = r.catalog.Workflows(); return nil },
		)
	} else {
		_ = parallel(
			func() error { agentList = r.catalog.Agents(); return nil },
			func() error { executorList = r.catalog.Executors(); return nil },
			func() error { workflowList = r.catalog.Workflows(); return nil },
			func() error { issueList = r.issues.Issues(); return nil },
			func() error { r, _ := r.runs.RunSummariesFor(filter, ""); runList = r; return nil },
		)
	}

	agentDefs := map[string]domain.AgentDefinition{}
	for _, agent := range agentList {
		agentDefs[agent.ID] = agent
	}
	executorDefs := map[string]domain.ExecutorDefinition{}
	for _, executor := range executorList {
		executorDefs[executor.ID] = executor
	}
	workflowDefs := map[string]domain.WorkflowDefinition{}
	for _, workflow := range workflowList {
		workflowDefs[workflow.ID] = workflow
	}
	issuesByID := map[string]domain.Issue{}
	for _, issue := range issueList {
		issuesByID[issue.ID] = issue
	}
	// RunSummaries intentionally omits workflow snapshots on MySQL list paths.
	// Full scans need the frozen name/version/category/node bindings; overview
	// uses the live workflow catalog instead so the page does not hydrate every snapshot.
	if r.DB != nil && !overview {
		runIDs := make([]string, 0, len(runList))
		for _, run := range runList {
			if filter.WorkflowID != "" && run.WorkflowID != filter.WorkflowID {
				continue
			}
			if !analyticsRunInWindow(run, filter) || (filter.UserID != "" && !analyticsRunMatchesUser(run, filter.UserID, issuesByID[strings.TrimSpace(run.ExecutionContext.IssueID)])) {
				continue
			}
			issueID := strings.TrimSpace(run.ExecutionContext.IssueID)
			if filter.IssueSource != "" && !analyticsIssueMatchesSource(issuesByID[issueID], filter.IssueSource) {
				continue
			}
			if filter.RequirementsOnly && !analyticsIsRequirementIssue(issuesByID[issueID]) {
				continue
			}
			runIDs = append(runIDs, run.ID)
		}
		if snapshots, err := queryAnalyticsWorkflowSnapshots(r.DB, runIDs); err == nil {
			for i := range runList {
				if snapshot, ok := snapshots[runList[i].ID]; ok {
					runList[i].WorkflowSnapshot = snapshot
				}
			}
		}
	}

	for _, run := range runList {
		if filter.WorkflowID != "" && run.WorkflowID != filter.WorkflowID {
			continue
		}
		if !analyticsRunInWindow(run, filter) {
			continue
		}
		issueID := strings.TrimSpace(run.ExecutionContext.IssueID)
		if filter.UserID != "" && !analyticsRunMatchesUser(run, filter.UserID, issuesByID[issueID]) {
			continue
		}
		if filter.IssueSource != "" && !analyticsIssueMatchesSource(issuesByID[issueID], filter.IssueSource) {
			continue
		}
		if filter.RequirementsOnly && !analyticsIsRequirementIssue(issuesByID[issueID]) {
			continue
		}
		includedRunIDs = append(includedRunIDs, run.ID)

		out.Summary.TotalRuns++
		out.Summary.TotalTokens += run.Usage.TotalTokens
		out.Summary.TotalCostUSD += run.Usage.CostUSD
		out.Summary.InputTokens += run.Usage.InputTokens
		out.Summary.OutputTokens += run.Usage.OutputTokens
		out.Summary.CacheReadTokens += run.Usage.CacheReadTokens
		out.Summary.CacheWriteTokens += run.Usage.CacheWriteTokens
		out.Summary.ReasoningTokens += run.Usage.ReasoningTokens
		cacheReadTotal += run.Usage.CacheReadTokens
		cacheDenominator += run.Usage.InputTokens + run.Usage.CacheReadTokens
		if usageCostKnown(run.Usage) {
			costKnownRuns++
			out.Summary.CostReportedUSD += run.Usage.CostUSD
		} else {
			out.Summary.CostUnknownRuns++
		}
		out.Summary.ToolCalls += run.ToolCalls
		out.Summary.Artifacts += runArtifactCount(run)
		out.Summary.TelemetryEvents += int(run.TelemetrySequence)
		if n := len(run.Telemetry); n > int(run.TelemetrySequence) {
			out.Summary.TelemetryEvents += n - int(run.TelemetrySequence)
		}
		out.Summary.LogRecords += int(run.LogSequence)
		if n := len(run.Logs); n > int(run.LogSequence) {
			out.Summary.LogRecords += n - int(run.LogSequence)
		}
		if run.TelemetrySequence > 0 || len(run.Telemetry) > 0 {
			out.Coverage.RunsWithTelemetry++
		}
		if run.LogSequence > 0 || len(run.Logs) > 0 {
			out.Coverage.RunsWithLogs++
		}

		reworks := runReworks(run)
		out.Summary.TotalReworks += reworks
		if reworks > 0 {
			out.Summary.ReworkedRuns++
		}
		duration, complete := analyticsRunDuration(run, now)
		// 执行耗时为 SOP 节点耗时之和，不含审批/排队/恢复等待。
		execDuration := runExecutionDuration(run)
		if complete {
			terminal++
			terminalDuration += duration
			terminalDurations = append(terminalDurations, duration)
			terminalExecDuration += execDuration
			terminalExecDurations = append(terminalExecDurations, execDuration)
			terminalWaitDuration += runWaitDuration(run, now)
		}
		switch run.Status {
		case "running":
			out.Summary.ActiveRuns++
		case "waiting":
			out.Summary.WaitingRuns++
		case "succeeded":
			out.Summary.SucceededRuns++
			if reworks == 0 {
				firstPassSucceeded++
			}
		case "failed":
			out.Summary.FailedRuns++
			if issueID != "" {
				issueFailures[issueID]++
			}
		case "cancelled":
			out.Summary.CancelledRuns++
		}
		issue := issuesByID[issueID]
		traceID := strings.TrimSpace(run.ExecutionContext.TraceID)
		if traceID == "" {
			traceID = run.ID
		}
		projectID := domain.RunProjectID(run, issue.ProjectID)
		errorNodeID, errorMessage := analyticsRunError(run)
		trace := domain.AnalyticsRunTrace{
			ProjectID: projectID, IssueID: issueID, IssueSource: analyticsIssueSource(issue), WorkflowTaskID: run.ExecutionContext.TaskID,
			RunID: run.ID, TraceID: traceID, Status: run.Status, DurationMs: duration,
			Tokens: run.Usage.TotalTokens, CostUSD: run.Usage.CostUSD, CostKnown: usageCostKnown(run.Usage),
			ToolCalls: run.ToolCalls, ErrorNodeID: errorNodeID, ErrorMessage: errorMessage,
			CreatedAt: run.CreatedAt,
		}
		if tapdID := issue.RequirementTAPDID(); tapdID != "" {
			trace.ExternalID = tapdID
		}
		if issue.ExternalRef != nil {
			trace.ExternalURL = issue.ExternalRef.ExternalURL
		}
		if complete {
			trace.FinishedAt = runEndedAt(run, now)
		}
		runTraceIndex[run.ID] = len(out.RunTraces)
		out.RunTraces = append(out.RunTraces, trace)
		if issueID != "" {
			issueRuns[issueID]++
			issueExec[issueID] += execDuration
			accumulateIssueSource(sources, analyticsIssueSource(issuesByID[issueID]), run, execDuration)
		}

		workflow := run.WorkflowSnapshot
		if workflow.ID == "" {
			workflow = workflowDefs[run.WorkflowID]
		}
		workflowName := workflow.Name
		if workflowName == "" {
			workflowName = run.WorkflowID
		}
		category := analyticsWorkflowCategory(workflow, workflowDefs[run.WorkflowID])
		accumulateBreakdown(workflows, run.WorkflowID, workflowName, run, reworks, duration, complete, execDuration)
		if acc := workflows[run.WorkflowID]; acc != nil {
			acc.item.Category = category
			acc.nodeCount = len(workflow.Nodes)
			acc.nodeTypes = analyticsNodeTypes(workflow.Nodes)
		}
		accumulateVersion(versions, run.WorkflowID, workflowName, workflow.Version, workflowDefs[run.WorkflowID].Version, run, duration, complete, execDuration)
		accumulateBreakdown(categories, category, "", run, reworks, duration, complete, execDuration)
		if acc := categories[category]; acc != nil && issueID != "" {
			if acc.issueIDs == nil {
				acc.issueIDs = map[string]struct{}{}
			}
			if run.Status == "succeeded" {
				acc.issueIDs[issueID] = struct{}{}
			}
		}
		// 趋势与每用户每日用量在概览态同样输出，节点级拆解仍只在完整扫描时计算。
		eventAt := run.CreatedAt
		if complete {
			eventAt = runEndedAt(run, now)
		}
		eventAt = eventAt.UTC()
		day := eventAt.Format("2006-01-02")
		bucket := analyticsTrendBucket(eventAt, filter.Grain)
		point := trends[bucket]
		if point == nil {
			point = &domain.AnalyticsTrendPoint{Date: bucket}
			trends[bucket] = point
		}
		point.Runs++
		point.Reworks += reworks
		point.Tokens += run.Usage.TotalTokens
		point.CostUSD += run.Usage.CostUSD
		point.InputTokens += run.Usage.InputTokens
		point.OutputTokens += run.Usage.OutputTokens
		point.CacheReadTokens += run.Usage.CacheReadTokens
		point.CacheWriteTokens += run.Usage.CacheWriteTokens
		point.ExecutionDurationMs += execDuration
		if complete {
			point.DurationMs += duration
		}
		if run.Status == "succeeded" {
			point.Succeeded++
		}
		if run.Status == "failed" {
			point.Failed++
		}
		if issueID != "" {
			if trendIssues[bucket] == nil {
				trendIssues[bucket] = map[string]struct{}{}
			}
			trendIssues[bucket][issueID] = struct{}{}
		}
		accumulateUserUsage(users, analyticsRunUserID(run, issuesByID[issueID]), day, run, duration, complete, execDuration)
		accumulateProjectUsage(projects, domain.RunProjectID(run, issuesByID[issueID].ProjectID), issueID, run)
		definition := map[string]domain.WorkflowNode{}
		for _, node := range workflow.Nodes {
			definition[node.ID] = node
		}
		for nodeID, nodeRun := range run.Nodes {
			if nodeRun == nil {
				continue
			}
			node := definition[nodeID]
			nodeDuration := int64(0)
			nodeComplete := !nodeRun.StartedAt.IsZero() && !nodeRun.FinishedAt.IsZero()
			if nodeComplete {
				nodeDuration = nodeRun.FinishedAt.Sub(nodeRun.StartedAt).Milliseconds()
			}
			key := run.WorkflowID + "/" + nodeID
			name := node.Name
			if name == "" {
				name = nodeID
			}
			accumulateNode(nodes, key, name, nodeRun, nodeDuration, nodeComplete)
			model := nodeRun.Model
			if model == "" && node.ExecutorID != "" {
				model = executorDefs[node.ExecutorID].Model
			}
			if model != "" {
				accumulateNode(models, model, model, nodeRun, nodeDuration, nodeComplete)
			}
			if nodeRun.Error != "" {
				code := normalizeFailure(nodeRun.Error)
				failureKey := nodeID + "\x00" + code
				if failures[failureKey] == nil {
					failures[failureKey] = &domain.AnalyticsFailure{Code: code, NodeID: nodeID}
				}
				failures[failureKey].Count++
			}
			if !overview {
				if node.AgentID != "" {
					accumulateNode(agents, node.AgentID, agentName(agentDefs, node.AgentID), nodeRun, nodeDuration, nodeComplete)
				}
				if node.ExecutorID != "" {
					accumulateNode(executors, node.ExecutorID, executorName(executorDefs, node.ExecutorID), nodeRun, nodeDuration, nodeComplete)
				}
			}
		}
	}
	// Tool outcome and human-decision facts live in append-only event tables,
	// not in Run summary columns. Load only matching tool-finish telemetry and
	// compact control events, then join them back to each auditable Run row.
	signals := r.analyticsRunSignals(includedRunIDs)
	for runID, signal := range signals {
		index, ok := runTraceIndex[runID]
		if !ok {
			continue
		}
		trace := &out.RunTraces[index]
		trace.ToolCallsSucceeded = signal.toolSucceeded
		trace.ToolCallsFailed = signal.toolFailed
		trace.HumanInterventions = signal.interventions
		out.Demo.ToolCallsSucceeded += signal.toolSucceeded
		out.Demo.ToolCallsFailed += signal.toolFailed
		out.Demo.HumanInterventions += signal.interventions
	}
	out.Demo.ToolCallsObserved = out.Demo.ToolCallsSucceeded + out.Demo.ToolCallsFailed
	if out.Demo.ToolCallsObserved > 0 {
		out.Demo.ToolSuccessRate = float64(out.Demo.ToolCallsSucceeded) * 100 / float64(out.Demo.ToolCallsObserved)
	}
	for _, trace := range out.RunTraces {
		if trace.Status == "succeeded" && trace.HumanInterventions == 0 {
			out.Demo.AutomatedSucceededRuns++
		}
	}
	if out.Summary.TotalRuns > 0 {
		out.Demo.AverageTokensPerRun = float64(out.Summary.TotalTokens) / float64(out.Summary.TotalRuns)
		out.Demo.AverageHumanInterventions = float64(out.Demo.HumanInterventions) / float64(out.Summary.TotalRuns)
	}
	if out.Summary.SucceededRuns > 0 {
		out.Demo.AutomationRate = float64(out.Demo.AutomatedSucceededRuns) * 100 / float64(out.Summary.SucceededRuns)
	}
	sort.Slice(out.RunTraces, func(i, j int) bool {
		if out.RunTraces[i].CreatedAt.Equal(out.RunTraces[j].CreatedAt) {
			return out.RunTraces[i].RunID > out.RunTraces[j].RunID
		}
		return out.RunTraces[i].CreatedAt.After(out.RunTraces[j].CreatedAt)
	})
	// Coverage counts adapter/level distributions over the included runs. On
	// MySQL the generated columns make this an indexed GROUP BY; other
	// dialects (tests) compute the same numbers from the payloads.
	if !overview && len(includedRunIDs) > 0 {
		if r.isMySQL() {
			if adapters, levels, err := queryAnalyticsCoverage(r.DB, includedRunIDs); err == nil {
				out.Coverage.AdapterEvents = adapters
				out.Coverage.LogLevels = levels
			}
		} else {
			r.computeAnalyticsCoverage(includedRunIDs, &out.Coverage)
		}
	}

	if terminal > 0 {
		out.Summary.CompletionRate = float64(out.Summary.SucceededRuns) * 100 / float64(terminal)
		out.Summary.FirstPassRate = float64(firstPassSucceeded) * 100 / float64(terminal)
		out.Summary.AverageDurationMs = terminalDuration / int64(terminal)
		out.Summary.CancelRate = float64(out.Summary.CancelledRuns) * 100 / float64(terminal)
		out.Summary.ExecutionAverageDurationMs = terminalExecDuration / int64(terminal)
	}
	if attempts := out.Summary.SucceededRuns + out.Summary.FailedRuns; attempts > 0 {
		// 成功率排除取消的执行，取消率单独成列，避免"取消"污染成功率分母。
		out.Summary.SuccessRate = float64(out.Summary.SucceededRuns) * 100 / float64(attempts)
	}
	if cacheDenominator > 0 {
		out.Summary.CacheHitRate = float64(cacheReadTotal) * 100 / float64(cacheDenominator)
	}
	if out.Summary.TotalRuns > 0 {
		out.Summary.CostCoveragePct = float64(costKnownRuns) * 100 / float64(out.Summary.TotalRuns)
	}
	if terminal > 0 {
		out.Summary.WaitAverageDurationMs = terminalWaitDuration / int64(terminal)
	}
	if issues := len(issueRuns); issues > 0 {
		out.Summary.UniqueIssues = issues
		out.Summary.RunsPerIssue = float64(sumIssueCounts(issueRuns)) / float64(issues)
		out.Summary.FailuresPerIssue = float64(sumIssueCounts(issueFailures)) / float64(issues)
	}
	out.Summary.P50DurationMs = percentile(terminalDurations, 50)
	out.Summary.P95DurationMs = percentile(terminalDurations, 95)
	out.Summary.P99DurationMs = percentile(terminalDurations, 99)
	out.Summary.ExecutionP50DurationMs = percentile(terminalExecDurations, 50)
	out.Summary.ExecutionP95DurationMs = percentile(terminalExecDurations, 95)
	out.Summary.ExecutionP99DurationMs = percentile(terminalExecDurations, 99)
	if execValues := issueExecValues(issueExec); len(execValues) > 0 {
		out.Summary.IssueP50ExecutionDurationMs = percentile(execValues, 50)
		out.Summary.IssueP95ExecutionDurationMs = percentile(execValues, 95)
	}
	// 轻量维度（SOP、模型、失败原因、用户每日用量）在概览态同样输出，
	// Agent/CLI 与交付明细仍只在完整扫描时计算。
	out.Workflows = finalizeBreakdowns(workflows)
	out.Categories = finalizeBreakdowns(categories)
	out.WorkflowVersions = finalizeWorkflowVersions(versions)
	out.IssueSources = finalizeBreakdowns(sources)
	out.UserUsage = finalizeUserUsage(users)
	out.ProjectUsage = finalizeProjectUsage(projects)
	out.Models = finalizeBreakdowns(models)
	out.Nodes = finalizeBreakdowns(nodes)
	applyBreakdownShares(out.Workflows, out.Summary.TotalTokens, out.Summary.TotalCostUSD)
	applyBreakdownShares(out.Categories, out.Summary.TotalTokens, out.Summary.TotalCostUSD)
	applyBreakdownShares(out.IssueSources, out.Summary.TotalTokens, out.Summary.TotalCostUSD)
	applyBreakdownShares(out.Models, out.Summary.TotalTokens, out.Summary.TotalCostUSD)
	applyBreakdownShares(out.Nodes, out.Summary.TotalTokens, out.Summary.TotalCostUSD)
	for _, failure := range failures {
		out.Failures = append(out.Failures, *failure)
	}
	sort.Slice(out.Failures, func(i, j int) bool { return out.Failures[i].Count > out.Failures[j].Count })
	for _, point := range trends {
		if ids := trendIssues[point.Date]; ids != nil {
			point.Issues = len(ids)
		}
		out.Trend = append(out.Trend, *point)
	}
	sort.Slice(out.Trend, func(i, j int) bool { return out.Trend[i].Date < out.Trend[j].Date })
	if overview {
		out.Summary.InProgressIssues = countInProgressIssues(issueList, filter)
		return out
	}
	out.Agents = finalizeBreakdowns(agents)
	out.Executors = finalizeBreakdowns(executors)
	applyBreakdownShares(out.Agents, out.Summary.TotalTokens, out.Summary.TotalCostUSD)
	applyBreakdownShares(out.Executors, out.Summary.TotalTokens, out.Summary.TotalCostUSD)
	out.Deliveries = r.delivery.buildDeliveries(filter, now, runList, workflowDefs, issuesByID)
	out.Delivery = domain.SummarizeDeliveries(out.Deliveries)
	applyWeeklyThroughput(&out.Summary, out.Deliveries, now)
	out.Summary.InProgressIssues = countInProgressIssues(issueList, filter)
	return out
}

// analyticsHumanEventTypes are explicit human decisions. run.paused / run.resumed
// are excluded: they fire on user pause, but also on lease takeover and service
// recovery, and a single approval often emits pause+resume+gate together.
var analyticsHumanEventTypes = []string{
	"gate.approved",
	"node.passed",
	"gate.rejected",
	"node.retry_requested",
	"agent.turns.declined",
}

// analyticsRunSignals reads only the append-only facts needed by the Loop
// demo. It deliberately avoids hydrating logs, output, artifacts, and ordinary
// progress telemetry into the analytics request.
func (r *AnalyticsRepo) analyticsRunSignals(runIDs []string) map[string]analyticsRunSignals {
	out := map[string]analyticsRunSignals{}
	for start := 0; start < len(runIDs); start += 500 {
		batch := runIDs[start:min(start+500, len(runIDs))]
		r.accumulateToolSignals(out, batch)

		var eventRows []runEventRow
		if err := r.DB.Select("run_id", "type").Where("run_id IN ? AND type IN ?", batch, analyticsHumanEventTypes).Find(&eventRows).Error; err == nil {
			for _, row := range eventRows {
				signal := out[row.RunID]
				signal.interventions++
				out[row.RunID] = signal
			}
		}
	}
	return out
}

func (r *AnalyticsRepo) accumulateToolSignals(out map[string]analyticsRunSignals, batch []string) {
	if len(batch) == 0 || r.DB == nil {
		return
	}
	type toolRow struct {
		RunID     string `gorm:"column:run_id"`
		Succeeded int    `gorm:"column:succeeded"`
		Failed    int    `gorm:"column:failed"`
	}
	successExpr := "CASE WHEN json_extract(payload, '$.tool.success') IS NOT NULL THEN json_extract(payload, '$.tool.success') ELSE (CASE WHEN lower(coalesce(json_extract(payload, '$.level'), '')) = 'error' THEN 0 ELSE 1 END) END"
	filter := "json_extract(payload, '$.category') = ? AND json_extract(payload, '$.name') = ? AND json_extract(payload, '$.phase') = ?"
	if r.isMySQL() {
		successExpr = "CASE WHEN JSON_EXTRACT(payload, '$.tool.success') IS NOT NULL THEN JSON_EXTRACT(payload, '$.tool.success') ELSE (LOWER(COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.level')), '')) <> 'error') END"
		filter = "JSON_UNQUOTE(JSON_EXTRACT(payload, '$.category')) = ? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.name')) = ? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.phase')) = ?"
	}
	var rows []toolRow
	if err := r.DB.Table("run_telemetry").
		Select("run_id, SUM("+successExpr+") AS succeeded, SUM(1-("+successExpr+")) AS failed").
		Where("run_id IN ? AND "+filter, batch, "tool", "tool.call", "finish").
		Group("run_id").
		Scan(&rows).Error; err != nil {
		return
	}
	for _, row := range rows {
		signal := out[row.RunID]
		signal.toolSucceeded += row.Succeeded
		signal.toolFailed += row.Failed
		out[row.RunID] = signal
	}
}

func analyticsRunError(run domain.Run) (string, string) {
	ids := make([]string, 0, len(run.Nodes))
	for id, node := range run.Nodes {
		if node != nil && strings.TrimSpace(node.Error) != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return "", ""
	}
	sort.Strings(ids)
	node := run.Nodes[ids[0]]
	return ids[0], normalizeFailure(node.Error)
}

// computeAnalyticsCoverage derives adapter/level coverage from raw payloads
// for dialects without the generated columns (test sqlite databases).
func (r *AnalyticsRepo) computeAnalyticsCoverage(runIDs []string, coverage *domain.AnalyticsCoverage) {
	for _, runID := range runIDs {
		telemetry, err := r.runs.loadRunTelemetryAll(runID)
		if err != nil {
			continue
		}
		for _, event := range telemetry {
			adapter := event.Source.Adapter
			if adapter == "" {
				adapter = "unknown"
			}
			coverage.AdapterEvents[adapter]++
		}
		logs, err := r.runs.loadRunLogsAll(runID)
		if err != nil {
			continue
		}
		for _, record := range logs {
			level := record.Level
			if level == "" {
				level = "info"
			}
			coverage.LogLevels[level]++
		}
	}
}

// queryAnalyticsCoverage groups telemetry adapters and log levels using the
// MySQL generated columns.
func queryAnalyticsCoverage(db *gorm.DB, runIDs []string) (map[string]int, map[string]int, error) {
	adapters := map[string]int{}
	levels := map[string]int{}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(runIDs)), ",")
	args := make([]any, len(runIDs))
	for i, id := range runIDs {
		args[i] = id
	}
	coverageArgs := append(append(make([]any, 0, len(args)*2), args...), args...)
	type coverageRow struct {
		Kind  string `gorm:"column:kind"`
		Value string `gorm:"column:value"`
		Count int    `gorm:"column:count"`
	}
	var rows []coverageRow
	err := db.Raw(`
		SELECT 'adapter' AS kind, adapter AS value, COUNT(*) AS count
		FROM run_telemetry WHERE run_id IN (`+placeholders+`) GROUP BY value
		UNION ALL
		SELECT 'level' AS kind, level AS value, COUNT(*) AS count
		FROM run_logs WHERE run_id IN (`+placeholders+`) GROUP BY value`, coverageArgs...).Scan(&rows).Error
	if err != nil {
		return nil, nil, err
	}
	for _, row := range rows {
		if row.Kind == "adapter" {
			adapters[row.Value] = row.Count
		} else {
			levels[row.Value] = row.Count
		}
	}
	return adapters, levels, nil
}

func runReworks(run domain.Run) int {
	total := 0
	for _, node := range run.Nodes {
		total += node.ReworkCount
	}
	return total
}

func analyticsRunDuration(run domain.Run, now time.Time) (int64, bool) {
	complete := run.Status == "succeeded" || run.Status == "failed" || run.Status == "cancelled"
	return runDuration(run, now), complete
}

func analyticsTrendBucket(at time.Time, grain string) string {
	switch strings.ToLower(strings.TrimSpace(grain)) {
	case "week":
		year, week := at.ISOWeek()
		return fmt.Sprintf("%d-W%02d", year, week)
	case "month", "all":
		return at.Format("2006-01")
	default:
		return at.Format("2006-01-02")
	}
}

// analyticsRunInWindow 保持与既有行为一致：按 Run 创建时间过滤。
func analyticsRunInWindow(run domain.Run, filter domain.AnalyticsFilter) bool {
	if !filter.From.IsZero() && run.CreatedAt.Before(filter.From) {
		return false
	}
	if !filter.To.IsZero() && run.CreatedAt.After(filter.To) {
		return false
	}
	return true
}

func analyticsRunMatchesUser(run domain.Run, userID string, issue domain.Issue) bool {
	want := strings.TrimSpace(userID)
	if want == "" {
		return true
	}
	if strconv.FormatInt(run.ExecutionContext.ResponsibleUserID, 10) == want {
		return true
	}
	return issue.AssigneeUserID != 0 && strconv.FormatInt(issue.AssigneeUserID, 10) == want
}

// analyticsRunUserID 返回执行责任人；未写入时回退到需求责任人。
func analyticsRunUserID(run domain.Run, issue domain.Issue) string {
	if run.ExecutionContext.ResponsibleUserID != 0 {
		return strconv.FormatInt(run.ExecutionContext.ResponsibleUserID, 10)
	}
	if issue.AssigneeUserID != 0 {
		return strconv.FormatInt(issue.AssigneeUserID, 10)
	}
	return ""
}

func analyticsIssueMatchesSource(issue domain.Issue, source string) bool {
	want := strings.ToLower(strings.TrimSpace(source))
	if want == "" {
		return true
	}
	got := strings.ToLower(strings.TrimSpace(issue.Source))
	if got == "" {
		got = "local"
	}
	return got == want
}

func analyticsIsRequirementIssue(issue domain.Issue) bool {
	return issue.ID != "" && domain.IsRequirementWorkItem(issue)
}

// usageCostKnown 区分"成本为 0"与"成本未识别"：显式上报/估算视为已识别，
// 旧数据（无 costStatus）按是否有成本金额判定。
func usageCostKnown(usage domain.TokenUsage) bool {
	switch strings.ToLower(strings.TrimSpace(usage.CostStatus)) {
	case "reported", "estimated":
		return true
	case "unknown":
		return false
	}
	return usage.CostUSD > 0
}

// analyticsWorkflowCategory 取 SOP 的受控统计标签，优先使用 Run 冻结的快照。
func analyticsWorkflowCategory(snapshot domain.WorkflowDefinition, fallback domain.WorkflowDefinition) string {
	if category := strings.TrimSpace(snapshot.MetricCategory); category != "" {
		return category
	}
	return strings.TrimSpace(fallback.MetricCategory)
}

func analyticsNodeTypes(nodes []domain.WorkflowNode) map[string]int {
	out := map[string]int{}
	for _, node := range nodes {
		kind := strings.TrimSpace(node.Type)
		if kind == "" {
			kind = "unknown"
		}
		out[kind]++
	}
	return out
}

func sumIssueCounts(values map[string]int) int {
	total := 0
	for _, count := range values {
		total += count
	}
	return total
}

func issueExecValues(values map[string]int64) []int64 {
	out := make([]int64, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// countInProgressIssues 统计仍未闭环的需求数（WIP 快照）。
func countInProgressIssues(issues []domain.Issue, filter domain.AnalyticsFilter) int {
	total := 0
	for _, issue := range issues {
		if !filter.MatchesProject(issue.ProjectID) {
			continue
		}
		if filter.WorkflowID != "" && issue.WorkflowID != filter.WorkflowID {
			continue
		}
		if filter.IssueSource != "" && !analyticsIssueMatchesSource(issue, filter.IssueSource) {
			continue
		}
		if filter.RequirementsOnly && !analyticsIsRequirementIssue(issue) {
			continue
		}
		if filter.UserID != "" && strconv.FormatInt(issue.AssigneeUserID, 10) != filter.UserID {
			continue
		}
		if !issueInProgress(issue) {
			continue
		}
		total++
	}
	return total
}

func issueInProgress(issue domain.Issue) bool {
	if strings.EqualFold(strings.TrimSpace(issue.DeliveryState), "merged") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(issue.Status)) {
	case "done", "closed", "cancelled", "canceled":
		return false
	}
	return true
}

// applyWeeklyThroughput 计算近 7 天的人均吞吐：唯一交付需求数 ÷ 有交付的成员数。
func applyWeeklyThroughput(summary *domain.AnalyticsSummary, deliveries []domain.AnalyticsDelivery, now time.Time) {
	since := now.AddDate(0, 0, -7)
	issues := map[string]struct{}{}
	contributors := map[string]struct{}{}
	for _, item := range deliveries {
		if !item.Delivered || item.LastRunAt.IsZero() {
			continue
		}
		if item.LastRunAt.Before(since) || item.LastRunAt.After(now.Add(time.Minute)) {
			continue
		}
		issues[item.IssueID] = struct{}{}
		if item.UserID != "" {
			contributors[item.UserID] = struct{}{}
		}
	}
	summary.WeeklyDeliveredIssues = len(issues)
	summary.WeeklyActiveContributors = len(contributors)
	if len(contributors) > 0 {
		summary.WeeklyThroughputPerUser = float64(len(issues)) / float64(len(contributors))
	}
}

func accumulateBreakdown(values map[string]*analyticsAccumulator, key, name string, run domain.Run, reworks int, duration int64, complete bool, execDuration int64) {
	value := values[key]
	if value == nil {
		value = &analyticsAccumulator{item: domain.AnalyticsBreakdown{Key: key, Name: name}}
		values[key] = value
	}
	if name != "" {
		value.item.Name = name
	}
	value.item.Runs++
	value.item.Reworks += reworks
	value.item.Tokens += run.Usage.TotalTokens
	value.item.CostUSD += run.Usage.CostUSD
	value.item.ToolCalls += run.ToolCalls
	value.inputTokens += run.Usage.InputTokens
	value.outputTokens += run.Usage.OutputTokens
	value.cacheReadTokens += run.Usage.CacheReadTokens
	value.cacheWriteTokens += run.Usage.CacheWriteTokens
	if run.Status == "succeeded" {
		value.item.Succeeded++
	}
	if run.Status == "failed" {
		value.item.Failed++
	}
	if run.Status == "cancelled" {
		value.item.Cancelled++
	}
	if run.Status == "running" || run.Status == "waiting" {
		value.item.Active++
	}
	if complete {
		value.duration += duration
		value.durations = append(value.durations, duration)
		value.execDuration += execDuration
		value.execDurations = append(value.execDurations, execDuration)
	}
}

func accumulateNode(values map[string]*analyticsAccumulator, key, name string, node *domain.NodeRun, duration int64, complete bool) {
	value := values[key]
	if value == nil {
		value = &analyticsAccumulator{item: domain.AnalyticsBreakdown{Key: key, Name: name}}
		values[key] = value
	}
	if name != "" {
		value.item.Name = name
	}
	value.item.Runs++
	value.item.Reworks += node.ReworkCount
	value.item.Tokens += node.Usage.TotalTokens
	value.item.CostUSD += node.Usage.CostUSD
	value.item.ToolCalls += node.ToolCalls
	value.inputTokens += node.Usage.InputTokens
	value.outputTokens += node.Usage.OutputTokens
	value.cacheReadTokens += node.Usage.CacheReadTokens
	value.cacheWriteTokens += node.Usage.CacheWriteTokens
	value.nodeRuns++
	if node.Attempt > 1 {
		value.retriedNodes++
	}
	if node.ReworkCount > 0 {
		value.reworkNodes++
	}
	if node.Model != "" {
		if value.models == nil {
			value.models = map[string]int{}
		}
		value.models[node.Model]++
	}
	if node.Status == "succeeded" {
		value.item.Succeeded++
	}
	if node.Status == "failed" {
		value.item.Failed++
		value.item.FailedNodes++
	}
	if node.Status == "running" || node.Status == "waiting_approval" {
		value.item.Active++
	}
	if complete {
		value.duration += duration
		value.durations = append(value.durations, duration)
		// 节点耗时本身就是"执行耗时"，与双口径中的执行耗时同源。
		value.execDuration += duration
		value.execDurations = append(value.execDurations, duration)
	}
}

// accumulateVersion 按 SOP 版本聚合，用于版本对比（样本不足时前端标注）。
func accumulateVersion(values map[string]*analyticsVersionAccumulator, workflowID, workflowName string, snapshotVersion int, fallbackVersion int, run domain.Run, duration int64, complete bool, execDuration int64) {
	version := snapshotVersion
	if version <= 0 {
		version = fallbackVersion
	}
	key := workflowID + "#" + strconv.Itoa(version)
	value := values[key]
	if value == nil {
		value = &analyticsVersionAccumulator{item: domain.AnalyticsWorkflowVersion{
			WorkflowID:         workflowID,
			Name:               workflowName,
			Version:            version,
			InsufficientSample: true,
		}}
		values[key] = value
	}
	if workflowName != "" {
		value.item.Name = workflowName
	}
	value.item.Runs++
	value.item.Tokens += run.Usage.TotalTokens
	value.item.CostUSD += run.Usage.CostUSD
	if run.Status == "succeeded" {
		value.item.Succeeded++
	}
	if run.Status == "failed" {
		value.item.Failed++
	}
	if complete {
		value.duration += duration
		value.durations = append(value.durations, duration)
		value.execDuration += execDuration
		value.execDurations = append(value.execDurations, execDuration)
	}
}

// accumulateUserUsage 按"责任人 × 自然日"聚合用量。
func accumulateUserUsage(values map[string]*analyticsUserAccumulator, userID, day string, run domain.Run, duration int64, complete bool, execDuration int64) {
	if userID == "" {
		userID = "unassigned"
	}
	key := userID + "\x00" + day
	value := values[key]
	if value == nil {
		value = &analyticsUserAccumulator{item: domain.AnalyticsUserUsage{UserID: userID, Date: day, CostKnown: true}}
		values[key] = value
	}
	value.item.Runs++
	value.item.Tokens += run.Usage.TotalTokens
	value.item.InputTokens += run.Usage.InputTokens
	value.item.OutputTokens += run.Usage.OutputTokens
	value.item.CacheReadTokens += run.Usage.CacheReadTokens
	value.item.CacheWriteTokens += run.Usage.CacheWriteTokens
	value.item.CostUSD += run.Usage.CostUSD
	value.item.ExecutionDurationMs += execDuration
	if !usageCostKnown(run.Usage) {
		value.item.CostKnown = false
	}
	if run.Status == "succeeded" {
		value.item.Success++
	}
	if run.Status == "failed" {
		value.item.Failed++
	}
	if complete {
		value.item.DurationMs += duration
	}
}

func finalizeBreakdowns(values map[string]*analyticsAccumulator) []domain.AnalyticsBreakdown {
	out := make([]domain.AnalyticsBreakdown, 0, len(values))
	for _, value := range values {
		terminal := value.item.Succeeded + value.item.Failed
		if terminal > 0 {
			value.item.SuccessRate = float64(value.item.Succeeded) * 100 / float64(terminal)
		}
		if all := terminal + value.item.Cancelled; all > 0 {
			value.item.CancelRate = float64(value.item.Cancelled) * 100 / float64(all)
			value.item.ReworkRate = float64(value.item.Reworks) * 100 / float64(value.item.Runs)
		}
		if len(value.durations) > 0 {
			value.item.AverageDurationMs = value.duration / int64(len(value.durations))
			value.item.P95DurationMs = percentile(value.durations, 95)
		}
		if len(value.execDurations) > 0 {
			value.item.AverageExecutionDurationMs = value.execDuration / int64(len(value.execDurations))
			value.item.P95ExecutionDurationMs = percentile(value.execDurations, 95)
		}
		value.item.InputTokens = value.inputTokens
		value.item.OutputTokens = value.outputTokens
		value.item.CacheReadTokens = value.cacheReadTokens
		value.item.CacheWriteTokens = value.cacheWriteTokens
		value.item.ToolCalls = value.toolCalls
		value.item.NodeRuns = value.nodeRuns
		value.item.RetriedNodes = value.retriedNodes
		value.item.ReworkNodes = value.reworkNodes
		if value.nodeRuns > 0 {
			value.item.RetryRate = float64(value.retriedNodes) * 100 / float64(value.nodeRuns)
		}
		if len(value.models) > 0 {
			value.item.ModelMix = value.models
		}
		if len(value.nodeTypes) > 0 {
			value.item.NodeTypes = value.nodeTypes
			value.item.NodeCount = value.nodeCount
		}
		if len(value.issueIDs) > 0 {
			value.item.CompletedIssues = len(value.issueIDs)
		}
		out = append(out, value.item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runs == out[j].Runs {
			return out[i].Name < out[j].Name
		}
		return out[i].Runs > out[j].Runs
	})
	return out
}

func finalizeWorkflowVersions(values map[string]*analyticsVersionAccumulator) []domain.AnalyticsWorkflowVersion {
	out := make([]domain.AnalyticsWorkflowVersion, 0, len(values))
	for _, value := range values {
		terminal := value.item.Succeeded + value.item.Failed
		if terminal > 0 {
			value.item.SuccessRate = float64(value.item.Succeeded) * 100 / float64(terminal)
		}
		if len(value.durations) > 0 {
			value.item.AverageDurationMs = value.duration / int64(len(value.durations))
		}
		if len(value.execDurations) > 0 {
			value.item.AverageExecutionDurationMs = value.execDuration / int64(len(value.execDurations))
		}
		// 样本量不足时标注，避免版本对比被单次执行误导。
		value.item.InsufficientSample = value.item.Runs < 5
		out = append(out, value.item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].WorkflowID == out[j].WorkflowID {
			return out[i].Version > out[j].Version
		}
		return out[i].WorkflowID < out[j].WorkflowID
	})
	return out
}

func finalizeUserUsage(values map[string]*analyticsUserAccumulator) []domain.AnalyticsUserUsage {
	out := make([]domain.AnalyticsUserUsage, 0, len(values))
	for _, value := range values {
		out = append(out, value.item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date == out[j].Date {
			return out[i].Tokens > out[j].Tokens
		}
		return out[i].Date > out[j].Date
	})
	return out
}

func accumulateProjectUsage(values map[string]*analyticsProjectAccumulator, projectID, issueID string, run domain.Run) {
	if projectID == "" {
		projectID = "unassigned"
	}
	value := values[projectID]
	if value == nil {
		value = &analyticsProjectAccumulator{item: domain.AnalyticsProjectUsage{ProjectID: projectID, CostKnown: true}, issues: map[string]struct{}{}}
		values[projectID] = value
	}
	value.item.Runs++
	value.item.Tokens += run.Usage.TotalTokens
	value.item.CostUSD += run.Usage.CostUSD
	if !usageCostKnown(run.Usage) {
		value.item.CostKnown = false
	}
	if run.Status == "succeeded" {
		value.item.Success++
	}
	if run.Status == "failed" {
		value.item.Failed++
	}
	if issueID != "" {
		value.issues[issueID] = struct{}{}
	}
}

func finalizeProjectUsage(values map[string]*analyticsProjectAccumulator) []domain.AnalyticsProjectUsage {
	out := make([]domain.AnalyticsProjectUsage, 0, len(values))
	for _, value := range values {
		value.item.UniqueIssues = len(value.issues)
		out = append(out, value.item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens == out[j].Tokens {
			return out[i].Runs > out[j].Runs
		}
		return out[i].Tokens > out[j].Tokens
	})
	return out
}

// applyBreakdownShares 回填各维度占全局 Token/成本的比例，用于占比结构展示。
func applyBreakdownShares(list []domain.AnalyticsBreakdown, totalTokens int64, totalCostUSD float64) {
	for i := range list {
		if totalTokens > 0 {
			list[i].TokenSharePct = float64(list[i].Tokens) * 100 / float64(totalTokens)
		}
		if totalCostUSD > 0 {
			list[i].CostSharePct = list[i].CostUSD * 100 / totalCostUSD
		}
	}
}

// analyticsIssueSource 归一需求来源，未标注的本地需求归为 local。
func analyticsIssueSource(issue domain.Issue) string {
	source := strings.ToLower(strings.TrimSpace(issue.Source))
	if source == "" {
		return "local"
	}
	return source
}

// accumulateIssueSource 按需求来源聚合执行量、Token、成本与执行耗时。
func accumulateIssueSource(values map[string]*analyticsAccumulator, source string, run domain.Run, execDuration int64) {
	if source == "" {
		source = "local"
	}
	value := values[source]
	if value == nil {
		value = &analyticsAccumulator{item: domain.AnalyticsBreakdown{Key: source, Name: source}}
		values[source] = value
	}
	value.item.Runs++
	value.item.Tokens += run.Usage.TotalTokens
	value.item.CostUSD += run.Usage.CostUSD
	value.inputTokens += run.Usage.InputTokens
	value.outputTokens += run.Usage.OutputTokens
	value.cacheReadTokens += run.Usage.CacheReadTokens
	value.cacheWriteTokens += run.Usage.CacheWriteTokens
	value.item.ToolCalls += run.ToolCalls
	if run.Status == "succeeded" {
		value.item.Succeeded++
		if value.issueIDs == nil {
			value.issueIDs = map[string]struct{}{}
		}
		value.issueIDs[strings.TrimSpace(run.ExecutionContext.IssueID)] = struct{}{}
	}
	if run.Status == "failed" {
		value.item.Failed++
	}
	if run.Status == "cancelled" {
		value.item.Cancelled++
	}
	if execDuration > 0 {
		value.execDuration += execDuration
		value.execDurations = append(value.execDurations, execDuration)
	}
}

func percentile(values []int64, pct int) int64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]int64(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	index := (len(copyValues)*pct+99)/100 - 1
	if index < 0 {
		index = 0
	}
	if index >= len(copyValues) {
		index = len(copyValues) - 1
	}
	return copyValues[index]
}

func normalizeFailure(message string) string {
	message = strings.TrimSpace(strings.Split(message, "\n")[0])
	if len(message) > 160 {
		message = message[:160]
	}
	if message == "" {
		return "unknown failure"
	}
	return message
}

func agentName(values map[string]domain.AgentDefinition, id string) string {
	if value, ok := values[id]; ok && value.Name != "" {
		return value.Name
	}
	return id
}
func executorName(values map[string]domain.ExecutorDefinition, id string) string {
	if value, ok := values[id]; ok && value.Name != "" {
		return value.Name
	}
	return id
}

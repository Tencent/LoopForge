package repo

import (
	"agentflow/internal/domain"
	"strings"
)

// Reprice a copy for analytics; raw adapter amounts and persisted Run/Node rows
// remain intact. Missing model/token splits make fallback estimates incomplete.
func analyticsPricedRun(run domain.Run) domain.Run {
	if run.IsWorkspace() {
		// Conversation workspaces have no nodes; AddConversationRunUsage
		// prices each turn with its model when it is recorded.
		return run
	}
	nodes := make(map[string]*domain.NodeRun, len(run.Nodes))
	var input, output, read, write, total int64
	var cost float64
	known, reported := true, true
	for id, original := range run.Nodes {
		if original == nil {
			continue
		}
		node := *original
		// Existing CodeBuddy rows also carry its hardcoded USD-zero placeholder.
		if node.Provider == "codebuddy" && node.Usage.CostStatus == "reported" && node.Usage.CostUSD == 0 {
			node.Usage.CostStatus = ""
		}
		model := strings.ToLower(strings.TrimSpace(node.Model))
		inclusive := node.Provider == "codex" || node.Provider == "openai" || strings.HasPrefix(model, "gpt-")
		node.Usage = domain.AnalyticsPricedUsage(node.Model, node.Usage, inclusive)
		nodes[id] = &node
		input += node.Usage.InputTokens
		output += node.Usage.OutputTokens
		read += node.Usage.CacheReadTokens
		write += node.Usage.CacheWriteTokens
		total += node.Usage.TotalTokens
		cost += node.Usage.CostUSD
		known = known && usageCostKnown(node.Usage)
		reported = reported && node.Usage.CostStatus == "reported"
	}
	u := run.Usage
	if len(nodes) == 0 {
		// A run-level bill remains usable without per-node attribution.
		// Missing attribution prevents estimation, not recognition of a bill.
		if priced := domain.AnalyticsPricedUsage("", u, false); priced.CostStatus == "reported" {
			run.Nodes, run.Usage = nodes, priced
			return run
		}
	}
	if input != u.InputTokens || output != u.OutputTokens || read != u.CacheReadTokens || write != u.CacheWriteTokens || total != u.TotalTokens {
		known = false
	}
	if len(nodes) == 0 && (u.CostUSD != 0 || u.ReasoningTokens != 0) {
		known = false
	}
	run.Nodes = nodes
	run.Usage.CostUSD, run.Usage.CostStatus = cost, "unknown"
	run.Usage.CostPricingVersion = domain.AnalyticsPricingVersion
	if known {
		run.Usage.CostStatus = "estimated"
		if reported && len(nodes) > 0 {
			run.Usage.CostStatus = "reported"
		}
	}
	return run
}

func analyticsInsightPricedUsage(u domain.InsightUsage) domain.TokenUsage {
	usage := domain.TokenUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.InputTokens + u.OutputTokens, CacheReadTokens: u.CacheReadTokens, CacheWriteTokens: u.CacheWriteTokens, ReasoningTokens: u.ReasoningTokens, CostStatus: u.CostStatus}
	if u.CostUSD != nil {
		usage.CostUSD = *u.CostUSD
	}
	return domain.AnalyticsPricedUsage(u.Model, usage, true)
}

// The legacy USD fields remain USD equivalents; CNY is the reporting currency.
func applyAnalyticsCNY(out *domain.AnalyticsSnapshot) {
	fx := domain.AnalyticsUSDToCNY
	out.Summary.CostCurrency, out.Summary.USDToCNY, out.Summary.CostPricingVersion = "CNY", fx, domain.AnalyticsPricingVersion
	out.Summary.TotalCostCNY = out.Summary.TotalCostUSD * fx
	out.Summary.CostReportedCNY = out.Summary.CostReportedUSD * fx
	out.Summary.ConversationCostCNY = out.Summary.ConversationCostUSD * fx
	out.Delivery.TotalCostCNY = out.Delivery.TotalCostUSD * fx
	for i := range out.Trend {
		out.Trend[i].CostCNY = out.Trend[i].CostUSD * fx
		out.Trend[i].ConversationCostCNY = out.Trend[i].ConversationCostUSD * fx
	}
	for i := range out.WeekHour {
		out.WeekHour[i].CostCNY = out.WeekHour[i].CostUSD * fx
	}
	for _, list := range [][]domain.AnalyticsBreakdown{out.Workflows, out.Agents, out.Executors, out.Models, out.Nodes, out.IssueSources, out.Categories} {
		for i := range list {
			list[i].CostCNY = list[i].CostUSD * fx
		}
	}
	for i := range out.Deliveries {
		out.Deliveries[i].CostCNY = out.Deliveries[i].CostUSD * fx
	}
	for i := range out.WorkflowVersions {
		out.WorkflowVersions[i].CostCNY = out.WorkflowVersions[i].CostUSD * fx
	}
	for i := range out.UserUsage {
		out.UserUsage[i].CostCNY = out.UserUsage[i].CostUSD * fx
	}
	for i := range out.ProjectUsage {
		out.ProjectUsage[i].CostCNY = out.ProjectUsage[i].CostUSD * fx
	}
	for i := range out.RunTraces {
		out.RunTraces[i].CostCNY = out.RunTraces[i].CostUSD * fx
	}
}

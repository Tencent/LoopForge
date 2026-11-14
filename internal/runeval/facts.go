package runeval

import (
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"agentflow/internal/domain"
)

const (
	maxDigestEvents  = 400
	maxDigestMessage = 200
	maxFactInput     = 800
	maxErrorText     = 160
	maxToolRepeats   = 12
	maxErrorFacts    = 12
)

func BuildFacts(run domain.Run, catalog domain.RunEvalCatalog) domain.RunEvalFacts {
	facts := domain.RunEvalFacts{
		RunID:           run.ID,
		WorkflowID:      run.WorkflowID,
		WorkflowName:    run.WorkflowSnapshot.Name,
		Status:          run.Status,
		Input:           clip(run.Input, maxFactInput),
		ProjectID:       run.ExecutionContext.ProjectID,
		Usage:           run.Usage,
		ToolCalls:       run.ToolCalls,
		ArtifactCount:   len(run.Artifacts),
		EventCount:      len(run.Events),
		TelemetryCount:  len(run.Telemetry),
		EventTypes:      map[string]int{},
		EditableCatalog: catalog,
	}
	if facts.ArtifactCount == 0 {
		facts.ArtifactCount = run.ArtifactCount
	}
	facts.WallDurationMs = elapsedMs(run.CreatedAt, run.UpdatedAt)
	nodeMeta := map[string]domain.WorkflowNode{}
	for _, node := range run.WorkflowSnapshot.Nodes {
		nodeMeta[node.ID] = node
	}
	artifactCount := map[string]int{}
	for _, art := range run.Artifacts {
		artifactCount[art.NodeID]++
	}
	lifecycle := map[string]int{}
	verification := map[string]int{}
	toolCounts := map[string]*domain.RunEvalToolRepeat{}
	errorCounts := map[string]*domain.RunEvalErrorFact{}
	var digest []domain.RunEvalDigestEvent
	for _, ev := range run.Telemetry {
		switch ev.Category {
		case "lifecycle":
			if ev.Phase == "start" || strings.Contains(strings.ToLower(ev.Name), "start") {
				lifecycle[ev.NodeID]++
			}
		case "verification":
			verification[ev.NodeID]++
		case "tool":
			bumpToolRepeat(toolCounts, ev)
		case "error":
			text := ev.Message
			if ev.Error != nil && ev.Error.Message != "" {
				text = ev.Error.Message
			}
			bumpError(errorCounts, ev.NodeID, text)
		}
		if keepDigest(ev) && len(digest) < maxDigestEvents {
			digest = append(digest, digestEvent(ev))
		}
	}
	for _, ev := range run.Events {
		facts.EventTypes[ev.Type]++
		if ev.Type == "reject" || ev.Type == "retry" || ev.Type == "rework" {
			if ev.NodeID != "" {
				facts.PingPong = appendUnique(facts.PingPong, ev.NodeID+":"+ev.Type)
			}
		}
		if strings.TrimSpace(ev.Message) != "" && (ev.Type == "error" || ev.Type == "failed" || ev.Type == "reject") {
			bumpError(errorCounts, ev.NodeID, ev.Message)
		}
	}
	for _, id := range orderedNodeIDs(run) {
		nr := run.Nodes[id]
		meta := nodeMeta[id]
		fact := domain.RunEvalNodeFact{
			NodeID:       id,
			Name:         meta.Name,
			Type:         meta.Type,
			MaxAttempts:  meta.MaxAttempts,
			MaxReworks:   meta.MaxReworks,
			Artifacts:    artifactCount[id],
			Lifecycle:    lifecycle[id],
			Verification: verification[id],
			AgentID:      meta.AgentID,
		}
		if nr != nil {
			fact.Status = nr.Status
			fact.Attempt = nr.Attempt
			fact.ReworkCount = nr.ReworkCount
			fact.ToolCalls = nr.ToolCalls
			fact.Usage = nr.Usage
			fact.Error = clip(nr.Error, maxErrorText)
			fact.DurationMs = elapsedMs(nr.StartedAt, nr.FinishedAt)
			facts.ExecDurationMs += fact.DurationMs
		}
		facts.Nodes = append(facts.Nodes, fact)
	}
	facts.ToolRepeats = rankedTools(toolCounts)
	facts.Errors = rankedErrors(errorCounts)
	facts.Digest = digest
	return facts
}

// elapsedMs returns the non-negative milliseconds between start and end.
// A zero start yields 0; a zero end means the span is still open and is
// measured up to now.
func elapsedMs(start, end time.Time) int64 {
	if start.IsZero() {
		return 0
	}
	if end.IsZero() {
		end = time.Now().UTC()
	}
	ms := end.Sub(start).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}

// orderedNodeIDs lists the workflow snapshot nodes in declaration order,
// followed by any node that only exists in the run's runtime state.
func orderedNodeIDs(run domain.Run) []string {
	ids := make([]string, 0, len(run.Nodes)+len(run.WorkflowSnapshot.Nodes))
	seen := map[string]bool{}
	for _, node := range run.WorkflowSnapshot.Nodes {
		ids = append(ids, node.ID)
		seen[node.ID] = true
	}
	for id := range run.Nodes {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

func keepDigest(ev domain.TelemetryEvent) bool {
	switch ev.Category {
	case "lifecycle", "tool", "verification", "error", "usage", "delivery", "sandbox":
		return ev.Phase != "progress"
	default:
		return ev.Level == "error" || ev.Error != nil
	}
}

func digestEvent(ev domain.TelemetryEvent) domain.RunEvalDigestEvent {
	item := domain.RunEvalDigestEvent{
		Sequence: ev.Sequence,
		NodeID:   ev.NodeID,
		Category: ev.Category,
		Name:     ev.Name,
		Phase:    ev.Phase,
		Message:  clip(ev.Message, maxDigestMessage),
	}
	if !ev.Timestamp.IsZero() {
		item.Time = ev.Timestamp.UTC().Format(time.RFC3339)
	}
	if ev.Tool != nil {
		item.Tool = ev.Tool.Name
	}
	if ev.Error != nil {
		item.Error = clip(ev.Error.Message, maxDigestMessage)
	}
	return item
}

func bumpError(bag map[string]*domain.RunEvalErrorFact, nodeID, text string) {
	text = clip(strings.Join(strings.Fields(text), " "), maxErrorText)
	if text == "" {
		return
	}
	key := nodeID + "\x00" + text
	item := bag[key]
	if item == nil {
		item = &domain.RunEvalErrorFact{NodeID: nodeID, Text: text}
		bag[key] = item
	}
	item.Count++
}

// bumpToolRepeat accumulates one tool telemetry event into bag, keyed by node
// and tool name. Count grows on phase-less, start and finish events; Failed
// grows on any error signal.
func bumpToolRepeat(bag map[string]*domain.RunEvalToolRepeat, ev domain.TelemetryEvent) {
	name := ev.Name
	if ev.Tool != nil && ev.Tool.Name != "" {
		name = ev.Tool.Name
	}
	if name == "" {
		name = "tool"
	}
	key := ev.NodeID + "\x00" + name
	item := bag[key]
	if item == nil {
		item = &domain.RunEvalToolRepeat{NodeID: ev.NodeID, Tool: name}
		bag[key] = item
	}
	if ev.Phase == "" || ev.Phase == "start" || ev.Phase == "finish" {
		item.Count++
	}
	if ev.Error != nil || ev.Phase == "error" || ev.Level == "error" {
		item.Failed++
	}
}

func rankedTools(bag map[string]*domain.RunEvalToolRepeat) []domain.RunEvalToolRepeat {
	out := make([]domain.RunEvalToolRepeat, 0, len(bag))
	for _, item := range bag {
		if item.Count < 3 && item.Failed < 2 {
			continue
		}
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Failed != out[j].Failed {
			return out[i].Failed > out[j].Failed
		}
		return out[i].Count > out[j].Count
	})
	if len(out) > maxToolRepeats {
		out = out[:maxToolRepeats]
	}
	return out
}

func rankedErrors(bag map[string]*domain.RunEvalErrorFact) []domain.RunEvalErrorFact {
	out := make([]domain.RunEvalErrorFact, 0, len(bag))
	for _, item := range bag {
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if len(out) > maxErrorFacts {
		out = out[:maxErrorFacts]
	}
	return out
}

func appendUnique(list []string, value string) []string {
	if slices.Contains(list, value) {
		return list
	}
	return append(list, value)
}

func clip(value string, n int) string {
	value = strings.TrimSpace(value)
	if n <= 0 || utf8.RuneCountInString(value) <= n {
		return value
	}
	runes := []rune(value)
	return string(runes[:n]) + "…"
}

func BuildCatalog(run domain.Run, resolveAgent func(id string) (domain.AgentDefinition, bool)) domain.RunEvalCatalog {
	wf := run.WorkflowSnapshot
	out := domain.RunEvalCatalog{WorkflowID: wf.ID, Generic: wf.Generic}
	seenAgents := map[string]bool{}
	for _, node := range wf.Nodes {
		out.Nodes = append(out.Nodes, domain.RunEvalCatalogNode{
			ID:            node.ID,
			Name:          node.Name,
			Type:          node.Type,
			AgentID:       node.AgentID,
			MaxAttempts:   node.MaxAttempts,
			MaxReworks:    node.MaxReworks,
			OnFailure:     node.OnFailure,
			RetryStrategy: node.RetryStrategy,
		})
		if node.AgentID == "" || seenAgents[node.AgentID] || resolveAgent == nil {
			continue
		}
		agent, ok := resolveAgent(node.AgentID)
		if !ok {
			continue
		}
		seenAgents[agent.ID] = true
		item := domain.RunEvalCatalogAgent{
			ID:           agent.ID,
			Name:         agent.Name,
			ProjectID:    agent.ProjectID,
			Shared:       agent.Shared,
			Prompt:       clip(agent.Prompt, 400),
			Verification: agent.Verification,
		}
		out.Agents = append(out.Agents, item)
	}
	return out
}

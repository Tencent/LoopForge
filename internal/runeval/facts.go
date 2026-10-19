package runeval

import (
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
	if !run.CreatedAt.IsZero() {
		end := run.UpdatedAt
		if end.IsZero() {
			end = time.Now().UTC()
		}
		facts.WallDurationMs = end.Sub(run.CreatedAt).Milliseconds()
		if facts.WallDurationMs < 0 {
			facts.WallDurationMs = 0
		}
	}
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
			name := ev.Name
			if ev.Tool != nil && ev.Tool.Name != "" {
				name = ev.Tool.Name
			}
			if name == "" {
				name = "tool"
			}
			key := ev.NodeID + "\x00" + name
			item := toolCounts[key]
			if item == nil {
				item = &domain.RunEvalToolRepeat{NodeID: ev.NodeID, Tool: name}
				toolCounts[key] = item
			}
			if ev.Phase == "" || ev.Phase == "start" || ev.Phase == "finish" {
				item.Count++
			}
			if ev.Error != nil || ev.Phase == "error" || ev.Level == "error" {
				item.Failed++
			}
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
	ids := make([]string, 0, len(run.Nodes)+len(nodeMeta))
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
	for _, id := range ids {
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
			if !nr.StartedAt.IsZero() {
				end := nr.FinishedAt
				if end.IsZero() {
					end = time.Now().UTC()
				}
				fact.DurationMs = end.Sub(nr.StartedAt).Milliseconds()
				if fact.DurationMs < 0 {
					fact.DurationMs = 0
				}
				facts.ExecDurationMs += fact.DurationMs
			}
		}
		facts.Nodes = append(facts.Nodes, fact)
	}
	facts.ToolRepeats = rankedTools(toolCounts)
	facts.Errors = rankedErrors(errorCounts)
	facts.Digest = digest
	return facts
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
	for _, item := range list {
		if item == value {
			return list
		}
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
			Instructions: clip(agent.Instructions, 400),
			Verification: agent.Verification,
		}
		if agent.TaskPrompt != nil {
			item.Objective = clip(agent.TaskPrompt.Objective, 240)
		}
		out.Agents = append(out.Agents, item)
	}
	return out
}

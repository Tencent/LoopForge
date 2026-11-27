package evolution

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// ToolHotspot summarizes how frequently and expensively one tool was invoked.
type ToolHotspot struct {
	Tool       string `json:"tool"`
	Calls      int    `json:"calls"`
	Failed     int    `json:"failed,omitempty"`
	Duplicates int    `json:"duplicates,omitempty"`
	TotalMs    int64  `json:"totalMs,omitempty"`
}

// ToolTraceSummary is a compact, high-signal digest of a run's tool calls
// (duplicates, failures, slow/heavy outputs, and step sequence) passed to the
// improver so edits can target actual execution bottlenecks instead of guessing.
type ToolTraceSummary struct {
	TotalCalls     int           `json:"totalCalls"`
	FailedCalls    int           `json:"failedCalls,omitempty"`
	DuplicateCalls int           `json:"duplicateCalls,omitempty"`
	TopTools       []ToolHotspot `json:"topTools,omitempty"`
	Duplicates     []string      `json:"duplicates,omitempty"`
	Failures       []string      `json:"failures,omitempty"`
	HeavyCalls     []string      `json:"heavyCalls,omitempty"`
	Steps          []string      `json:"steps,omitempty"`
}

type collapsedToolCall struct {
	index       int
	callID      string
	tool        string
	target      string
	sig         string
	failed      bool
	errMsg      string
	durationMs  int64
	outputBytes int
	startAt     time.Time
	endAt       time.Time
}

const (
	maxTraceHotspotTools = 6
	maxTraceDuplicates   = 6
	maxTraceFailures     = 5
	maxTraceHeavyCalls   = 5
	maxTraceSteps        = 16
	heavyCallDurationMs  = 3000
	heavyCallOutputBytes = 8000
)

// BuildToolTraceSummary compresses raw tool telemetry events into hotspots
// (top tools, duplicate invocations, failed invocations, heavy calls, and a
// chronological step trace). When nodeID is non-empty and matching tool events
// exist, the summary focuses on that node; otherwise it covers the whole run.
func BuildToolTraceSummary(events []domain.TelemetryEvent, nodeID string) *ToolTraceSummary {
	nodeID = strings.TrimSpace(nodeID)
	allTool := make([]domain.TelemetryEvent, 0, len(events))
	nodeTool := make([]domain.TelemetryEvent, 0, len(events))
	for _, ev := range events {
		if ev.Category != "tool" {
			continue
		}
		allTool = append(allTool, ev)
		if nodeID != "" && ev.NodeID == nodeID {
			nodeTool = append(nodeTool, ev)
		}
	}
	selected := allTool
	if len(nodeTool) > 0 {
		selected = nodeTool
	}
	if len(selected) == 0 {
		return nil
	}

	// Normalize to chronological order regardless of query sort direction.
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].Sequence != 0 && selected[j].Sequence != 0 && selected[i].Sequence != selected[j].Sequence {
			return selected[i].Sequence < selected[j].Sequence
		}
		if !selected[i].Timestamp.Equal(selected[j].Timestamp) {
			return selected[i].Timestamp.Before(selected[j].Timestamp)
		}
		return selected[i].ID < selected[j].ID
	})

	calls := make([]*collapsedToolCall, 0, len(selected))
	byCallID := map[string]*collapsedToolCall{}

	for _, ev := range selected {
		name := toolEventName(ev)
		if name == "" {
			continue
		}
		callID := strings.TrimSpace(ev.ToolCallID)
		if callID == "" && ev.Tool != nil {
			callID = strings.TrimSpace(ev.Tool.CallID)
		}
		var item *collapsedToolCall
		if callID != "" {
			item = byCallID[callID]
		}
		if item == nil {
			item = &collapsedToolCall{
				index:   len(calls) + 1,
				callID:  callID,
				tool:    name,
				startAt: ev.Timestamp,
				endAt:   ev.Timestamp,
			}
			calls = append(calls, item)
			if callID != "" {
				byCallID[callID] = item
			}
		} else {
			if item.tool == "" || item.tool == "tool" {
				item.tool = name
			}
			if !ev.Timestamp.IsZero() && (item.startAt.IsZero() || ev.Timestamp.Before(item.startAt)) {
				item.startAt = ev.Timestamp
			}
			if !ev.Timestamp.IsZero() && ev.Timestamp.After(item.endAt) {
				item.endAt = ev.Timestamp
			}
		}

		if target := toolEventTarget(ev); target != "" && item.target == "" {
			item.target = target
		}
		if ev.Tool != nil && ev.Tool.DurationMs > item.durationMs {
			item.durationMs = ev.Tool.DurationMs
		}
		if ob := toolEventOutputBytes(ev); ob > item.outputBytes {
			item.outputBytes = ob
		}
		if toolEventFailed(ev) {
			item.failed = true
			if msg := toolEventError(ev); msg != "" && item.errMsg == "" {
				item.errMsg = msg
			}
		}
	}

	if len(calls) == 0 {
		return nil
	}

	// Fill derived duration when explicit DurationMs was omitted.
	for _, c := range calls {
		if c.durationMs <= 0 && !c.startAt.IsZero() && !c.endAt.IsZero() && c.endAt.After(c.startAt) {
			c.durationMs = c.endAt.Sub(c.startAt).Milliseconds()
		}
		c.sig = normalizeToolSignature(c.tool, c.target)
	}

	summary := &ToolTraceSummary{
		TotalCalls: len(calls),
	}

	toolAgg := map[string]*ToolHotspot{}
	sigCounts := map[string]int{}
	sigExample := map[string]*collapsedToolCall{}
	heavy := make([]*collapsedToolCall, 0)

	for _, c := range calls {
		agg := toolAgg[c.tool]
		if agg == nil {
			agg = &ToolHotspot{Tool: c.tool}
			toolAgg[c.tool] = agg
		}
		agg.Calls++
		agg.TotalMs += c.durationMs
		if c.failed {
			agg.Failed++
			summary.FailedCalls++
			if len(summary.Failures) < maxTraceFailures {
				line := formatToolCallLabel(c.tool, c.target)
				if c.errMsg != "" {
					line += ": " + domain.TrimEvoText(c.errMsg, 120)
				}
				summary.Failures = append(summary.Failures, fmt.Sprintf("#%d %s", c.index, line))
			}
		}
		if c.sig != "" {
			sigCounts[c.sig]++
			if _, ok := sigExample[c.sig]; !ok {
				sigExample[c.sig] = c
			}
		}
		if c.durationMs >= heavyCallDurationMs || c.outputBytes >= heavyCallOutputBytes {
			heavy = append(heavy, c)
		}
	}

	type dupEntry struct {
		sig   string
		count int
		call  *collapsedToolCall
	}
	dups := make([]dupEntry, 0)
	for sig, count := range sigCounts {
		if count <= 1 {
			continue
		}
		ex := sigExample[sig]
		extra := count - 1
		summary.DuplicateCalls += extra
		if agg := toolAgg[ex.tool]; agg != nil {
			agg.Duplicates += extra
		}
		dups = append(dups, dupEntry{sig: sig, count: count, call: ex})
	}
	sort.Slice(dups, func(i, j int) bool {
		if dups[i].count != dups[j].count {
			return dups[i].count > dups[j].count
		}
		return dups[i].call.index < dups[j].call.index
	})
	for i, d := range dups {
		if i >= maxTraceDuplicates {
			break
		}
		summary.Duplicates = append(summary.Duplicates, fmt.Sprintf("%s ×%d", formatToolCallLabel(d.call.tool, d.call.target), d.count))
	}

	hotspots := make([]ToolHotspot, 0, len(toolAgg))
	for _, agg := range toolAgg {
		hotspots = append(hotspots, *agg)
	}
	sort.Slice(hotspots, func(i, j int) bool {
		if hotspots[i].Calls != hotspots[j].Calls {
			return hotspots[i].Calls > hotspots[j].Calls
		}
		if hotspots[i].Duplicates != hotspots[j].Duplicates {
			return hotspots[i].Duplicates > hotspots[j].Duplicates
		}
		if hotspots[i].TotalMs != hotspots[j].TotalMs {
			return hotspots[i].TotalMs > hotspots[j].TotalMs
		}
		return hotspots[i].Tool < hotspots[j].Tool
	})
	if len(hotspots) > maxTraceHotspotTools {
		hotspots = hotspots[:maxTraceHotspotTools]
	}
	summary.TopTools = hotspots

	sort.Slice(heavy, func(i, j int) bool {
		si := heavy[i].durationMs + int64(heavy[i].outputBytes/4)
		sj := heavy[j].durationMs + int64(heavy[j].outputBytes/4)
		if si != sj {
			return si > sj
		}
		return heavy[i].index < heavy[j].index
	})
	for i, c := range heavy {
		if i >= maxTraceHeavyCalls {
			break
		}
		parts := []string{}
		if c.durationMs > 0 {
			parts = append(parts, fmt.Sprintf("%.1fs", float64(c.durationMs)/1000.0))
		}
		if c.outputBytes > 0 {
			parts = append(parts, formatByteSize(c.outputBytes))
		}
		summary.HeavyCalls = append(summary.HeavyCalls, fmt.Sprintf("#%d %s (%s)", c.index, formatToolCallLabel(c.tool, c.target), strings.Join(parts, ", ")))
	}

	summary.Steps = buildTraceSteps(calls)
	return summary
}

func buildTraceSteps(calls []*collapsedToolCall) []string {
	formatStep := func(c *collapsedToolCall) string {
		status := "ok"
		if c.failed {
			if c.errMsg != "" {
				status = "fail: " + domain.TrimEvoText(c.errMsg, 60)
			} else {
				status = "fail"
			}
		}
		meta := status
		if c.durationMs >= 1000 {
			meta += fmt.Sprintf(", %.1fs", float64(c.durationMs)/1000.0)
		}
		return fmt.Sprintf("%d. %s [%s]", c.index, formatToolCallLabel(c.tool, c.target), meta)
	}
	if len(calls) <= maxTraceSteps {
		steps := make([]string, 0, len(calls))
		for _, c := range calls {
			steps = append(steps, formatStep(c))
		}
		return steps
	}
	headCount := 10
	tailCount := maxTraceSteps - headCount - 1
	steps := make([]string, 0, maxTraceSteps)
	for i := 0; i < headCount; i++ {
		steps = append(steps, formatStep(calls[i]))
	}
	omitted := len(calls) - headCount - tailCount
	steps = append(steps, fmt.Sprintf("... (省略中间 %d 步) ...", omitted))
	for i := len(calls) - tailCount; i < len(calls); i++ {
		steps = append(steps, formatStep(calls[i]))
	}
	return steps
}

func toolEventName(ev domain.TelemetryEvent) string {
	if ev.Tool != nil && strings.TrimSpace(ev.Tool.Name) != "" {
		return strings.TrimSpace(ev.Tool.Name)
	}
	return strings.TrimSpace(ev.Name)
}

func toolEventFailed(ev domain.TelemetryEvent) bool {
	if ev.Tool != nil && ev.Tool.Success != nil && !*ev.Tool.Success {
		return true
	}
	return ev.Error != nil || ev.Phase == "error" || ev.Level == "error"
}

func toolEventError(ev domain.TelemetryEvent) string {
	if ev.Error != nil && strings.TrimSpace(ev.Error.Message) != "" {
		return strings.TrimSpace(ev.Error.Message)
	}
	if toolEventFailed(ev) && strings.TrimSpace(ev.Message) != "" {
		return strings.TrimSpace(ev.Message)
	}
	return ""
}

var preferredToolTargetKeys = []string{
	"command", "cmd", "path", "file_path", "filePath", "file",
	"pattern", "query", "url", "target", "symbol", "directory", "dir",
	"input", "args", "arguments",
}

func toolEventTarget(ev domain.TelemetryEvent) string {
	if ev.Attributes != nil {
		for _, key := range preferredToolTargetKeys {
			if raw, ok := ev.Attributes[key]; ok {
				if val := compactAttrValue(raw); val != "" {
					return domain.TrimEvoText(val, 80)
				}
			}
		}
	}
	if ev.Tool != nil && strings.TrimSpace(ev.Tool.Operation) != "" {
		return domain.TrimEvoText(strings.TrimSpace(ev.Tool.Operation), 80)
	}
	if !toolEventFailed(ev) && strings.TrimSpace(ev.Message) != "" {
		return domain.TrimEvoText(strings.TrimSpace(ev.Message), 80)
	}
	return ""
}

func compactAttrValue(v any) string {
	switch x := v.(type) {
	case string:
		return strings.Join(strings.Fields(x), " ")
	case map[string]any:
		for _, key := range preferredToolTargetKeys {
			if raw, ok := x[key]; ok {
				if s := compactAttrValue(raw); s != "" {
					return s
				}
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, min(len(keys), 2))
		for _, k := range keys {
			if s := compactAttrValue(x[k]); s != "" {
				parts = append(parts, k+"="+s)
				if len(parts) >= 2 {
					break
				}
			}
		}
		return strings.Join(parts, " ")
	case []any:
		parts := make([]string, 0, min(len(x), 3))
		for _, item := range x {
			if s := compactAttrValue(item); s != "" {
				parts = append(parts, s)
				if len(parts) >= 3 {
					break
				}
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func toolEventOutputBytes(ev domain.TelemetryEvent) int {
	if ev.Attributes == nil {
		return 0
	}
	for _, key := range []string{"outputBytes", "output_bytes", "resultBytes", "size", "bytes"} {
		if raw, ok := ev.Attributes[key]; ok {
			switch n := raw.(type) {
			case int:
				if n > 0 {
					return n
				}
			case int64:
				if n > 0 {
					return int(n)
				}
			case float64:
				if n > 0 {
					return int(n)
				}
			}
		}
	}
	for _, key := range []string{"output", "result", "stdout"} {
		if s, ok := ev.Attributes[key].(string); ok && len(s) > 0 {
			return len(s)
		}
	}
	return 0
}

func normalizeToolSignature(tool, target string) string {
	t := strings.ToLower(strings.TrimSpace(tool))
	if t == "" {
		return ""
	}
	arg := strings.ToLower(strings.Join(strings.Fields(target), " "))
	if arg == "" {
		// Without a target argument, only exact repeated calls to the same
		// no-arg tool are counted if the tool name itself is specific.
		return ""
	}
	return t + "|" + arg
}

func formatToolCallLabel(tool, target string) string {
	if target == "" {
		return tool
	}
	return fmt.Sprintf("%s(%s)", tool, domain.TrimEvoText(target, 72))
}

func formatByteSize(b int) string {
	if b >= 1024 {
		return fmt.Sprintf("%.1fKB", float64(b)/1024.0)
	}
	return fmt.Sprintf("%dB", b)
}

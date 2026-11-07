package engine

import (
	"agentflow/internal/domain"
	"os"
	"path/filepath"
	"strings"
)

const maxHumanContextBytes = 8000

func composeHumanFeedback(reason, context string) string {
	reason = strings.TrimSpace(reason)
	context = strings.TrimSpace(context)
	if context != "" && len(context) > maxHumanContextBytes {
		context = context[:maxHumanContextBytes]
	}
	switch {
	case context == "":
		return reason
	case reason == "":
		return "人工补充上下文：\n" + context
	default:
		return reason + "\n\n人工补充上下文：\n" + context
	}
}

func recordHumanContext(r *domain.Run, nodeID, reason, context string) string {
	note := composeHumanFeedback(reason, context)
	ctx := strings.TrimSpace(context)
	if ctx == "" {
		return note
	}
	if len(ctx) > maxHumanContextBytes {
		ctx = ctx[:maxHumanContextBytes]
	}
	if prev := strings.TrimSpace(r.HumanContext); prev != "" && prev != ctx {
		r.HumanContext = prev + "\n\n---\n" + ctx
	} else {
		r.HumanContext = ctx
	}
	attempt := 0
	if nr := r.Nodes[nodeID]; nr != nil {
		attempt = nr.Attempt
	}
	appendArtifact(r, nodeID, attempt, "human-context", "human-context.md", "text/markdown", []byte("# 人工补充上下文\n\n"+ctx+"\n"))
	return note
}

func writeHumanContextFile(workspace, note string) error {
	note = strings.TrimSpace(note)
	if note == "" || strings.TrimSpace(workspace) == "" {
		return nil
	}
	return os.WriteFile(filepath.Join(workspace, "human-context.md"), []byte("# 人工补充上下文\n\n"+note+"\n"), 0o644)
}

func safePathSegment(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "artifact"
	}
	return b.String()
}

func extensionFor(mediaType string) string {
	if mediaType == "text/markdown" || mediaType == "" {
		return ".md"
	}
	if mediaType == "application/json" {
		return ".json"
	}
	return ".txt"
}

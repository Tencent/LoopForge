package engine

import (
	"agentflow/internal/domain"
	"os"
	"path/filepath"
	"strings"
)

const maxHumanContextBytes = 8000

const (
	// humanContextLabel prefixes the operator note composeHumanFeedback folds
	// into a node's feedback.
	humanContextLabel = "人工补充上下文：\n"
	// humanContextSeparator joins successive notes in Run.HumanContext.
	humanContextSeparator = "\n\n---\n"
)

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
		return humanContextLabel + context
	default:
		return reason + "\n\n" + humanContextLabel + context
	}
}

// feedbackWithoutHumanContext drops the operator notes recordHumanContext
// folded into a node's feedback when the prompt renders Run.HumanContext as its
// own section, so the agent reads each note once. Text the notes do not cover,
// such as the retry reason, stays.
func feedbackWithoutHumanContext(feedback, humanContext string) string {
	humanContext = strings.TrimSpace(humanContext)
	if feedback == "" || humanContext == "" {
		return feedback
	}
	stripped := feedback
	for _, note := range append([]string{humanContext}, strings.Split(humanContext, humanContextSeparator)...) {
		if note = strings.TrimSpace(note); note == "" {
			continue
		}
		block := humanContextLabel + note
		stripped = strings.ReplaceAll(stripped, "\n\n"+block, "")
		stripped = strings.ReplaceAll(stripped, block, "")
	}
	if stripped == feedback {
		return feedback
	}
	return strings.TrimSpace(stripped)
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
		r.HumanContext = prev + humanContextSeparator + ctx
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
	// Replace through a rename: a parallel branch's CLI may be reading the
	// file and must never see it truncated.
	target := filepath.Join(workspace, "human-context.md")
	content := []byte("# 人工补充上下文\n\n" + note + "\n")
	if existing, err := os.ReadFile(target); err == nil && string(existing) == string(content) {
		return nil
	}
	temp, err := os.CreateTemp(workspace, ".human-context-*.md")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), target)
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

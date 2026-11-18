package engine

import (
	"agentflow/internal/domain"
	"os"
	"path/filepath"
	"strings"
)

const maxHumanContextBytes = 8000

func runNodeLanguage(r *domain.Run, nodeID string, extra ...string) string {
	var texts []string
	texts = append(texts, extra...)
	if r != nil {
		texts = append(texts, r.HumanContext, r.Input)
		if r.WorkflowSnapshot.Name != "" {
			texts = append(texts, r.WorkflowSnapshot.Name)
		}
		if nodeID != "" {
			if nr := r.Nodes[nodeID]; nr != nil {
				texts = append(texts, nr.Feedback, nr.Output)
			}
			for _, n := range r.WorkflowSnapshot.Nodes {
				if n.ID == nodeID {
					texts = append(texts, n.Name, n.TaskPrompt)
					break
				}
			}
		}
		for _, n := range r.WorkflowSnapshot.Nodes {
			texts = append(texts, n.Name)
		}
	}
	return detectPromptLanguage(texts...)
}

func composeHumanFeedback(reason, context string) string {
	return composeHumanFeedbackWithLang(reason, context, detectPromptLanguage(reason, context))
}

func composeHumanFeedbackWithLang(reason, context, lang string) string {
	reason = strings.TrimSpace(reason)
	context = strings.TrimSpace(context)
	if context != "" && len(context) > maxHumanContextBytes {
		context = context[:maxHumanContextBytes]
	}
	header := "人工补充上下文：\n"
	switch lang {
	case "ko":
		header = "사용자 추가 컨텍스트:\n"
	case "en":
		header = "Manual Context:\n"
	}
	switch {
	case context == "":
		return reason
	case reason == "":
		return header + context
	default:
		return reason + "\n\n" + header + context
	}
}

func recordHumanContext(r *domain.Run, nodeID, reason, context string) string {
	lang := runNodeLanguage(r, nodeID, reason, context)
	return recordHumanContextWithLang(r, nodeID, reason, context, lang)
}

func recordHumanContextWithLang(r *domain.Run, nodeID, reason, context, lang string) string {
	note := composeHumanFeedbackWithLang(reason, context, lang)
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
	artifactTitle := "# 人工补充上下文\n\n"
	switch lang {
	case "ko":
		artifactTitle = "# 사용자 추가 컨텍스트\n\n"
	case "en":
		artifactTitle = "# Manual Context\n\n"
	}
	appendArtifact(r, nodeID, attempt, "human-context", "human-context.md", "text/markdown", []byte(artifactTitle+ctx+"\n"))
	return note
}

func writeHumanContextFile(workspace, note string) error {
	note = strings.TrimSpace(note)
	if note == "" || strings.TrimSpace(workspace) == "" {
		return nil
	}
	lang := detectPromptLanguage(note)
	artifactTitle := "# 人工补充上下文\n\n"
	switch lang {
	case "ko":
		artifactTitle = "# 사용자 추가 컨텍스트\n\n"
	case "en":
		artifactTitle = "# Manual Context\n\n"
	}
	// Replace through a rename: a parallel branch's CLI may be reading the
	// file and must never see it truncated.
	target := filepath.Join(workspace, "human-context.md")
	content := []byte(artifactTitle + note + "\n")
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

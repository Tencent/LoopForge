package httpapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
)

func (d *deps) knowledgeService() *knowledge.Service {
	if d == nil {
		return nil
	}
	if d.knowledgeSvc != nil {
		return d.knowledgeSvc
	}
	if d.repo != nil {
		d.knowledgeSvc = knowledge.NewService(d.repo, randomID)
	}
	return d.knowledgeSvc
}

func (d *deps) executor() *knowledge.Executor {
	if d == nil {
		return nil
	}
	if d.knowledgeExec != nil {
		return d.knowledgeExec
	}
	if svc := d.knowledgeService(); svc != nil {
		d.knowledgeExec = knowledge.NewExecutor(svc)
	}
	return d.knowledgeExec
}

func knowledgeToolNamespace(connectorID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(connectorID)))
	return fmt.Sprintf("kb_%x", sum[:6])
}

func knowledgeServerMeta(d *deps, ownerUserID int64, connectorID string) (name, connType string) {
	if d != nil {
		if svc := d.knowledgeService(); svc != nil {
			if conn, ok := svc.GetConnector(ownerUserID, connectorID); ok {
				return firstNonEmpty(conn.Name, conn.ID), conn.Type
			}
		}
	}
	return connectorID, ""
}

func isKnowledgeSearchOrReadTool(upstreamName string) bool {
	switch upstreamName {
	case knowledge.ToolSearch, knowledge.ToolFetch, knowledge.ToolListSources,
		knowledge.WeKnoraToolSearchKnowledge, knowledge.WeKnoraToolReadDocument,
		knowledge.WeKnoraToolListDocuments, knowledge.WeKnoraToolListKnowledgeBases,
		"grep_chunks":
		return true
	default:
		return strings.Contains(upstreamName, "search") || strings.Contains(upstreamName, "read") || strings.Contains(upstreamName, "fetch")
	}
}

func knowledgeToolDescription(connName, connType, connectorID, upstreamName, rawDesc string) string {
	label := strings.TrimSpace(connName)
	if label == "" {
		label = connectorID
	}
	header := "[个人知识库：" + label
	if t := strings.TrimSpace(connType); t != "" {
		header += " (" + t + ")"
	}
	header += "]"
	desc := strings.TrimSpace(rawDesc)
	if desc != "" {
		desc = header + " · " + desc
	} else {
		desc = header + " · " + upstreamName
	}
	if isKnowledgeSearchOrReadTool(upstreamName) {
		desc += "；涉及个人档案、团队规范、项目架构、排障或内部事实时优先调用；寒暄或纯通用知识无需调用。"
	}
	return desc
}

// knowledgeReferencePromptContext builds the Chat SystemPrompt section for bound
// personal knowledge connectors: lookup/citation policy + cached L0 catalog.
func knowledgeReferencePromptContext(ctx context.Context, d *deps, uid int64, refs []domain.KnowledgeRef) string {
	if len(refs) == 0 || d == nil {
		return ""
	}
	exec := d.executor()
	if exec == nil {
		return domain.KnowledgeLookupInstruction
	}
	catalog := exec.BuildCatalogXML(ctx, uid, refs, "chat", func(conn domain.KnowledgeConnector, toolName string) string {
		return knowledgeToolNamespace(conn.ID) + "__" + toolName
	})
	if catalog == "" {
		return domain.KnowledgeLookupInstruction
	}
	return domain.KnowledgeLookupInstruction + "\n" + catalog
}

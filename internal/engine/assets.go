package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/repo"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// platformMCPServer materializes the reserved agentflow-platform MCP
// definition. The token stays in memory (env-only) and travels with the run's
// asset bundle the same way any other HTTP MCP header does. Without a
// configured base the URL is path-only: execution machines rebase it onto
// their own server URL, while cloud runs drop it (see runtime.PrepareMCPServers).
func (e *Engine) platformMCPServer() domain.MCPServerDefinition {
	e.mu.Lock()
	base, token := e.platformMCPBase, e.platformMCPToken
	e.mu.Unlock()
	def := domain.MCPServerDefinition{
		ID:          domain.PlatformMCPServerID,
		Name:        "LoopForge 平台",
		Scope:       "project",
		Description: "LoopForge 平台侧能力：需求、任务、运行、项目等平台操作以 MCP 工具提供",
		Transport:   "http",
		URL:         base + "/mcp",
		TimeoutSec:  60,
	}
	if token != "" {
		def.Headers = map[string]string{"Authorization": "Bearer " + token}
	}
	return def
}

// knowledgeConnectorMCP resolves one user-owned KnowledgeConnector ref into
// a read-only proxied MCPServerDefinition with an afkb1. token. Returns false
// when the connector does not exist under userID or is disabled.
func (e *Engine) knowledgeConnectorMCP(claims knowledge.ProxyClaims) (domain.MCPServerDefinition, domain.KnowledgeConnector, bool) {
	if e.repo == nil || claims.Ref == nil || strings.TrimSpace(claims.Ref.ConnectorID) == "" {
		return domain.MCPServerDefinition{}, domain.KnowledgeConnector{}, false
	}
	exec := e.knowledgeExecutor()
	if exec == nil || exec.Service() == nil {
		return domain.MCPServerDefinition{}, domain.KnowledgeConnector{}, false
	}
	conn, ok := exec.Service().GetConnector(claims.UserID, claims.Ref.ConnectorID)
	if !ok || !conn.Enabled {
		return domain.MCPServerDefinition{}, domain.KnowledgeConnector{}, false
	}
	e.mu.Lock()
	base := e.platformMCPBase
	e.mu.Unlock()
	token, err := knowledge.SignProxyToken(claims, time.Now())
	if err != nil {
		return domain.MCPServerDefinition{}, domain.KnowledgeConnector{}, false
	}
	serverID := domain.KnowledgeMCPServerID + "-" + conn.ID
	desc := fmt.Sprintf("个人只读知识库连接器（%s）：%s", conn.Type, conn.Name)
	return knowledge.MCPServerDefinition(base, serverID, conn.Name, desc, token), conn, true
}

// knowledgeSkippedWarning explains a role knowledge toggle that could not
// attach any user-owned connector, so the node log stays informative.
func knowledgeSkippedWarning(agent domain.AgentDefinition, assets domain.AgentAssetBundle) string {
	if !agent.KnowledgeBase {
		return ""
	}
	for _, server := range assets.MCPServers {
		if server.ID == domain.KnowledgeMCPServerID || strings.HasPrefix(server.ID, domain.KnowledgeMCPServerID+"-") {
			return ""
		}
	}
	return "角色开启了知识库，但当前执行人未配置已启用的个人知识库连接器（" + domain.KnowledgeMCPServerID + "）"
}

// RoleAssets resolves a role's assets outside any LOOP run, for a chat
// subagent: no workflow is bound, so workflow-global rules are not added.
func (e *Engine) RoleAssets(agent domain.AgentDefinition, projectID string, userID int64) domain.AgentAssetBundle {
	return e.resolveAssets(agent, projectID, "", userID)
}

func (e *Engine) resolveAssets(agent domain.AgentDefinition, projectID, workflowID string, userID int64) domain.AgentAssetBundle {
	bundle := domain.AgentAssetBundle{}
	for _, id := range agent.Rules {
		if v, ok := e.repo.Rule(id); ok {
			if v.ProjectID != projectID && !v.Shared {
				if tmpl, found := e.repo.GenericRuleByID(id); found {
					bundle.Rules = append(bundle.Rules, tmpl)
				}
				continue
			}
			bundle.Rules = append(bundle.Rules, v)
			continue
		}
		if v, ok := e.repo.GenericRuleByID(id); ok {
			bundle.Rules = append(bundle.Rules, v)
		}
	}
	for _, id := range agent.Skills {
		if v, ok := e.repo.Skill(id); ok {
			if v.ProjectID != projectID && !v.Shared {
				if tmpl, found := e.repo.GenericSkillByID(id); found {
					bundle.Skills = append(bundle.Skills, tmpl)
				}
				continue
			}
			if len(v.FileRefs) > 0 {
				if contents := e.repo.SkillFileContents(v.ID); len(contents) > 0 {
					if bundle.SkillFiles == nil {
						bundle.SkillFiles = map[string]map[string][]byte{}
					}
					bundle.SkillFiles[v.ID] = contents
				}
			}
			bundle.Skills = append(bundle.Skills, v)
			continue
		}
		if v, ok := e.repo.GenericSkillByID(id); ok {
			bundle.Skills = append(bundle.Skills, v)
		}
	}
	for _, id := range agent.MCPServers {
		if v, ok := e.repo.MCPServer(id); ok {
			if !roleMCPServerVisible(v, projectID, userID) {
				continue
			}
			bundle.MCPServers = append(bundle.MCPServers, v)
			e.loadMCPFiles(&bundle, v)
			continue
		}
		if v, ok := e.projectMarketMCP(id, projectID); ok {
			bundle.MCPServers = append(bundle.MCPServers, v)
		}
	}
	// 平台 MCP 是角色级独立开关（不进 mcpServers 列表）：开启即注入
	// agentflow-platform，让本次调用拿到平台侧操作工具。
	if agent.PlatformMCP {
		bundle.MCPServers = append(bundle.MCPServers, e.platformMCPServer())
	}
	if agent.KnowledgeBase {
		exec := e.knowledgeExecutor()
		var injected bool
		if exec != nil && exec.Service() != nil {
			for _, conn := range exec.Service().ListConnectors(userID) {
				if !conn.Enabled {
					continue
				}
				ref := domain.KnowledgeRef{ConnectorID: conn.ID, Mode: "all"}
				if v, _, ok := e.knowledgeConnectorMCP(knowledge.ProxyClaims{ProjectID: projectID, UserID: userID, AgentID: agent.ID, Ref: &ref}); ok {
					bundle.MCPServers = append(bundle.MCPServers, v)
					injected = true
				}
			}
		}
		if injected {
			bundle.Rules = append(bundle.Rules, knowledge.UsageRule())
		}
	}
	e.appendWorkflowGlobalRules(&bundle, projectID, workflowID)
	return bundle
}

func (e *Engine) resolveNodeAssets(ctx context.Context, agent domain.AgentDefinition, run domain.Run, wfID string, node domain.WorkflowNode) domain.AgentAssetBundle {
	project := run.ExecutionContext.ProjectID
	uid := run.ExecutionContext.ResponsibleUserID
	exec := e.knowledgeExecutor()

	// Priority (A-Union):
	// 1) Explicit run-level override (ExecutionContext.KnowledgeRefs != nil) wins exclusively.
	// 2) Otherwise, union of node-level KnowledgeRefs (taking precedence per ConnectorID)
	//    and role-level KnowledgeBase toggle (appending remaining enabled connectors owned by uid with mode="all").
	var userConnectors []domain.KnowledgeConnector
	if agent.KnowledgeBase && exec != nil && exec.Service() != nil {
		userConnectors = exec.Service().ListConnectors(uid)
	}
	effectiveRefs := mergeNodeAndRoleKnowledgeRefs(
		run.ExecutionContext.KnowledgeRefs,
		node.KnowledgeRefs,
		agent.KnowledgeBase,
		userConnectors,
	)

	// Suppress role-level generic tokens so we mint node/run/attempt-bound tokens below.
	agent.KnowledgeBase = false
	bundle := e.resolveAssets(agent, project, wfID, uid)
	attempt := 0
	if nr := run.Nodes[node.ID]; nr != nil {
		attempt = nr.Attempt
	}

	var attached []domain.MCPServerDefinition
	var attachedRefs []domain.KnowledgeRef
	for _, ref := range effectiveRefs {
		r := ref
		srv, _, ok := e.knowledgeConnectorMCP(knowledge.ProxyClaims{
			ProjectID: project,
			UserID:    uid,
			AgentID:   agent.ID,
			RunID:     run.ID,
			NodeID:    node.ID,
			Attempt:   attempt,
			Ref:       &r,
		})
		if !ok {
			continue
		}
		bundle.MCPServers = append(bundle.MCPServers, srv)
		attached = append(attached, srv)
		attachedRefs = append(attachedRefs, r)
	}
	if len(attached) > 0 {
		catalog := ""
		if exec != nil {
			catalog = exec.BuildCatalogXML(ctx, uid, attachedRefs, "sop", func(conn domain.KnowledgeConnector, toolName string) string {
				return "mcp__" + domain.KnowledgeMCPServerID + "-" + conn.ID + "__" + toolName
			})
		}
		if catalog == "" {
			catalog = fallbackSOPKnowledgeCatalog(attached)
		}
		bundle.Rules = append(bundle.Rules, domain.RuleDefinition{
			ID:          domain.SOPKnowledgeUsageRuleID,
			Name:        "节点知识库使用规范",
			Description: "SOP 节点执行前先核对并检索当前执行人绑定的个人知识库，引用附精准出处",
			Content:     domain.SOPKnowledgeLookupInstruction + "\n" + catalog,
			Priority:    60,
		})
	}
	return bundle
}

// mergeNodeAndRoleKnowledgeRefs computes the effective KnowledgeRefs for an SOP node:
//   - If runOverride != nil: explicit run-level override wins exclusively.
//   - Otherwise: nodeRefs take precedence per ConnectorID (preserving allowlist/sources),
//     and if roleToggle is true, any other enabled connectors owned by the responsible
//     user are appended with mode="all" (A-Union).
func mergeNodeAndRoleKnowledgeRefs(
	runOverride *[]domain.KnowledgeRef,
	nodeRefs []domain.KnowledgeRef,
	roleToggle bool,
	userConnectors []domain.KnowledgeConnector,
) []domain.KnowledgeRef {
	if runOverride != nil {
		return append([]domain.KnowledgeRef(nil), (*runOverride)...)
	}
	seen := make(map[string]bool, len(nodeRefs)+len(userConnectors))
	var out []domain.KnowledgeRef
	for _, ref := range nodeRefs {
		id := strings.TrimSpace(ref.ConnectorID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, ref)
	}
	if roleToggle {
		for _, conn := range userConnectors {
			if !conn.Enabled || seen[conn.ID] {
				continue
			}
			seen[conn.ID] = true
			out = append(out, domain.KnowledgeRef{ConnectorID: conn.ID, Mode: "all"})
		}
	}
	return out
}

func fallbackSOPKnowledgeCatalog(servers []domain.MCPServerDefinition) string {
	xmlReplacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	esc := func(s string) string { return xmlReplacer.Replace(s) }
	var b strings.Builder
	b.WriteString("<bound_knowledge_bases>\n")
	for _, s := range servers {
		fmt.Fprintf(&b, "  <kb id=\"%s\" name=\"%s\" tools=\"mcp__%s__*\">\n", esc(s.ID), esc(s.Name), esc(s.ID))
		if desc := strings.TrimSpace(s.Description); desc != "" {
			fmt.Fprintf(&b, "    <description>%s</description>\n", esc(desc))
		}
		b.WriteString("  </kb>\n")
	}
	b.WriteString("</bound_knowledge_bases>")
	return b.String()
}

// roleMCPServerVisible reports whether a catalog MCP server listed on a role
// may be injected for this run: servers without a scope are skipped, project
// servers must belong to the run's project, and personal servers must be the
// caller's own and not installed from the market.
func roleMCPServerVisible(server domain.MCPServerDefinition, projectID string, userID int64) bool {
	switch server.Scope {
	case "":
		return false
	case "project":
		return server.ProjectID == projectID
	case "personal":
		return server.OwnerUserID == userID && server.MarketID == ""
	default:
		return true
	}
}

// appendWorkflowGlobalRules injects project rules that were marked global and
// bound to this LOOP. Every agent node of the workflow receives them, even when
// the node's role does not list the rule. Script and approval nodes do not
// resolve an agent asset bundle, so they are unchanged.
func (e *Engine) appendWorkflowGlobalRules(bundle *domain.AgentAssetBundle, projectID, workflowID string) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" || e == nil || e.repo == nil {
		return
	}
	have := make(map[string]struct{}, len(bundle.Rules))
	for _, rule := range bundle.Rules {
		have[rule.ID] = struct{}{}
	}
	for _, rule := range e.repo.Rules() {
		if !rule.Global {
			continue
		}
		if _, ok := have[rule.ID]; ok {
			continue
		}
		if rule.ProjectID != projectID && !rule.Shared {
			continue
		}
		if !ruleTargetsWorkflow(rule, workflowID) {
			continue
		}
		bundle.Rules = append(bundle.Rules, rule)
		have[rule.ID] = struct{}{}
	}
}

func ruleTargetsWorkflow(rule domain.RuleDefinition, workflowID string) bool {
	for _, id := range rule.WorkflowIDs {
		if strings.TrimSpace(id) == workflowID {
			return true
		}
	}
	return false
}

func workflowAgent(st *repo.Store, wf domain.WorkflowDefinition, id string) (domain.AgentDefinition, bool) {
	if domain.IsPreviewBuildAgent(id) {
		return domain.PreviewBuildAgent(wf.Preview), true
	}
	if wf.Generic {
		if template, ok := st.GenericTemplateByWorkflow(wf.ID); ok {
			for _, role := range template.Agents {
				if role.ID == id {
					return role, true
				}
			}
		}
	}
	if agent, ok := st.Agent(id); ok {
		if st.AgentShadowsTemplateOutside(agent, wf.ProjectID) {
			return st.GenericAgentByID(id)
		}
		if wf.ID == BaselineAlignWorkflowID && id == BaselineAlignAgentID {
			for _, node := range wf.Nodes {
				if node.Type == "agent" && node.AgentID == id && node.ExecutorID != "" {
					if agent.ExecutorID != node.ExecutorID {
						agent.ExecutorID, agent.Model, agent.Effort = node.ExecutorID, "", ""
					}
					break
				}
			}
		}
		return agent, true
	}
	return st.GenericAgentByID(id)
}

// projectSandbox resolves a generic template's preferred sandbox without ever
// crossing project boundaries. Project-owned profiles are preferred after an
// explicitly visible preferred profile, then shared/platform profiles.
func projectSandbox(st *repo.Store, preferred, projectID string) (domain.SandboxProfile, bool) {
	if profile, ok := st.Sandbox(preferred); ok && (profile.ProjectID == projectID || profile.ProjectID == "" || profile.Shared) {
		return profile, true
	}
	profiles := st.Sandboxes()
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })
	for _, profile := range profiles {
		if profile.ProjectID == projectID {
			return profile, true
		}
	}
	for _, profile := range profiles {
		if profile.ProjectID == "" || profile.Shared {
			return profile, true
		}
	}
	return domain.SandboxProfile{}, false
}

// projectMarketMCP finds the project-scoped install of a market MCP. Generic
// roles bind catalog ids (61) or aliases (gongfeng); the install stores the
// catalog id on MarketID, so alias lookup also matches the market URL path.
func (e *Engine) projectMarketMCP(marketID, projectID string) (domain.MCPServerDefinition, bool) {
	marketID = strings.TrimSpace(marketID)
	if marketID == "" || strings.TrimSpace(projectID) == "" {
		return domain.MCPServerDefinition{}, false
	}
	for _, v := range e.repo.MCPServers() {
		if v.Scope == "project" && v.ProjectID == projectID && projectMarketMCPMatches(v, marketID) {
			return v, true
		}
	}
	return domain.MCPServerDefinition{}, false
}

func projectMarketMCPMatches(server domain.MCPServerDefinition, ref string) bool {
	ref = strings.TrimSpace(ref)
	marketID := strings.TrimSpace(server.MarketID)
	if ref == "" || marketID == "" {
		return false
	}
	if strings.EqualFold(marketID, ref) {
		return true
	}
	u := strings.ToLower(strings.TrimRight(strings.TrimSpace(server.URL), "/"))
	return strings.HasSuffix(u, "/"+strings.ToLower(ref))
}

func (e *Engine) resolveScriptMCPAssets(projectID string, userID int64) domain.AgentAssetBundle {
	bundle := domain.AgentAssetBundle{}
	projectID = strings.TrimSpace(projectID)
	for _, v := range e.repo.MCPServers() {
		if v.Scope == "project" && strings.TrimSpace(v.ProjectID) == projectID {
			bundle.MCPServers = append(bundle.MCPServers, v)
			e.loadMCPFiles(&bundle, v)
			continue
		}
		if v.Scope == "personal" && v.OwnerUserID == userID && strings.TrimSpace(v.MarketID) == "" {
			bundle.MCPServers = append(bundle.MCPServers, v)
			e.loadMCPFiles(&bundle, v)
		}
	}
	return bundle
}

func (e *Engine) loadMCPFiles(bundle *domain.AgentAssetBundle, server domain.MCPServerDefinition) {
	if bundle == nil || len(server.FileRefs) == 0 {
		return
	}
	if contents := e.repo.MCPFileContents(server.ID); len(contents) > 0 {
		if bundle.MCPFiles == nil {
			bundle.MCPFiles = map[string]map[string][]byte{}
		}
		bundle.MCPFiles[server.ID] = contents
	}
}

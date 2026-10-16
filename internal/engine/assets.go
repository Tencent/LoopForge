package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/repo"
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
		Name:        "AgentFlow 平台",
		Scope:       "project",
		Description: "AgentFlow 平台侧能力：需求、任务、运行、项目等平台操作以 MCP 工具提供",
		Transport:   "http",
		URL:         base + "/mcp",
		TimeoutSec:  60,
	}
	if token != "" {
		def.Headers = map[string]string{"Authorization": "Bearer " + token}
	}
	return def
}

// knowledgeMCPServer materializes the reserved read-only knowledge proxy with
// a fresh project-scoped token. It degrades to a no-op while the knowledge
// base is not connected, so turning the role toggle on never fails a run.
func (e *Engine) knowledgeMCPServer(agentID, projectID string, userID int64) (domain.MCPServerDefinition, bool) {
	if e.repo == nil {
		return domain.MCPServerDefinition{}, false
	}
	e.mu.Lock()
	base := e.platformMCPBase
	e.mu.Unlock()
	settings := e.repo.KnowledgeSettings()
	if !settings.Ready() {
		return domain.MCPServerDefinition{}, false
	}
	token, err := knowledge.SignProxyToken(settings, knowledge.ProxyClaims{ProjectID: projectID, UserID: userID, AgentID: agentID}, time.Now())
	if err != nil {
		return domain.MCPServerDefinition{}, false
	}
	return knowledge.MCPServerDefinition(base, token), true
}

// knowledgeSkippedWarning explains a knowledge toggle that resolveAssets could
// not honor, so the node log says why instead of the agent silently lacking
// the tools.
func knowledgeSkippedWarning(agent domain.AgentDefinition, assets domain.AgentAssetBundle) string {
	if !agent.KnowledgeBase {
		return ""
	}
	for _, server := range assets.MCPServers {
		if server.ID == domain.KnowledgeMCPServerID {
			return ""
		}
	}
	return "角色开启了团队知识库，但未注入 " + domain.KnowledgeMCPServerID + "：知识中心未启用或未配置知识库地址/Token"
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
			if v.Scope == "" {
				continue
			}
			if v.Scope == "project" && v.ProjectID != projectID {
				continue
			}
			if v.Scope == "personal" && v.OwnerUserID != userID {
				continue
			}
			if v.Scope == "personal" && v.MarketID != "" {
				continue
			}
			bundle.MCPServers = append(bundle.MCPServers, v)
			if len(v.FileRefs) > 0 {
				if contents := e.repo.MCPFileContents(v.ID); len(contents) > 0 {
					if bundle.MCPFiles == nil {
						bundle.MCPFiles = map[string]map[string][]byte{}
					}
					bundle.MCPFiles[v.ID] = contents
				}
			}
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
		if v, ok := e.knowledgeMCPServer(agent.ID, projectID, userID); ok {
			bundle.MCPServers = append(bundle.MCPServers, v)
			bundle.Rules = append(bundle.Rules, knowledge.UsageRule())
		}
	}
	e.appendWorkflowGlobalRules(&bundle, projectID, workflowID)
	return bundle
}

// appendWorkflowGlobalRules injects project rules that were marked global and
// bound to this SOP. Every agent node of the workflow receives them, even when
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
	if agent, ok := st.Agent(id); ok {
		if st.AgentShadowsTemplateOutside(agent, wf.ProjectID) {
			return st.GenericAgentByID(id)
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

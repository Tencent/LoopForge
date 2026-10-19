package repo

import "agentflow/internal/domain"

// SeedTestGenericTemplates inserts compact generic_templates rows so tests can
// exercise listing, clone, and catalog lookups without shipping playbook text
// in the production binary.
func SeedTestGenericTemplates(s *Store) error {
	if s == nil {
		return nil
	}
	for _, template := range []GenericTemplate{testBackendGenericTemplate(), testNamedGenericTemplate(GenericProductDeliveryWorkflowID, "通用产品交付 SOP", GenericAgentProductPRD), testNamedGenericTemplate(GenericFrontendDeliveryWorkflowID, "通用前端交付 SOP", GenericAgentFrontendShape)} {
		if err := s.PutGenericTemplate(template); err != nil {
			return err
		}
	}
	return nil
}

func testNamedGenericTemplate(id, name, agentID string) GenericTemplate {
	return GenericTemplate{
		Workflow: domain.WorkflowDefinition{
			ID: id, Name: name, Version: 1, Revision: 1, Status: "published",
			Owner: "平台", Description: name, SandboxProfileID: GenericDockerSandboxID,
			Generic: true,
			Nodes: []domain.WorkflowNode{
				{ID: "work", Name: "执行", Type: "agent", AgentID: agentID, MaxAttempts: 1, ArtifactOutputs: []domain.ArtifactContract{{Name: "report", Path: "report.md", Required: true}}},
			},
		},
		Agents: []domain.AgentDefinition{{
			ID: agentID, Name: name + "角色", Version: 1, ExecutorID: "codebuddy",
			Instructions: "test role",
			TaskPrompt:   &domain.TaskPromptSpec{Objective: "完成任务", Inputs: []string{"任务输入：{{input}}"}},
		}},
	}
}

func testBackendGenericTemplate() GenericTemplate {
	agents := []struct {
		id, name string
		mcp      []string
		skills   []string
	}{
		{GenericAgentRequirement, "需求定型", nil, []string{GenericSkillClarifyRequirements}},
		{GenericAgentDesign, "方案设计", nil, []string{GenericSkillTechDesign, GenericSkillWritingPlans}},
		{GenericAgentImplement, "实现", nil, []string{GenericSkillBrainstorming}},
		{GenericAgentReview, "独立评审", nil, []string{GenericSkillKnowledgeDistill}},
		{GenericAgentTest, "测试验证", nil, nil},
		{GenericAgentMR, "发起 MR", []string{"gongfeng"}, nil},
		{GenericAgentSummary, "交付汇总", nil, nil},
	}
	outAgents := make([]domain.AgentDefinition, 0, len(agents))
	nodes := make([]domain.WorkflowNode, 0, len(agents))
	for i, spec := range agents {
		outAgents = append(outAgents, domain.AgentDefinition{
			ID: spec.id, Name: spec.name, Version: 1, ExecutorID: "codebuddy",
			Instructions: "test role",
			Rules:        []string{GenericRuleCore, GenericRuleBackendProfile},
			Skills:       spec.skills,
			MCPServers:   spec.mcp,
			TaskPrompt:   &domain.TaskPromptSpec{Objective: spec.name, Inputs: []string{"任务输入：{{input}}"}},
		})
		nodeID := spec.id
		switch spec.id {
		case GenericAgentRequirement:
			nodeID = "requirement"
		case GenericAgentDesign:
			nodeID = "design"
		case GenericAgentImplement:
			nodeID = "implement"
		case GenericAgentReview:
			nodeID = "review"
		case GenericAgentTest:
			nodeID = "test"
		case GenericAgentMR:
			nodeID = "mr"
		case GenericAgentSummary:
			nodeID = "summary"
		}
		nodes = append(nodes, domain.WorkflowNode{
			ID: nodeID, Name: spec.name, Type: "agent", AgentID: spec.id, MaxAttempts: 1,
			ArtifactOutputs: []domain.ArtifactContract{{Name: nodeID + "-report", Path: nodeID + "-report.md", Required: true}},
			Position:        domain.Position{X: float64(80 + i*80), Y: 200},
		})
	}
	return GenericTemplate{
		Workflow: domain.WorkflowDefinition{
			ID: GenericBackendDeliveryWorkflowID, Name: "通用后端交付 SOP（LoopForge）",
			Version: 1, Revision: 1, Status: "published", Owner: "平台",
			Description:      "测试用通用后端 SOP",
			SandboxProfileID: GenericDockerSandboxID, Generic: true,
			Requirements: []domain.WorkflowRequirement{
				{Key: RequirementGongfengGitToken, NodeID: "mr", Label: "工蜂 Git Token", Hint: "push 需要 Git Token", Setup: "settings"},
				{Key: RequirementGongfengMCP, NodeID: "mr", Label: "工蜂 MCP", Hint: "创建 MR 需要工蜂 MCP", Setup: "mcp"},
				{Key: RequirementTaihuPAT, NodeID: "mr", Label: "太湖个人令牌", Hint: "工蜂 MCP 走太湖认证", Setup: "settings"},
			},
			Nodes: nodes,
			Edges: []domain.WorkflowEdge{
				{ID: "e1", Source: "requirement", Target: "design", When: "success"},
				{ID: "e2", Source: "design", Target: "implement", When: "success"},
				{ID: "e3", Source: "implement", Target: "review", When: "success"},
				{ID: "e4", Source: "review", Target: "test", When: "success"},
				{ID: "e5", Source: "test", Target: "mr", When: "success"},
				{ID: "e6", Source: "mr", Target: "summary", When: "success"},
			},
		},
		Agents: outAgents,
		Rules: []domain.RuleDefinition{
			{ID: GenericRuleCore, Name: "LoopForge 核心", Content: "follow the stored template"},
			{ID: GenericRuleBackendProfile, Name: "后端画像", Content: "backend delivery"},
		},
		Skills: []domain.SkillDefinition{
			{ID: GenericSkillClarifyRequirements, Name: "澄清需求", Content: "clarify"},
			{ID: GenericSkillBrainstorming, Name: "方案头脑风暴", Content: "brainstorm"},
			{ID: GenericSkillTechDesign, Name: "技术方案", Content: "design"},
			{ID: GenericSkillWritingPlans, Name: "执行计划", Content: "plan"},
			{ID: GenericSkillKnowledgeDistill, Name: "知识蒸馏", Content: "distill"},
		},
	}
}

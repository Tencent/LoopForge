package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
)

func (r *CatalogRepo) loadGenericTemplate(id string) (GenericTemplate, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return GenericTemplate{}, false
	}
	var row genericTemplateRow
	if err := r.DB.Where("id = ?", id).Take(&row).Error; err != nil || len(row.Payload) == 0 {
		return GenericTemplate{}, false
	}
	template, err := decodeGenericTemplate(row.Payload)
	if err != nil {
		return GenericTemplate{}, false
	}
	return template, true
}

func decodeGenericTemplate(raw []byte) (GenericTemplate, error) {
	var template GenericTemplate
	if err := json.Unmarshal(raw, &template); err != nil {
		return GenericTemplate{}, err
	}
	if strings.TrimSpace(template.Workflow.ID) == "" {
		return GenericTemplate{}, errors.New("generic template missing workflow id")
	}
	template.Workflow.Generic = true
	template.Workflow.ProjectID = ""
	template.Workflow.Shared = false
	return template, nil
}

func (r *CatalogRepo) putGenericTemplate(template GenericTemplate) error {
	id := strings.TrimSpace(template.Workflow.ID)
	if id == "" {
		return errors.New("generic template workflow id is required")
	}
	template.Workflow.ID = id
	template.Workflow.Generic = true
	template.Workflow.ProjectID = ""
	template.Workflow.Shared = false
	if err := r.validateWorkflowNotifications(template.Workflow); err != nil {
		return err
	}
	now := time.Now().UTC()
	if template.Workflow.UpdatedAt.IsZero() {
		template.Workflow.UpdatedAt = now
	}
	name := strings.TrimSpace(template.Workflow.Name)
	if name == "" {
		name = id
	}
	return upsert(r.DB, "generic_templates", map[string]any{
		"id":         id,
		"name":       name,
		"payload":    mustJSON(template),
		"updated_at": timePtr(template.Workflow.UpdatedAt),
	})
}

// PutGenericTemplate replaces a stored generic template. Callers must already
// have checked platform-admin authorization.
func (r *CatalogRepo) PutGenericTemplate(template GenericTemplate) error {
	return r.putGenericTemplate(template)
}

// UpdateGenericWorkflow writes the workflow half of a stored template and
// leaves the bundled roles, rules and skills untouched.
func (r *CatalogRepo) UpdateGenericWorkflow(workflow domain.WorkflowDefinition) (domain.WorkflowDefinition, error) {
	if err := domain.ValidateWorkflowPreparation(workflow); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	if err := domain.ValidateWorkflowMetricStages(workflow); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	template, ok := r.GenericTemplateByWorkflow(workflow.ID)
	if !ok {
		return domain.WorkflowDefinition{}, errors.New("generic template not found")
	}
	old := template.Workflow
	if workflow.Revision != old.Revision {
		return domain.WorkflowDefinition{}, fmt.Errorf("workflow revision conflict: current revision is %d", old.Revision)
	}
	workflow.Revision = old.Revision + 1
	now := time.Now().UTC()
	workflow.Generic = true
	workflow.ProjectID = ""
	workflow.Shared = false
	if workflow.Version == 0 {
		workflow.Version = template.Workflow.Version
	}
	if workflow.Version == 0 {
		workflow.Version = 1
	}
	if workflow.Status == "" {
		workflow.Status = template.Workflow.Status
	}
	if workflow.CreatedAt.IsZero() {
		workflow.CreatedAt = template.Workflow.CreatedAt
	}
	if len(workflow.Requirements) == 0 {
		workflow.Requirements = template.Workflow.Requirements
	}
	if strings.TrimSpace(workflow.SandboxProfileID) == "" {
		workflow.SandboxProfileID = template.Workflow.SandboxProfileID
	}
	workflow.UpdatedAt = now
	template.Workflow = workflow
	if err := r.snapshotRevision("workflow", old.ID, old.Name, old.Version, old.Revision, old); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	if err := r.putGenericTemplate(template); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	return workflow, nil
}

// UpdateGenericAgent replaces one bundled role on a stored template. The
// workflow graph, rules and skills stay untouched, and nothing is written
// into the project agents table.
func (r *CatalogRepo) UpdateGenericAgent(workflowID string, agent domain.AgentDefinition) (domain.AgentDefinition, error) {
	template, ok := r.GenericTemplateByWorkflow(workflowID)
	if !ok {
		return domain.AgentDefinition{}, errors.New("generic template not found")
	}
	id := strings.TrimSpace(agent.ID)
	if id == "" {
		return domain.AgentDefinition{}, errors.New("agent id is required")
	}
	index := -1
	for i, existing := range template.Agents {
		if existing.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return domain.AgentDefinition{}, fmt.Errorf("角色 %q 不属于该通用模板", id)
	}
	existing := template.Agents[index]
	agent.ID = id
	agent.ProjectID = ""
	agent.Shared = false
	if agent.Version != 0 && agent.Version != existing.Version {
		return domain.AgentDefinition{}, fmt.Errorf("角色 %q 版本冲突：当前版本已是 %d，请刷新后重改", id, existing.Version)
	}
	agent.Version = existing.Version + 1
	if agent.UpdatedAt.IsZero() {
		agent.UpdatedAt = time.Now().UTC()
	}
	template.Agents[index] = agent
	if err := r.putGenericTemplate(template); err != nil {
		return domain.AgentDefinition{}, err
	}
	return agent, nil
}

// PublishGenericWorkflow bumps the stored template version and marks it published.
func (r *CatalogRepo) PublishGenericWorkflow(id string) (domain.WorkflowDefinition, error) {
	template, ok := r.GenericTemplateByWorkflow(id)
	if !ok {
		return domain.WorkflowDefinition{}, errors.New("generic template not found")
	}
	template.Workflow.Status = "published"
	template.Workflow.Version++
	if template.Workflow.Version < 1 {
		template.Workflow.Version = 1
	}
	template.Workflow.UpdatedAt = time.Now().UTC()
	if err := r.putGenericTemplate(template); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	return template.Workflow, nil
}

// GenericTemplates returns every stored generic SOP.
func (r *CatalogRepo) GenericTemplates() []GenericTemplate {
	var rows []genericTemplateRow
	if err := r.DB.Order("id ASC").Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]GenericTemplate, 0, len(rows))
	for _, row := range rows {
		template, err := decodeGenericTemplate(row.Payload)
		if err != nil {
			continue
		}
		out = append(out, template)
	}
	return out
}

// GenericTemplateByWorkflow resolves a generic template by workflow id.
func (r *CatalogRepo) GenericTemplateByWorkflow(id string) (GenericTemplate, bool) {
	return r.loadGenericTemplate(id)
}

// GenericWorkflowByID resolves a generic workflow definition by id.
func (r *CatalogRepo) GenericWorkflowByID(id string) (domain.WorkflowDefinition, bool) {
	template, ok := r.GenericTemplateByWorkflow(id)
	if !ok {
		return domain.WorkflowDefinition{}, false
	}
	return template.Workflow, true
}

// GenericAgentByID resolves a template role by id across stored templates.
func (r *CatalogRepo) GenericAgentByID(id string) (domain.AgentDefinition, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.AgentDefinition{}, false
	}
	for _, template := range r.GenericTemplates() {
		for _, agent := range template.Agents {
			if agent.ID == id {
				return agent, true
			}
		}
	}
	return domain.AgentDefinition{}, false
}

// AgentShadowsTemplateOutside reports whether a stored role only binds inside
// its own project because it reuses a template role id: workflows in any other
// project (including the generic template itself) run the built-in role.
func (r *CatalogRepo) AgentShadowsTemplateOutside(agent domain.AgentDefinition, workflowProjectID string) bool {
	if strings.TrimSpace(agent.ProjectID) == "" || agent.Shared || agent.ProjectID == workflowProjectID {
		return false
	}
	_, inTemplate := r.GenericAgentByID(agent.ID)
	return inTemplate
}

// WorkflowNamesUsingAgent lists the SOPs whose nodes resolve to this stored
// role at run time.
func (r *CatalogRepo) WorkflowNamesUsingAgent(agent domain.AgentDefinition) []string {
	var names []string
	for _, workflow := range r.Workflows() {
		if r.AgentShadowsTemplateOutside(agent, workflow.ProjectID) {
			continue
		}
		for _, node := range workflow.Nodes {
			if strings.TrimSpace(node.AgentID) == agent.ID {
				names = append(names, workflow.Name)
				break
			}
		}
	}
	return names
}

// GenericRuleByID resolves a template rule by id across stored templates.
func (r *CatalogRepo) GenericRuleByID(id string) (domain.RuleDefinition, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.RuleDefinition{}, false
	}
	for _, template := range r.GenericTemplates() {
		for _, rule := range template.Rules {
			if rule.ID == id {
				return rule, true
			}
		}
	}
	return domain.RuleDefinition{}, false
}

// GenericSkillByID resolves a template skill by id across stored templates.
func (r *CatalogRepo) GenericSkillByID(id string) (domain.SkillDefinition, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.SkillDefinition{}, false
	}
	for _, template := range r.GenericTemplates() {
		for _, skill := range template.Skills {
			if skill.ID == id {
				return skill, true
			}
		}
	}
	return domain.SkillDefinition{}, false
}

// GenericWorkflows returns every generic workflow definition from the store.
func (r *CatalogRepo) GenericWorkflows() []domain.WorkflowDefinition {
	templates := r.GenericTemplates()
	out := make([]domain.WorkflowDefinition, 0, len(templates))
	for _, template := range templates {
		out = append(out, template.Workflow)
	}
	return out
}

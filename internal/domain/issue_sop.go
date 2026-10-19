package domain

import (
	"fmt"
	"sort"
	"strings"
)

// IssueSOPBinding is one step in a requirement's SOP chain.
type IssueSOPBinding struct {
	ID             string   `json:"id,omitempty"`
	WorkflowID     string   `json:"workflowId"`
	Order          int      `json:"order"`
	AssigneeUserID int64    `json:"assigneeUserId,omitempty"`
	DependsOn      []string `json:"dependsOn,omitempty"`
	Handoff        []string `json:"handoff,omitempty"`
}

// EffectiveSOPBindings returns the explicit chain, or synthesizes a single
// step from WorkflowID so old tickets keep working.
func (i Issue) EffectiveSOPBindings() []IssueSOPBinding {
	if i.SOPBindings != nil {
		return NormalizeSOPBindings(i.SOPBindings)
	}
	return bindingsFromWorkflow(i.WorkflowID, i.AssigneeUserID)
}

// SOPPlaybook is a named, reusable SOP combo for requirements in a project.
type SOPPlaybook struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Default  bool              `json:"default,omitempty"`
	Bindings []IssueSOPBinding `json:"bindings,omitempty"`
}

func NormalizeSOPPlaybooks(in []SOPPlaybook) []SOPPlaybook {
	seen := map[string]bool{}
	out := make([]SOPPlaybook, 0, len(in))
	for i, book := range in {
		book.Name = strings.TrimSpace(book.Name)
		book.ID = strings.TrimSpace(book.ID)
		if book.ID == "" {
			book.ID = "pb-" + strings.TrimSpace(book.Name)
			if book.ID == "pb-" {
				book.ID = fmt.Sprintf("pb-%d", i+1)
			}
		}
		if seen[book.ID] {
			continue
		}
		seen[book.ID] = true
		if book.Name == "" {
			book.Name = "未命名套餐"
		}
		book.Bindings = NormalizeSOPBindings(book.Bindings)
		out = append(out, book)
	}
	hasDefault := false
	for _, book := range out {
		if book.Default {
			hasDefault = true
			break
		}
	}
	if !hasDefault && len(out) > 0 {
		out[0].Default = true
	}
	if hasDefault {
		found := false
		for i := range out {
			if out[i].Default && !found {
				found = true
				continue
			}
			out[i].Default = false
		}
	}
	return out
}

// Playbooks returns named combos, wrapping a legacy single SOPBindings chain
// as one default combo when SOPPlaybooks is empty.
func (p Project) Playbooks() []SOPPlaybook {
	if len(p.SOPPlaybooks) > 0 {
		return NormalizeSOPPlaybooks(p.SOPPlaybooks)
	}
	bindings := NormalizeSOPBindings(p.SOPBindings)
	if len(bindings) == 0 {
		bindings = bindingsFromWorkflow(p.WorkflowID, 0)
	}
	if len(bindings) == 0 {
		return nil
	}
	return []SOPPlaybook{{ID: "default", Name: "默认流程", Default: true, Bindings: bindings}}
}

func (p Project) Playbook(id string) (SOPPlaybook, bool) {
	id = strings.TrimSpace(id)
	for _, book := range p.Playbooks() {
		if book.ID == id {
			return book, true
		}
	}
	return SOPPlaybook{}, false
}

func (p Project) DefaultPlaybook() (SOPPlaybook, bool) {
	books := p.Playbooks()
	for _, book := range books {
		if book.Default {
			return book, true
		}
	}
	if len(books) > 0 {
		return books[0], true
	}
	return SOPPlaybook{}, false
}

func (p Project) EffectiveSOPBindings() []IssueSOPBinding {
	if book, ok := p.DefaultPlaybook(); ok {
		return append([]IssueSOPBinding(nil), book.Bindings...)
	}
	return nil
}

func ResolveSOPBindings(issue Issue, project Project) []IssueSOPBinding {
	if issue.SOPBindings != nil {
		return NormalizeSOPBindings(issue.SOPBindings)
	}
	return issue.EffectiveSOPBindings()
}

// SOPBindingForWorkflow returns an explicit chain step for a workflow.
func (i Issue) SOPBindingForWorkflow(workflowID string) (IssueSOPBinding, bool) {
	return bindingForWorkflow(NormalizeSOPBindings(i.SOPBindings), workflowID)
}

// ResolveSOPBinding looks up a step on the resolved (inherited) chain.
func ResolveSOPBinding(issue Issue, project Project, workflowID string) (IssueSOPBinding, bool) {
	return bindingForWorkflow(ResolveSOPBindings(issue, project), workflowID)
}

func bindingForWorkflow(bindings []IssueSOPBinding, workflowID string) (IssueSOPBinding, bool) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return IssueSOPBinding{}, false
	}
	for _, binding := range bindings {
		if binding.WorkflowID == workflowID {
			return binding, true
		}
	}
	return IssueSOPBinding{}, false
}

func bindingsFromWorkflow(workflowID string, assigneeUserID int64) []IssueSOPBinding {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return nil
	}
	return []IssueSOPBinding{{
		ID:             "sop-default",
		WorkflowID:     wf,
		Order:          1,
		AssigneeUserID: assigneeUserID,
	}}
}

func NormalizeSOPBindings(in []IssueSOPBinding) []IssueSOPBinding {
	type item struct {
		binding IssueSOPBinding
		index   int
	}
	seen := map[string]bool{}
	items := make([]item, 0, len(in))
	for i, binding := range in {
		wf := strings.TrimSpace(binding.WorkflowID)
		if wf == "" || seen[wf] {
			continue
		}
		seen[wf] = true
		binding.WorkflowID = wf
		binding.ID = strings.TrimSpace(binding.ID)
		if binding.ID == "" {
			binding.ID = "sop-" + wf
		}
		binding.Handoff = compactStrings(binding.Handoff)
		binding.DependsOn = compactStrings(binding.DependsOn)
		if binding.Order <= 0 {
			binding.Order = (i + 1) * 10
		}
		items = append(items, item{binding: binding, index: i})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].binding.Order == items[j].binding.Order {
			return items[i].index < items[j].index
		}
		return items[i].binding.Order < items[j].binding.Order
	})
	ids := map[string]bool{}
	out := make([]IssueSOPBinding, 0, len(items))
	for i, item := range items {
		binding := item.binding
		binding.Order = i + 1
		ids[binding.ID] = true
		out = append(out, binding)
	}
	for i, binding := range out {
		deps := make([]string, 0, len(binding.DependsOn))
		for _, id := range binding.DependsOn {
			if ids[id] && id != binding.ID {
				deps = append(deps, id)
			}
		}
		out[i].DependsOn = deps
	}
	return out
}

func compactStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		value := strings.TrimSpace(raw)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

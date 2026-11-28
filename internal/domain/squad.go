package domain

// AgentSquadWorkflowID is the workflow id of every agent squad run. It is
// reserved so a saved LOOP never shares run counts, health or filters with
// squad runs.
const AgentSquadWorkflowID = "agent-squad"

// AgentSquad is frozen with the run. Members are the allowed roles, while
// WorkflowSnapshot.Nodes contains only turns actually requested so far.
type AgentSquad struct {
	Members  []SquadMember `json:"members"`
	MaxTurns int           `json:"maxTurns"`
}

type SquadMember struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SquadDecision struct {
	Action  string `json:"action"`
	To      string `json:"to,omitempty"`
	Summary string `json:"summary"`
}

type StartSquadRequest struct {
	MemberIDs        []string `json:"memberIds"` // First member opens the run.
	MaxTurns         int      `json:"maxTurns,omitempty"`
	SandboxProfileID string   `json:"sandboxProfileId,omitempty"`
}

// ExecutionRoles includes potential speakers when provisioning a squad's
// sandbox or choosing its executors, before any handoffs have happened.
func (wf WorkflowDefinition) ExecutionRoles() []WorkflowNode {
	if wf.Squad == nil {
		return wf.Nodes
	}
	nodes := make([]WorkflowNode, 0, len(wf.Squad.Members))
	for _, member := range wf.Squad.Members {
		nodes = append(nodes, WorkflowNode{ID: member.ID, Type: "agent", AgentID: member.ID, Name: member.Name})
	}
	return nodes
}

func (wf WorkflowDefinition) ExecutorDefaultKey(node WorkflowNode) string {
	if wf.Squad != nil {
		return node.AgentID
	}
	return node.ID
}

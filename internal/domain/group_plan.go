package domain

import (
	"time"
)

// FreeCollaborationWorkflowID is the workflow id of retired free collaboration
// runs, including chat workspaces converted from them by migration 020.
const FreeCollaborationWorkflowID = "free-collaboration"

// GroupPlanItem is one shared work item of a retired role group. It is kept
// only so historical run snapshots round-trip without losing data.
type GroupPlanItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail,omitempty"`
	Owner     string    `json:"owner,omitempty"` // agent ID; empty means unassigned
	Status    string    `json:"status"`          // todo, doing, done, blocked
	Note      string    `json:"note,omitempty"`
	NodeID    string    `json:"nodeId,omitempty"` // latest dispatched task for this item
	Revision  int       `json:"revision"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

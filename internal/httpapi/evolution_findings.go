package httpapi

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

type evoFindingRun struct {
	RunID        string    `json:"runId"`
	NodeID       string    `json:"nodeId,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	WorkflowName string    `json:"workflowName,omitempty"`
	IssueTitle   string    `json:"issueTitle,omitempty"`
	Status       string    `json:"status"`
	Detail       string    `json:"detail,omitempty"`
	Unavailable  bool      `json:"unavailable,omitempty"`
}

func (h *evolutionHandlers) findingRuns(c *gin.Context) {
	f, ok := h.repo.EvoFinding(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "问题不存在"})
		return
	}
	if !h.requireProjectReader(c, f.ProjectID) {
		return
	}
	key := c.Query("cluster")
	var occurrences []domain.EvoClusterOccurrence
	nodeID, complete, expected := "", false, 0
	if len(f.Clusters) > 0 {
		var cluster *domain.EvoSignalCluster
		for i := range f.Clusters {
			x := &f.Clusters[i]
			id := x.ID
			if id == "" {
				id = "legacy-" + strconv.Itoa(i)
			}
			if id == key {
				cluster = x
				break
			}
		}
		if cluster == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "报错分组已变化，请刷新问题列表"})
			return
		}
		nodeID, expected = cluster.NodeID, cluster.Runs
		occurrences = cluster.Occurrences
		complete = cluster.ID != "" && len(occurrences) == cluster.Runs
		if len(occurrences) == 0 && cluster.RunID != "" {
			occurrences = []domain.EvoClusterOccurrence{{RunID: cluster.RunID, Detail: cluster.Label, CreatedAt: cluster.LastSeenAt}}
		}
	} else {
		if key != "" {
			c.JSON(http.StatusNotFound, gin.H{"error": "报错分组不存在"})
			return
		}
		for _, sig := range f.Signals {
			occurrences = append(occurrences, domain.EvoClusterOccurrence{RunID: sig.RunID, Detail: sig.Detail, CreatedAt: sig.CreatedAt})
		}
		if len(occurrences) == 0 {
			for _, id := range f.SampleRunIDs {
				occurrences = append(occurrences, domain.EvoClusterOccurrence{RunID: id})
			}
		}
		expected = len(f.SampleRunIDs)
	}
	byRun := map[string]domain.EvoClusterOccurrence{}
	for _, o := range occurrences {
		if o.RunID == "" {
			continue
		}
		old, exists := byRun[o.RunID]
		if !exists || o.CreatedAt.After(old.CreatedAt) {
			byRun[o.RunID] = o
		}
	}
	items := make([]evoFindingRun, 0, len(byRun))
	for id, o := range byRun {
		item := evoFindingRun{RunID: id, NodeID: nodeID, CreatedAt: o.CreatedAt, Status: "unavailable", Unavailable: true}
		run, found := h.repo.RunState(id)
		if found && !run.ExecutionContext.IsEvolutionReplay() && h.runProject(run) == f.ProjectID {
			item.Unavailable = false
			item.Detail = domain.TrimEvoText(o.Detail, 400)
			item.Status = run.Status
			item.WorkflowName = run.WorkflowSnapshot.Name
			item.IssueTitle = domain.TrimEvoText(run.Input, 120)
			if issue, exists := h.repo.Issue(run.ExecutionContext.IssueID); exists && issue.ProjectID == f.ProjectID {
				item.IssueTitle = issue.Title
			}
			if item.CreatedAt.IsZero() {
				item.CreatedAt = run.UpdatedAt
			}
			if n := run.Nodes[nodeID]; n != nil && n.Status == "succeeded" && n.ReworkCount > 0 {
				item.Status = "reworked"
			}
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].RunID < items[j].RunID
	})
	c.JSON(http.StatusOK, gin.H{"items": items, "complete": complete, "total": expected})
}

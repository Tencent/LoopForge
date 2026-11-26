package httpapi

import (
	"net/http"
	"strings"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// Replay comparison: what one replay ran and how each node ended against
// the source run, so a reviewer can see what the candidate body changed
// without opening both runs side by side.

type evoReplayNodeSide struct {
	Status   string `json:"status,omitempty"`
	Rework   int    `json:"rework,omitempty"`
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type evoReplayNode struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Type    string             `json:"type,omitempty"`
	Shaped  bool               `json:"shaped,omitempty"`
	Reused  bool               `json:"reused,omitempty"`
	Changed bool               `json:"changed,omitempty"`
	Source  *evoReplayNodeSide `json:"source,omitempty"`
	Replay  *evoReplayNodeSide `json:"replay,omitempty"`
}

type evoReplayCompare struct {
	ReplayID     string          `json:"replayId"`
	SourceRunID  string          `json:"sourceRunId"`
	RunID        string          `json:"runId,omitempty"`
	WorkflowName string          `json:"workflowName,omitempty"`
	Input        string          `json:"input,omitempty"`
	Nodes        []evoReplayNode `json:"nodes"`
}

const (
	evoReplayInputMax  = 240
	evoReplayReasonMax = 200
)

func evoReplaySide(nr *domain.NodeRun) *evoReplayNodeSide {
	if nr == nil || nr.Status == "" || nr.Status == "pending" {
		return nil
	}
	side := &evoReplayNodeSide{Status: nr.Status, Rework: nr.ReworkCount}
	if g := nr.GateResult; g != nil {
		side.Decision = g.Decision
		side.Reason = domain.TrimEvoText(firstNonEmpty(g.Reason, g.Summary), evoReplayReasonMax)
	} else if nr.Status == "failed" {
		side.Reason = domain.TrimEvoText(nr.Error, evoReplayReasonMax)
	}
	return side
}

// replayCompare lists the source run's and the replay run's nodes in SOP
// order with their end states.
func (h *evolutionHandlers) replayCompare(c *gin.Context) {
	p, ok := h.repo.EvoProposal(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "提案不存在"})
		return
	}
	if !h.requireProjectReader(c, p.ProjectID) {
		return
	}
	var replay domain.EvoReplay
	found := false
	for _, r := range h.repo.EvoReplays(p.ID) {
		if r.ID == c.Param("rid") {
			replay, found = r, true
			break
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "回放不存在"})
		return
	}
	source, ok := h.repo.RunState(replay.SourceRunID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "原运行已被删除"})
		return
	}
	if h.runProject(source) != p.ProjectID {
		c.JSON(http.StatusForbidden, gin.H{"error": "原运行不属于提案项目"})
		return
	}
	out := evoReplayCompare{ReplayID: replay.ID, SourceRunID: replay.SourceRunID, RunID: replay.RunID,
		WorkflowName: source.WorkflowSnapshot.Name, Input: domain.TrimEvoText(source.Input, evoReplayInputMax), Nodes: []evoReplayNode{}}
	shapedRun := source
	var run domain.Run
	hasRun := false
	if replay.RunID != "" {
		if run, hasRun = h.repo.RunState(replay.RunID); hasRun {
			if h.runProject(run) != p.ProjectID {
				c.JSON(http.StatusForbidden, gin.H{"error": "回放运行不属于提案项目"})
				return
			}
			shapedRun = run
		}
	}
	shaped := map[string]bool{}
	if asset, ok := h.loadAsset(p.AssetKind, p.AssetID); ok {
		shaped = h.assetNodeIDs(shapedRun, asset)
	}
	reused := map[string]bool{}
	for _, id := range replay.ReusedNodes {
		reused[id] = true
	}
	for _, node := range shapedRun.WorkflowSnapshot.Nodes {
		item := evoReplayNode{ID: node.ID, Name: strings.TrimSpace(node.Name), Type: node.Type, Shaped: shaped[node.ID], Reused: reused[node.ID]}
		if item.Name == "" {
			item.Name = node.ID
		}
		item.Source = evoReplaySide(source.Nodes[node.ID])
		if hasRun && !item.Reused {
			item.Replay = evoReplaySide(run.Nodes[node.ID])
		}
		if item.Source == nil && item.Replay == nil && !item.Reused {
			continue
		}
		if !item.Reused && (item.Source == nil) != (item.Replay == nil) {
			item.Changed = hasRun
		} else if item.Source != nil && item.Replay != nil {
			item.Changed = item.Source.Status != item.Replay.Status || item.Source.Rework != item.Replay.Rework || item.Source.Decision != item.Replay.Decision
		}
		out.Nodes = append(out.Nodes, item)
	}
	c.JSON(http.StatusOK, out)
}

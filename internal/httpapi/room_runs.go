package httpapi

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// roomRunWatchLimit caps how many unfinished runs one read reconciles, so a
// busy room cannot turn every poll into dozens of run loads.
const roomRunWatchLimit = 12

func roomRunTerminal(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled":
		return true
	}
	return false
}

// roomProgressID is deterministic, so several API instances reconciling the
// same run write the same row instead of duplicating the message.
func roomProgressID(parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	return "prog-" + hex.EncodeToString(sum[:10])
}

func roomTrim(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func firstTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

// roomRunMessages turns the current state of a run into the progress
// messages the room should contain. It is pure so it can be unit tested.
func roomRunMessages(room domain.ChatRoom, dispatch domain.ChatMessage, run domain.Run, agentName func(string) string) []domain.ChatMessage {
	wfName := fallbackName(dispatch.WorkflowName, fallbackName(run.WorkflowSnapshot.Name, run.WorkflowID))
	base := domain.ChatMessage{RoomID: room.ID, IssueID: dispatch.IssueID, IssueNumber: dispatch.IssueNumber, IssueTitle: dispatch.IssueTitle,
		RunID: run.ID, WorkflowID: run.WorkflowID, WorkflowName: wfName}
	var out []domain.ChatMessage
	for _, node := range run.WorkflowSnapshot.Nodes {
		nr := run.Nodes[node.ID]
		if nr == nil {
			continue
		}
		author := "SOP「" + wfName + "」"
		if node.Type == "agent" && strings.TrimSpace(node.AgentID) != "" {
			if name := agentName(node.AgentID); name != "" {
				author = name
			}
		}
		msg := base
		msg.Author = author
		msg.NodeID = node.ID
		msg.NodeName = fallbackName(node.Name, node.ID)
		msg.NodeStatus = nr.Status
		msg.Attempt = nr.Attempt
		attempt := fmt.Sprint(nr.Attempt)
		switch nr.Status {
		case "succeeded":
			msg.ID = roomProgressID(run.ID, node.ID, "succeeded", attempt)
			msg.Kind = domain.ChatKindProgress
			msg.Body = "完成了「" + msg.NodeName + "」"
			if out := roomTrim(nr.Output, 280); out != "" && node.Type == "agent" {
				msg.Body += "\n" + out
			}
			msg.CreatedAt = firstTime(nr.FinishedAt, nr.StartedAt, run.UpdatedAt)
		case "failed":
			msg.ID = roomProgressID(run.ID, node.ID, "failed", attempt)
			msg.Kind = domain.ChatKindProgress
			msg.Body = "「" + msg.NodeName + "」失败"
			if e := roomTrim(nr.Error, 280); e != "" {
				msg.Body += "：" + e
			}
			msg.CreatedAt = firstTime(nr.FinishedAt, nr.StartedAt, run.UpdatedAt)
		case "waiting_approval":
			msg.ID = roomProgressID(run.ID, node.ID, "approval", attempt)
			msg.Kind = domain.ChatKindApproval
			msg.Author = "SOP「" + wfName + "」"
			msg.Body = "「" + msg.NodeName + "」等待审批，可以直接在群里批准"
			if roomRejectTarget(run.WorkflowSnapshot, node.ID) != "" {
				msg.Body += "或打回"
			}
			msg.CreatedAt = firstTime(nr.StartedAt, run.UpdatedAt)
		default:
			continue
		}
		out = append(out, msg)
	}
	if roomRunTerminal(run.Status) {
		msg := base
		msg.ID = roomProgressID(run.ID, "", "final", run.Status)
		msg.Kind = domain.ChatKindProgress
		msg.Author = "SOP「" + wfName + "」"
		msg.NodeStatus = run.Status
		msg.Body = map[string]string{"succeeded": "运行完成", "failed": "运行失败", "cancelled": "运行已取消"}[run.Status]
		msg.CreatedAt = firstTime(run.UpdatedAt)
		out = append(out, msg)
	}
	// Node clocks can predate the card (the run starts before the card is
	// saved); keep the thread order card -> steps -> result.
	floor := dispatch.CreatedAt
	for i := range out {
		if !floor.IsZero() && !out[i].CreatedAt.After(floor) {
			out[i].CreatedAt = floor.Add(time.Duration(i+1) * time.Millisecond)
		}
		floor = out[i].CreatedAt
	}
	return out
}

// roomRunCacheSize bounds the per-process cache of run views used by the
// room poller. Entries are keyed by run id and invalidated by revision.
const roomRunCacheSize = 512

type roomRunCacheEntry struct {
	revision int
	run      domain.Run
}

// roomRunCache keeps the last RunView of runs that rooms are watching, so a
// poll only reloads a run whose revision moved. Cached runs are shared and
// must be treated as read-only.
type roomRunCache struct {
	mu      sync.Mutex
	entries map[string]roomRunCacheEntry
}

func (c *roomRunCache) get(id string, revision int) (domain.Run, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[id]
	if !ok || entry.revision != revision {
		return domain.Run{}, false
	}
	return entry.run, true
}

func (c *roomRunCache) put(id string, run domain.Run) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]roomRunCacheEntry{}
	}
	if _, ok := c.entries[id]; !ok && len(c.entries) >= roomRunCacheSize {
		for key := range c.entries {
			delete(c.entries, key)
			if len(c.entries) < roomRunCacheSize/2 {
				break
			}
		}
	}
	c.entries[id] = roomRunCacheEntry{revision: run.Revision, run: run}
}

// roomRunProgress summarises a run for a dispatch card.
func roomRunProgress(m *domain.ChatMessage, run domain.Run) {
	m.RunStatus = run.Status
	m.RunTotal = len(run.WorkflowSnapshot.Nodes)
	m.RunCompleted = 0
	m.RunCurrent = nil
	for _, node := range run.WorkflowSnapshot.Nodes {
		nr := run.Nodes[node.ID]
		if nr == nil {
			continue
		}
		switch nr.Status {
		case "succeeded":
			m.RunCompleted++
		case "running", "waiting_approval", "pending_input":
			m.RunCurrent = append(m.RunCurrent, fallbackName(node.Name, node.ID))
		}
	}
}

func roomApprovalResolved(m domain.ChatMessage, run domain.Run) bool {
	nr := run.Nodes[m.NodeID]
	return nr == nil || nr.Status != "waiting_approval" || nr.Attempt != m.Attempt || roomRunTerminal(run.Status)
}

// syncRoomRuns reconciles unfinished runs dispatched from this room and
// returns the message list with live run fields filled in.
//
// Cost per poll: one batched revision lookup for the runs still in flight;
// a run view is loaded only when its revision moved since the last poll.
// Settled dispatch cards and resolved approvals are persisted as such and
// never touch the run tables again.
func (h *roomsHandlers) syncRoomRuns(room domain.ChatRoom, messages []domain.ChatMessage) []domain.ChatMessage {
	live := map[string]bool{}
	var ids []string
	watched := 0
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.RunID == "" || live[m.RunID] {
			continue
		}
		switch {
		case m.Kind == domain.ChatKindDispatch && m.RunFinal == "" && watched < roomRunWatchLimit:
			watched++
		case m.Kind == domain.ChatKindApproval && !m.Resolved:
		default:
			continue
		}
		live[m.RunID] = true
		ids = append(ids, m.RunID)
	}
	if len(ids) == 0 {
		return messages
	}
	revisions := h.repo.RunRevisions(ids)
	if revisions == nil {
		return messages
	}
	runs := make(map[string]domain.Run, len(ids))
	changed := map[string]bool{}
	for _, id := range ids {
		revision, ok := revisions[id]
		if !ok {
			continue
		}
		if run, hit := h.runCache.get(id, revision); hit {
			runs[id] = run
			continue
		}
		run, ok := h.repo.RunView(id)
		if !ok {
			continue
		}
		h.runCache.put(id, run)
		runs[id] = run
		changed[id] = true
	}

	known := make(map[string]bool, len(messages))
	for _, m := range messages {
		known[m.ID] = true
	}
	agentNames := map[string]string{}
	agentName := func(id string) string {
		if name, ok := agentNames[id]; ok {
			return name
		}
		name := ""
		if agent, ok := h.repo.Agent(id); ok {
			name = agent.Name
		}
		agentNames[id] = name
		return name
	}
	added := false
	for i := range messages {
		m := &messages[i]
		if !live[m.RunID] {
			continue
		}
		_, exists := revisions[m.RunID]
		run, loaded := runs[m.RunID]
		switch m.Kind {
		case domain.ChatKindDispatch:
			if m.RunFinal != "" {
				continue
			}
			if !loaded {
				continue
			}
			// Another instance may have posted these already; deterministic
			// ids make the write idempotent, so only a moved run is replayed.
			if changed[m.RunID] {
				for _, msg := range roomRunMessages(room, *m, run, agentName) {
					if known[msg.ID] {
						continue
					}
					if err := h.repo.PutMessage(msg); err == nil {
						known[msg.ID] = true
						added = true
					}
				}
			}
			roomRunProgress(m, run)
			if roomRunTerminal(run.Status) {
				m.RunFinal = run.Status
				m.RunCurrent = nil
				_ = h.repo.PutMessage(*m)
				added = true
			}
		case domain.ChatKindApproval:
			if m.Resolved {
				continue
			}
			if !exists || (loaded && roomApprovalResolved(*m, run)) {
				m.Resolved = true
				m.CanReject = false
				_ = h.repo.PutMessage(*m)
				continue
			}
			if loaded {
				m.CanReject = roomRejectTarget(run.WorkflowSnapshot, m.NodeID) != ""
			}
		}
	}
	if !added {
		return messages
	}
	h.touchRoom(room)
	fresh := h.repo.Messages(room.ID, 200)
	for i := range fresh {
		m := &fresh[i]
		run, ok := runs[m.RunID]
		if !ok {
			continue
		}
		switch {
		case m.Kind == domain.ChatKindDispatch && m.RunFinal == "":
			roomRunProgress(m, run)
		case m.Kind == domain.ChatKindApproval && !m.Resolved:
			if roomApprovalResolved(*m, run) {
				m.Resolved = true
				_ = h.repo.PutMessage(*m)
			} else {
				m.CanReject = roomRejectTarget(run.WorkflowSnapshot, m.NodeID) != ""
			}
		}
	}
	return fresh
}

type roomDecisionRequest struct {
	RunID  string `json:"runId"`
	NodeID string `json:"nodeId"`
	Action string `json:"action"` // approve, reject
	Reason string `json:"reason"`
}

// decide lets a room member approve or send back a step of a run that was
// dispatched from this room, and records the decision in the thread.
func (h *roomsHandlers) decide(c *gin.Context) {
	room, ok := h.repo.Room(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
		return
	}
	if !h.roomAccess(c, room.ProjectID, true) {
		return
	}
	var req roomDecisionRequest
	if !decode(c, &req) {
		return
	}
	req.RunID = strings.TrimSpace(req.RunID)
	req.NodeID = strings.TrimSpace(req.NodeID)
	req.Reason = roomTrim(req.Reason, 1000)
	if req.RunID == "" || req.NodeID == "" || (req.Action != "approve" && req.Action != "reject") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "runId、nodeId 和 action(approve/reject) 必填"})
		return
	}
	dispatch, found := h.repo.DispatchMessage(room.ID, req.RunID)
	if !found {
		c.JSON(http.StatusForbidden, gin.H{"error": "这个运行不是从当前群派发的"})
		return
	}
	run, ok := h.repo.RunView(req.RunID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	nr := run.Nodes[req.NodeID]
	if nr == nil || nr.Status != "waiting_approval" {
		c.JSON(http.StatusConflict, gin.H{"error": "这一步已经不在等待审批了"})
		return
	}
	if rejectBlockedIssueRun(h.deps, c, req.RunID) {
		return
	}
	nodeName := req.NodeID
	for _, node := range run.WorkflowSnapshot.Nodes {
		if node.ID == req.NodeID {
			nodeName = fallbackName(node.Name, node.ID)
		}
	}
	userID, author := h.roomActor(c)
	var err error
	body := ""
	if req.Action == "approve" {
		err = h.engine.Approve(req.RunID, req.NodeID, req.Reason)
		body = "批准了「" + nodeName + "」"
	} else {
		target := roomRejectTarget(run.WorkflowSnapshot, req.NodeID)
		if target == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "「" + nodeName + "」前面没有可以打回的步骤，也没有配置打回目标，只能批准或到运行详情里取消"})
			return
		}
		err = h.engine.Reject(req.RunID, domain.RejectRequest{NodeID: req.NodeID, TargetID: target, Reason: req.Reason})
		body = "打回了「" + nodeName + "」"
	}
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if req.Reason != "" {
		body += "：" + req.Reason
	}
	msg := domain.ChatMessage{ID: newChatID("msg"), RoomID: room.ID, UserID: userID, Author: author, Kind: domain.ChatKindDecision, Body: body, CreatedAt: time.Now().UTC(),
		IssueID: dispatch.IssueID, IssueNumber: dispatch.IssueNumber, IssueTitle: dispatch.IssueTitle, RunID: req.RunID, NodeID: req.NodeID, NodeName: nodeName}
	if err := h.repo.PutMessage(msg); err != nil {
		respond(c, err)
		return
	}
	h.touchRoom(room)
	c.JSON(http.StatusOK, msg)
}

// roomRejectTarget keeps the node's configured rework target and otherwise
// sends the work back to the closest upstream step.
func roomRejectTarget(wf domain.WorkflowDefinition, nodeID string) string {
	for _, node := range wf.Nodes {
		if node.ID == nodeID && strings.TrimSpace(node.RejectTo) != "" {
			return node.RejectTo
		}
	}
	var sources []string
	for _, edge := range wf.Edges {
		if edge.Target == nodeID && edge.Source != nodeID {
			sources = append(sources, edge.Source)
		}
	}
	if len(sources) == 0 {
		return ""
	}
	sort.Strings(sources)
	return sources[0]
}

package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"

	"github.com/gin-gonic/gin"
)

// A lane is one SOP run dispatched from the space, projected for the
// workbench: its graph, live node state, inputs and workspace.

type spaceLaneNode struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	AgentID     string    `json:"agentId,omitempty"`
	AgentName   string    `json:"agentName,omitempty"`
	Status      string    `json:"status"`
	Attempt     int       `json:"attempt,omitempty"`
	ReworkCount int       `json:"reworkCount,omitempty"`
	Activity    string    `json:"activity,omitempty"`
	Output      string    `json:"output,omitempty"`
	Error       string    `json:"error,omitempty"`
	SkipReason  string    `json:"skipReason,omitempty"`
	RejectTo    string    `json:"rejectTo,omitempty"`
	Tokens      int64     `json:"tokens,omitempty"`
	CostUSD     float64   `json:"costUsd,omitempty"`
	ToolCalls   int       `json:"toolCalls,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
	FinishedAt  time.Time `json:"finishedAt,omitempty"`
}

type spaceLaneEdge struct {
	Source    string                    `json:"source"`
	Target    string                    `json:"target"`
	When      string                    `json:"when,omitempty"`
	Condition *domain.WorkflowCondition `json:"condition,omitempty"`
}

type spaceLaneWorkspace struct {
	State   string     `json:"state"` // none, running, sleeping, cleaned
	Branch  string     `json:"branch,omitempty"`
	Repo    string     `json:"repo,omitempty"`
	SavedAt *time.Time `json:"savedAt,omitempty"`
	Error   string     `json:"error,omitempty"`
}

type spaceLane struct {
	RunID         string               `json:"runId,omitempty"`
	IssueID       string               `json:"issueId,omitempty"`
	MessageID     string               `json:"messageId"`
	WorkflowID    string               `json:"workflowId"`
	WorkflowName  string               `json:"workflowName"`
	SOPLane       string               `json:"sopLane,omitempty"`
	Status        string               `json:"status"`
	DispatchError string               `json:"dispatchError,omitempty"`
	OwnerID       int64                `json:"ownerId,omitempty"`
	OwnerName     string               `json:"ownerName,omitempty"`
	StartedAt     time.Time            `json:"startedAt"`
	UpdatedAt     time.Time            `json:"updatedAt,omitempty"`
	Nodes         []spaceLaneNode      `json:"nodes"`
	Edges         []spaceLaneEdge      `json:"edges"`
	Inputs        []domain.ArtifactRef `json:"inputs,omitempty"`
	ThreadID      string               `json:"threadId,omitempty"`
	Tokens        int64                `json:"tokens"`
	CostUSD       float64              `json:"costUsd"`
	ToolCalls     int                  `json:"toolCalls"`
	Workspace     spaceLaneWorkspace   `json:"workspace"`
}

type spaceLaneEvent struct {
	NodeID    string    `json:"nodeId,omitempty"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

type spaceLaneArtifact struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"nodeId"`
	Attempt   int       `json:"attempt"`
	Name      string    `json:"name"`
	MediaType string    `json:"mediaType"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"createdAt"`
}

type spaceLaneDetail struct {
	spaceLane
	Events    []spaceLaneEvent    `json:"events"`
	Artifacts []spaceLaneArtifact `json:"artifacts"`
}

func (h *spacesHandlers) agentNamer() func(string) string {
	cache := map[string]string{}
	return func(id string) string {
		if id == "" {
			return ""
		}
		if name, ok := cache[id]; ok {
			return name
		}
		name := ""
		if agent, ok := h.repo.Agent(id); ok {
			name = agent.Name
		}
		cache[id] = name
		return name
	}
}

func (h *spacesHandlers) lanes(room domain.ChatRoom, space domain.RequirementSpace) []spaceLane {
	agentName := h.agentNamer()
	out := []spaceLane{}
	for _, d := range h.repo.DispatchMessages(room.ID) {
		lane := spaceLane{RunID: d.RunID, IssueID: d.IssueID, MessageID: d.ID, WorkflowID: d.WorkflowID, WorkflowName: fallbackName(d.WorkflowName, d.WorkflowID),
			Status: "pending", DispatchError: d.DispatchError, OwnerID: d.UserID, OwnerName: d.Author, StartedAt: d.CreatedAt, Nodes: []spaceLaneNode{}, Edges: []spaceLaneEdge{}, Workspace: spaceLaneWorkspace{State: "none"}}
		if d.DispatchError != "" {
			lane.Status = "failed"
		}
		lane.Inputs = space.LaneInputs[d.RunID]
		if d.RunID != "" {
			if t, ok := h.repo.SpaceRunThread(room.ID, d.RunID); ok {
				lane.ThreadID = t.ID
			}
			if run, ok := h.repo.RunView(d.RunID); ok {
				fillLane(&lane, run, agentName)
				lane.Workspace = h.laneWorkspace(run)
			}
		}
		out = append(out, lane)
	}
	return out
}

func fillLane(lane *spaceLane, run domain.Run, agentName func(string) string) {
	wf := run.WorkflowSnapshot
	lane.WorkflowName = fallbackName(lane.WorkflowName, wf.Name)
	lane.SOPLane = wf.SOPLane
	lane.Status = run.Status
	lane.UpdatedAt = run.UpdatedAt
	lane.Tokens, lane.CostUSD, lane.ToolCalls = run.Usage.TotalTokens, run.Usage.CostUSD, run.ToolCalls
	var tokens int64
	var cost float64
	tools := 0
	for _, node := range wf.Nodes {
		n := spaceLaneNode{ID: node.ID, Name: fallbackName(node.Name, node.ID), Type: node.Type, AgentID: node.AgentID, AgentName: agentName(node.AgentID),
			Status: "pending", RejectTo: node.RejectTo}
		if nr := run.Nodes[node.ID]; nr != nil {
			n.Status, n.Attempt, n.ReworkCount, n.Activity = nr.Status, nr.Attempt, nr.ReworkCount, nr.Activity
			n.Output, n.Error = roomTrim(nr.Output, 1200), roomTrim(nr.Error, 600)
			n.SkipReason = nr.SkipReason
			n.Tokens, n.CostUSD, n.ToolCalls = nr.Usage.TotalTokens, nr.Usage.CostUSD, nr.ToolCalls
			n.StartedAt, n.FinishedAt = nr.StartedAt, nr.FinishedAt
			tokens += nr.Usage.TotalTokens
			cost += nr.Usage.CostUSD
			tools += nr.ToolCalls
		}
		lane.Nodes = append(lane.Nodes, n)
	}
	if lane.Tokens == 0 {
		lane.Tokens, lane.CostUSD = tokens, cost
	}
	if lane.ToolCalls == 0 {
		lane.ToolCalls = tools
	}
	for _, e := range wf.Edges {
		lane.Edges = append(lane.Edges, spaceLaneEdge{Source: e.Source, Target: e.Target, When: e.When, Condition: e.Condition})
	}
}

func (h *spacesHandlers) laneWorkspace(run domain.Run) spaceLaneWorkspace {
	ws := spaceLaneWorkspace{State: "none"}
	for _, r := range run.RepositoryContexts() {
		ws.Branch = fallbackName(r.WorkBranch, r.BaseBranch)
		ws.Repo = r.Name
		break
	}
	switch {
	case !domain.TerminalRunStatus(run.Status) && run.Status != "":
		ws.State = "running"
	case !run.WorkspaceCleanedAt.IsZero():
		ws.State = "cleaned"
	}
	if cp, ok := h.repo.WorkspaceCheckpoint(run.ID); ok {
		ws.SavedAt, ws.Error = cp.SavedAt, roomTrim(cp.Error, 300)
		if ws.State != "running" && cp.SavedAt != nil {
			ws.State = "sleeping"
		}
	}
	return ws
}

func (h *spacesHandlers) laneList(c *gin.Context) {
	room, space, ok := h.loadSpace(c, false)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, h.lanes(room, space))
}

func (h *spacesHandlers) lane(c *gin.Context) {
	room, space, ok := h.loadSpace(c, false)
	if !ok {
		return
	}
	runID := c.Param("runId")
	dispatch, found := h.repo.DispatchMessage(room.ID, runID)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "这个运行不是从当前需求空间派发的"})
		return
	}
	run, ok := h.repo.RunState(runID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	detail := spaceLaneDetail{spaceLane: spaceLane{RunID: runID, IssueID: dispatch.IssueID, MessageID: dispatch.ID, WorkflowID: dispatch.WorkflowID,
		WorkflowName: dispatch.WorkflowName, OwnerID: dispatch.UserID, OwnerName: dispatch.Author, StartedAt: dispatch.CreatedAt, Inputs: space.LaneInputs[runID],
		Nodes: []spaceLaneNode{}, Edges: []spaceLaneEdge{}}, Events: []spaceLaneEvent{}, Artifacts: []spaceLaneArtifact{}}
	if t, ok := h.repo.SpaceRunThread(room.ID, runID); ok {
		detail.ThreadID = t.ID
	}
	fillLane(&detail.spaceLane, run, h.agentNamer())
	if view, ok := h.repo.RunView(runID); ok {
		// RunState carries raw usage only; RunView aggregates it.
		detail.Tokens, detail.CostUSD, detail.ToolCalls = max(detail.Tokens, view.Usage.TotalTokens), max(detail.CostUSD, view.Usage.CostUSD), max(detail.ToolCalls, view.ToolCalls)
	}
	detail.Workspace = h.laneWorkspace(run)
	events := run.Events
	if len(events) > 300 {
		events = events[len(events)-300:]
	}
	for _, ev := range events {
		detail.Events = append(detail.Events, spaceLaneEvent{NodeID: ev.NodeID, Type: ev.Type, Message: roomTrim(ev.Message, 600), Timestamp: ev.Timestamp})
	}
	for _, a := range run.Artifacts {
		detail.Artifacts = append(detail.Artifacts, spaceLaneArtifact{ID: a.ID, NodeID: a.NodeID, Attempt: a.Attempt, Name: a.Name, MediaType: a.MediaType, Size: a.Size, CreatedAt: a.CreatedAt})
	}
	c.JSON(http.StatusOK, detail)
}

// startSOPs dispatches SOPs from the space. Inputs pin the artifact versions
// the lanes start from and are appended to the issue description so every
// agent reads the same spec.
func (h *spacesHandlers) startSOPs(c *gin.Context) {
	room, space, ok := h.loadSpace(c, true)
	if !ok {
		return
	}
	var req struct {
		roomIssueRequest
		Inputs []string `json:"inputs"`
	}
	if !decode(c, &req) {
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		req.Title = room.Name
	}
	if strings.TrimSpace(req.Description) == "" {
		req.Description = space.Summary
	}
	var refs []domain.ArtifactRef
	var lines []string
	for _, id := range req.Inputs {
		a, ok := h.repo.SpaceArtifact(room.ID, id)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "输入产物不存在"})
			return
		}
		refs = append(refs, domain.ArtifactRef{ID: a.ID, Version: a.Version})
		line := fmt.Sprintf("- %s v%d", a.Title, a.Version)
		if a.Summary != "" {
			line += "：" + roomTrim(a.Summary, 300)
		}
		if a.Kind == "doc" || a.Kind == "api" {
			if body := roomTrim(a.Body, 2500); body != "" {
				line += "\n" + indent(body)
			}
		}
		lines = append(lines, line)
	}
	if linked := h.requirementOf(space); linked != nil {
		req.IssueID = linked.ID
		if issue, ok := h.repo.Issue(linked.ID); ok {
			req.Description = issue.Description
		}
	} else {
		req.IssueID = ""
	}
	if len(lines) > 0 {
		base, _, _ := strings.Cut(req.Description, inputsHeading)
		req.Description = withInputs(base, lines)
		if req.IssueID != "" {
			desc := req.Description
			if _, err := h.repo.PatchIssue(req.IssueID, func(iss *domain.Issue) error {
				iss.Description = desc
				return nil
			}); err != nil {
				respond(c, err)
				return
			}
		}
	}
	result, ok := h.rooms.dispatchRoomIssue(c, room, req.roomIssueRequest)
	if !ok {
		return
	}
	if space.LaneInputs == nil {
		space.LaneInputs = map[string][]domain.ArtifactRef{}
	}
	for _, msg := range result.Messages {
		if msg.RunID == "" {
			continue
		}
		if len(refs) > 0 {
			space.LaneInputs[msg.RunID] = refs
		}
		_, _ = h.ensureThread(c, room, "", msg.RunID)
	}
	if space.IssueID == "" {
		space.IssueID = result.Issue.ID
	}
	if err := h.saveSpace(room, space); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, result)
}

const inputsHeading = "\n\n## 输入产物\n"

// withInputs appends the input artifact section to an issue description.
// The section is rebuilt on every start, so the run sees current versions.
func withInputs(description string, lines []string) string {
	out := strings.TrimSpace(description) + inputsHeading + strings.Join(lines, "\n")
	if len([]rune(out)) > 8000 {
		out = string([]rune(out)[:7990]) + "…"
	}
	return strings.TrimSpace(out)
}

// catalog lists the SOPs, roles and sandboxes the space's project may use.
func (h *spacesHandlers) catalog(c *gin.Context) {
	room, _, ok := h.loadSpace(c, false)
	if !ok {
		return
	}
	type wfItem struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Status      string `json:"status,omitempty"`
		SOPLane     string `json:"sopLane,omitempty"`
		Nodes       int    `json:"nodes"`
		ProjectID   string `json:"projectId,omitempty"`
	}
	type agentItem struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}
	type sandboxItem struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := struct {
		Workflows []wfItem      `json:"workflows"`
		Agents    []agentItem   `json:"agents"`
		Sandboxes []sandboxItem `json:"sandboxes"`
	}{[]wfItem{}, []agentItem{}, []sandboxItem{}}
	for _, wf := range h.repo.Workflows() {
		if wf.Status == "archived" || (!wf.Generic && !catalogVisible(wf.ProjectID, wf.Shared, room.ProjectID)) {
			continue
		}
		out.Workflows = append(out.Workflows, wfItem{ID: wf.ID, Name: wf.Name, Description: wf.Description, Status: wf.Status, SOPLane: wf.SOPLane, Nodes: len(wf.Nodes), ProjectID: wf.ProjectID})
	}
	for _, a := range h.repo.Agents() {
		if catalogVisible(a.ProjectID, a.Shared, room.ProjectID) {
			out.Agents = append(out.Agents, agentItem{ID: a.ID, Name: a.Name, Description: a.Description})
		}
	}
	for _, s := range h.repo.Sandboxes() {
		if catalogVisible(s.ProjectID, s.Shared, room.ProjectID) {
			out.Sandboxes = append(out.Sandboxes, sandboxItem{ID: s.ID, Name: fallbackName(s.Name, s.ID)})
		}
	}
	sort.Slice(out.Workflows, func(i, j int) bool { return out.Workflows[i].Name < out.Workflows[j].Name })
	c.JSON(http.StatusOK, out)
}

type spaceFlowNode struct {
	ID              string                    `json:"id"`
	Name            string                    `json:"name"`
	Type            string                    `json:"type"` // agent, script, approval
	AgentID         string                    `json:"agentId"`
	Script          string                    `json:"script"`
	MetricStage     string                    `json:"metricStage"`
	JoinRule        string                    `json:"joinRule,omitempty"`
	DefaultTarget   string                    `json:"defaultTarget,omitempty"`
	ArtifactOutputs []domain.ArtifactContract `json:"artifactOutputs,omitempty"`
	X               float64                   `json:"x"`
	Y               float64                   `json:"y"`
}

type spaceFlowRequest struct {
	ArtifactID       string          `json:"artifactId"`
	WorkflowID       string          `json:"workflowId"`
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	SOPLane          string          `json:"sopLane"`
	SandboxProfileID string          `json:"sandboxProfileId"`
	Nodes            []spaceFlowNode `json:"nodes"`
	Edges            []spaceLaneEdge `json:"edges"`
}

// saveFlow turns an orchestration drafted in the space into a real SOP via
// PUT /api/workflows, then keeps it as a versioned "sop" artifact.
func (h *spacesHandlers) saveFlow(c *gin.Context) {
	room, _, ok := h.loadSpace(c, true)
	if !ok {
		return
	}
	var req spaceFlowRequest
	if !decode(c, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len([]rune(req.Name)) > 60 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "编排名称需要 1 到 60 个字"})
		return
	}
	if len(req.Nodes) == 0 || len(req.Nodes) > 30 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "编排需要 1 到 30 个步骤"})
		return
	}
	wf, errMsg := h.flowWorkflow(room, req)
	if errMsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
		return
	}
	status, raw := h.internalCall(c, http.MethodPut, "/api/workflows", wf)
	if status >= 400 {
		c.JSON(status, gin.H{"error": apiError(raw, "保存 SOP 失败")})
		return
	}
	var stored domain.WorkflowDefinition
	_ = json.Unmarshal(raw, &stored)
	if stored.ID == "" {
		stored = wf
	}
	userID, author := h.rooms.roomActor(c)
	body, _ := json.Marshal(gin.H{"nodes": req.Nodes, "edges": req.Edges, "sandboxProfileId": wf.SandboxProfileID})
	art := domain.SpaceArtifact{ID: req.ArtifactID, SpaceID: room.ID, Kind: "sop", Title: req.Name, Summary: roomTrim(req.Description, 300), Format: "json",
		Body: string(body), WorkflowID: stored.ID, AuthorID: userID, AuthorName: author}
	base := 0
	if art.ID == "" {
		art.ID = newChatID("art")
	} else if old, ok := h.repo.SpaceArtifact(room.ID, art.ID); ok {
		base = old.Version
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "产物不存在"})
		return
	}
	saved, err := h.repo.PutSpaceArtifact(art, base)
	if err != nil {
		respond(c, err)
		return
	}
	verb := "保存了编排"
	if base > 0 {
		verb = "更新了编排"
	}
	_, _ = h.post(room, domain.ChatMessage{UserID: userID, Author: author, Kind: domain.ChatKindFlow, ArtifactID: saved.ID, ArtifactVersion: saved.Version, WorkflowID: stored.ID,
		WorkflowName: stored.Name, Body: fmt.Sprintf("%s「%s」v%d，可以直接启动", verb, saved.Title, saved.Version)})
	saved.Body = ""
	c.JSON(http.StatusOK, gin.H{"artifact": saved, "workflow": gin.H{"id": stored.ID, "name": stored.Name, "revision": stored.Revision, "status": stored.Status}})
}

func (h *spacesHandlers) flowWorkflow(room domain.ChatRoom, req spaceFlowRequest) (domain.WorkflowDefinition, string) {
	wf := domain.WorkflowDefinition{ID: strings.TrimSpace(req.WorkflowID), Name: req.Name, Version: 1, Status: "draft", ProjectID: room.ProjectID,
		Description: strings.TrimSpace(req.Description), SOPLane: strings.TrimSpace(req.SOPLane), SandboxProfileID: strings.TrimSpace(req.SandboxProfileID)}
	if wf.ID != "" {
		old, ok := h.repo.Workflow(wf.ID)
		if !ok || old.ProjectID != room.ProjectID {
			return wf, "只能更新当前项目里由需求空间保存的 SOP"
		}
		wf.Version, wf.Revision, wf.Status, wf.Tags, wf.Notifications = old.Version, old.Revision, old.Status, old.Tags, old.Notifications
		if wf.SandboxProfileID == "" {
			wf.SandboxProfileID = old.SandboxProfileID
		}
		if wf.Version == 0 {
			wf.Version = 1
		}
	} else {
		wf.ID = newChatID("wf")
	}
	if wf.SandboxProfileID == "" {
		for _, s := range h.repo.Sandboxes() {
			if s.ProjectID == room.ProjectID {
				wf.SandboxProfileID = s.ID
				break
			}
		}
	}
	if wf.SandboxProfileID == "" {
		for _, s := range h.repo.Sandboxes() {
			if s.Shared {
				wf.SandboxProfileID = s.ID
				break
			}
		}
	}
	if wf.SandboxProfileID == "" {
		return wf, "项目还没有可用的沙箱，请先在「沙箱」里创建一个"
	}
	ids := map[string]bool{}
	for i, n := range req.Nodes {
		id := strings.TrimSpace(n.ID)
		if id == "" || ids[id] {
			return wf, "步骤 ID 不能为空或重复"
		}
		ids[id] = true
		node := domain.WorkflowNode{ID: id, Name: fallbackName(strings.TrimSpace(n.Name), id), Type: n.Type, MaxAttempts: 1, MaxReworks: 2, JoinRule: n.JoinRule, DefaultTarget: n.DefaultTarget, ArtifactOutputs: n.ArtifactOutputs}
		node.Position.X, node.Position.Y = n.X, n.Y
		switch n.Type {
		case "agent":
			agent, ok := h.repo.Agent(n.AgentID)
			if !ok || !catalogVisible(agent.ProjectID, agent.Shared, room.ProjectID) {
				return wf, fmt.Sprintf("第 %d 步「%s」还没有选定可用的角色", i+1, node.Name)
			}
			node.AgentID = agent.ID
			node.MetricStage = "development"
		case "script":
			node.Script = strings.TrimSpace(n.Script)
			if node.Script == "" {
				node.Script = "echo \"" + strings.ReplaceAll(node.Name, "\"", "") + "\""
			}
			node.ScriptLanguage = "shell"
			node.MetricStage = "testing"
		case "approval":
			node.MetricStage = "none"
		default:
			return wf, fmt.Sprintf("第 %d 步的类型只能是 agent、script 或 approval", i+1)
		}
		if ms := strings.TrimSpace(n.MetricStage); ms != "" {
			node.MetricStage = ms
		}
		wf.Nodes = append(wf.Nodes, node)
	}
	for i, e := range req.Edges {
		if !ids[e.Source] || !ids[e.Target] || e.Source == e.Target {
			return wf, "连线指向了不存在的步骤"
		}
		when := e.When
		if when == "" {
			when = "success"
		}
		if when == "rework" {
			for j := range wf.Nodes {
				if wf.Nodes[j].ID == e.Source {
					wf.Nodes[j].RejectTo = e.Target
					wf.Nodes[j].OnFailure = "reject"
				}
			}
			continue
		}
		wf.Edges = append(wf.Edges, domain.WorkflowEdge{ID: fmt.Sprintf("e%d-%s-%s", i+1, e.Source, e.Target), Source: e.Source, Target: e.Target, When: when, Condition: e.Condition})
	}
	if err := engine.ValidateWorkflowRouting(wf); err != nil {
		return wf, err.Error()
	}
	return wf, ""
}

type spaceUsageDay struct {
	Day   string           `json:"day"`
	Lanes map[string]int64 `json:"lanes"`
}

// usage aggregates token and tool use of every lane, by lane, node and day.
func (h *spacesHandlers) usage(c *gin.Context) {
	room, space, ok := h.loadSpace(c, false)
	if !ok {
		return
	}
	lanes := h.lanes(room, space)
	days := map[string]map[string]int64{}
	type laneUsage struct {
		RunID     string          `json:"runId"`
		Name      string          `json:"name"`
		Status    string          `json:"status"`
		Tokens    int64           `json:"tokens"`
		CostUSD   float64         `json:"costUsd"`
		ToolCalls int             `json:"toolCalls"`
		Seconds   int64           `json:"seconds"`
		Nodes     []spaceLaneNode `json:"nodes"`
	}
	out := struct {
		Tokens    int64           `json:"tokens"`
		CostUSD   float64         `json:"costUsd"`
		ToolCalls int             `json:"toolCalls"`
		Seconds   int64           `json:"seconds"`
		Lanes     []laneUsage     `json:"lanes"`
		Days      []spaceUsageDay `json:"days"`
	}{Lanes: []laneUsage{}, Days: []spaceUsageDay{}}
	for _, l := range lanes {
		if l.RunID == "" {
			continue
		}
		lu := laneUsage{RunID: l.RunID, Name: l.WorkflowName, Status: l.Status, Tokens: l.Tokens, CostUSD: l.CostUSD, ToolCalls: l.ToolCalls, Nodes: []spaceLaneNode{}}
		for _, n := range l.Nodes {
			if n.StartedAt.IsZero() {
				continue
			}
			end := n.FinishedAt
			if end.IsZero() {
				end = time.Now().UTC()
			}
			if d := end.Sub(n.StartedAt); d > 0 {
				lu.Seconds += int64(d.Seconds())
			}
			n.Output, n.Error = "", ""
			lu.Nodes = append(lu.Nodes, n)
			day := n.StartedAt.Local().Format("2006-01-02")
			if days[day] == nil {
				days[day] = map[string]int64{}
			}
			days[day][l.WorkflowName] += n.Tokens
		}
		out.Tokens += lu.Tokens
		out.CostUSD += lu.CostUSD
		out.ToolCalls += lu.ToolCalls
		out.Seconds += lu.Seconds
		out.Lanes = append(out.Lanes, lu)
	}
	keys := make([]string, 0, len(days))
	for k := range days {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out.Days = append(out.Days, spaceUsageDay{Day: k, Lanes: days[k]})
	}
	c.JSON(http.StatusOK, out)
}

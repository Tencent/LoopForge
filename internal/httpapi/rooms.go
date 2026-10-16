package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

type roomsHandlers struct {
	*deps
	runCache roomRunCache
}

// roomMaxSOPs bounds one multi-SOP dispatch.
const roomMaxSOPs = 5

type roomIssueRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	WorkflowID  string   `json:"workflowId"`
	WorkflowIDs []string `json:"workflowIds"`
	AgentID     string   `json:"agentId"`
}

type roomIssueResult struct {
	Message       domain.ChatMessage   `json:"message"`
	Messages      []domain.ChatMessage `json:"messages,omitempty"`
	Issue         domain.Issue         `json:"issue"`
	Task          *domain.Task         `json:"task,omitempty"`
	Tasks         []domain.Task        `json:"tasks,omitempty"`
	DispatchError string               `json:"dispatchError,omitempty"`
}

func roomWorkflowIDs(req roomIssueRequest) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range append([]string{req.WorkflowID}, req.WorkflowIDs...) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (h *roomsHandlers) rooms(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet:
		projectID := strings.TrimSpace(c.Query("projectId"))
		if projectID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "projectId is required"})
			return
		}
		if _, ok := h.repo.Project(projectID); !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
			return
		}
		if !h.roomAccess(c, projectID, false) {
			return
		}
		c.JSON(http.StatusOK, h.roomViews(h.repo.Rooms(projectID)))
	case http.MethodPost:
		var req struct {
			ProjectID string `json:"projectId"`
			Name      string `json:"name"`
		}
		if !decode(c, &req) {
			return
		}
		req.ProjectID = strings.TrimSpace(req.ProjectID)
		req.Name = strings.TrimSpace(req.Name)
		if req.ProjectID == "" || req.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "projectId and name are required"})
			return
		}
		if _, ok := h.repo.Project(req.ProjectID); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "project not found"})
			return
		}
		if len([]rune(req.Name)) > 80 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "群名称不能超过 80 个字"})
			return
		}
		if !h.roomAccess(c, req.ProjectID, true) {
			return
		}
		userID, _ := h.roomActor(c)
		room := domain.ChatRoom{ID: newChatID("room"), ProjectID: req.ProjectID, Name: req.Name, CreatedBy: userID}
		if err := h.repo.PutRoom(room); err != nil {
			respond(c, err)
			return
		}
		stored, _ := h.repo.Room(room.ID)
		c.JSON(http.StatusCreated, h.roomView(stored))
	default:
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}

func (h *roomsHandlers) room(c *gin.Context) {
	room, ok := h.repo.Room(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
		return
	}
	if c.Request.Method != http.MethodGet {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
		return
	}
	if !h.roomAccess(c, room.ProjectID, false) {
		return
	}
	c.JSON(http.StatusOK, h.roomView(room))
}

func (h *roomsHandlers) messages(c *gin.Context) {
	room, ok := h.repo.Room(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
		return
	}
	if !h.roomAccess(c, room.ProjectID, c.Request.Method != http.MethodGet) {
		return
	}
	switch c.Request.Method {
	case http.MethodGet:
		c.JSON(http.StatusOK, h.syncRoomRuns(room, h.repo.Messages(room.ID, 200)))
	case http.MethodPost:
		var req struct {
			Body string `json:"body"`
		}
		if !decode(c, &req) {
			return
		}
		body := strings.TrimSpace(req.Body)
		if body == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "消息不能为空"})
			return
		}
		if len([]rune(body)) > 4000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "消息不能超过 4000 个字"})
			return
		}
		userID, author := h.roomActor(c)
		msg := domain.ChatMessage{ID: newChatID("msg"), RoomID: room.ID, UserID: userID, Author: author, Kind: "text", Body: body, CreatedAt: time.Now().UTC()}
		if err := h.repo.PutMessage(msg); err != nil {
			respond(c, err)
			return
		}
		h.touchRoom(room)
		c.JSON(http.StatusCreated, msg)
	default:
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}

func (h *roomsHandlers) createIssue(c *gin.Context) {
	if c.Request.Method != http.MethodPost {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
		return
	}
	room, ok := h.repo.Room(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
		return
	}
	if !h.roomAccess(c, room.ProjectID, true) {
		return
	}
	var req roomIssueRequest
	if !decode(c, &req) {
		return
	}
	if len([]rune(req.Title)) > 200 || len([]rune(req.Description)) > 8000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "标题或描述过长"})
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)
	req.AgentID = strings.TrimSpace(req.AgentID)
	workflowIDs := roomWorkflowIDs(req)
	if req.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}
	if len(workflowIDs) == 0 && req.AgentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要派给的 SOP 或角色"})
		return
	}
	if len(workflowIDs) > roomMaxSOPs {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("一次最多派给 %d 条 SOP", roomMaxSOPs)})
		return
	}
	workflowNames := make([]string, len(workflowIDs))
	for i, id := range workflowIDs {
		wf, found := h.repo.Workflow(id)
		if !found || (!wf.Generic && !catalogVisible(wf.ProjectID, wf.Shared, room.ProjectID)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "SOP 不存在或不属于当前项目"})
			return
		}
		workflowNames[i] = wf.Name
	}
	var agentName string
	if req.AgentID != "" {
		agent, found := h.repo.Agent(req.AgentID)
		if !found || !catalogVisible(agent.ProjectID, agent.Shared, room.ProjectID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "角色不存在或不属于当前项目"})
			return
		}
		agentName = agent.Name
	}
	firstWorkflowID, firstWorkflowName := "", ""
	if len(workflowIDs) > 0 {
		firstWorkflowID, firstWorkflowName = workflowIDs[0], workflowNames[0]
	}
	userID, author := h.roomActor(c)
	issue := domain.Issue{
		ID:          newChatID("iss"),
		ProjectID:   room.ProjectID,
		Title:       req.Title,
		Description: req.Description,
		Status:      "todo",
		Priority:    "medium",
		Kind:        "issue",
		Source:      "local",
		WorkflowID:  firstWorkflowID,
	}
	if userID > 0 {
		issue.AssigneeUserID = userID
	}
	if req.AgentID != "" {
		issue.AssigneeType = "agent"
		issue.AssigneeID = req.AgentID
	}
	targetType, targetID, targetName := roomTarget(firstWorkflowID, firstWorkflowName, req.AgentID, agentName)
	if len(workflowIDs) > 1 {
		quoted := make([]string, len(workflowNames))
		for i, name := range workflowNames {
			quoted[i] = "「" + fallbackName(name, workflowIDs[i]) + "」"
		}
		targetType, targetName = "multi", "SOP"+strings.Join(quoted, "、")
		if agentName != "" {
			targetName += "和角色「" + agentName + "」"
		}
	}
	issue.AppendOperation(domain.IssueOperation{
		Actor: author, ActorUserID: userID, Action: "create",
		Detail: "从协作群「" + room.Name + "」建单，派给" + targetName,
	})
	if err := h.repo.PutIssue(issue); err != nil {
		respond(c, err)
		return
	}
	stored, _ := h.repo.Issue(issue.ID)
	result := roomIssueResult{Issue: stored}
	base := domain.ChatMessage{
		RoomID: room.ID, UserID: userID, Author: author, Kind: domain.ChatKindDispatch,
		IssueID: stored.ID, IssueNumber: stored.Number, IssueTitle: stored.Title,
		TargetType: targetType, TargetID: targetID, TargetName: targetName, AgentID: req.AgentID,
	}
	var messages []domain.ChatMessage
	if len(workflowIDs) == 0 {
		msg := base
		msg.ID = newChatID("msg")
		msg.CreatedAt = time.Now().UTC()
		msg.Body = req.Description
		messages = append(messages, msg)
	}
	var errs []string
	for i, wfID := range workflowIDs {
		msg := base
		msg.ID = newChatID("msg")
		msg.CreatedAt = time.Now().UTC()
		msg.Lane = i
		msg.WorkflowID = wfID
		msg.WorkflowName = workflowNames[i]
		if i == 0 {
			msg.Body = req.Description
		}
		task, err := dispatchIssue(h.deps, dispatchRequest{IssueID: stored.ID, WorkflowID: wfID, TriggerUserID: userID})
		if err != nil {
			msg.DispatchError = err.Error()
			errs = append(errs, "「"+fallbackName(workflowNames[i], wfID)+"」"+err.Error())
		} else {
			msg.TaskID = task.ID
			msg.RunID = task.RunID
			result.Tasks = append(result.Tasks, task)
			name := workflowNames[i]
			_, _ = h.repo.PatchIssue(stored.ID, func(iss *domain.Issue) error {
				iss.AppendOperation(domain.IssueOperation{Actor: author, ActorUserID: userID, Action: "dispatch", Detail: "协作群派发 SOP「" + name + "」"})
				return nil
			})
		}
		messages = append(messages, msg)
	}
	if len(result.Tasks) > 0 {
		result.Task = &result.Tasks[0]
		if again, ok := h.repo.Issue(stored.ID); ok {
			result.Issue = again
		}
	}
	result.DispatchError = strings.Join(errs, "；")
	for _, msg := range messages {
		if err := h.repo.PutMessage(msg); err != nil {
			respond(c, err)
			return
		}
	}
	h.touchRoom(room)
	result.Message = messages[0]
	result.Messages = messages
	c.JSON(http.StatusCreated, result)
}

func roomTarget(workflowID, workflowName, agentID, agentName string) (typ, id, name string) {
	switch {
	case workflowID != "" && agentID != "":
		return "both", workflowID, "SOP「" + fallbackName(workflowName, workflowID) + "」和角色「" + fallbackName(agentName, agentID) + "」"
	case workflowID != "":
		return "sop", workflowID, "SOP「" + fallbackName(workflowName, workflowID) + "」"
	default:
		return "role", agentID, "角色「" + fallbackName(agentName, agentID) + "」"
	}
}

func fallbackName(name, id string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return id
}

func catalogVisible(ownerProject string, shared bool, projectID string) bool {
	return domain.CatalogVisible(ownerProject, shared, projectID)
}

// roomAccess checks the signed-in user against the project roster. Anonymous
// requests only reach here when platform auth is disabled (local dev), which
// matches every other project-scoped API. Viewers may read but not post.
func (h *roomsHandlers) roomAccess(c *gin.Context, projectID string, write bool) bool {
	current, ok := h.requestUser(c.Request, true)
	if !ok || current == nil {
		if authRequired() {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			return false
		}
		return true
	}
	if h.isPlatformAdmin(c.Request) {
		return true
	}
	project, found := h.repo.Project(projectID)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return false
	}
	if project.OwnerID <= 0 && len(project.Members) == 0 {
		return true
	}
	_, userID := actorLabel(current)
	if userID > 0 && project.OwnerID == userID {
		return true
	}
	for _, member := range project.Members {
		if userID > 0 && member.UserID == userID {
			if write && strings.EqualFold(strings.TrimSpace(member.Role), "viewer") {
				c.JSON(http.StatusForbidden, gin.H{"error": "只读成员不能在群里发言或派单"})
				return false
			}
			return true
		}
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "你不是该项目成员，不能访问协作群"})
	return false
}

func (h *roomsHandlers) roomActor(c *gin.Context) (int64, string) {
	current, ok := h.requestUser(c.Request, true)
	if !ok || current == nil {
		return 0, "群成员"
	}
	name, id := actorLabel(current)
	if name == "" {
		name = "群成员"
	}
	return id, name
}

func (h *roomsHandlers) touchRoom(room domain.ChatRoom) {
	room.UpdatedAt = time.Now().UTC()
	_ = h.repo.PutRoom(room)
}

func (h *roomsHandlers) roomViews(rooms []domain.ChatRoom) []domain.ChatRoomView {
	out := make([]domain.ChatRoomView, 0, len(rooms))
	rosters := map[string][]domain.ChatMember{}
	for _, room := range rooms {
		members, ok := rosters[room.ProjectID]
		if !ok {
			members = h.projectRoster(room.ProjectID)
			rosters[room.ProjectID] = members
		}
		out = append(out, domain.ChatRoomView{ChatRoom: room, Members: members})
	}
	return out
}

func (h *roomsHandlers) roomView(room domain.ChatRoom) domain.ChatRoomView {
	return domain.ChatRoomView{ChatRoom: room, Members: h.projectRoster(room.ProjectID)}
}

// projectRoster lists the project owner and members; rooms share it because
// room membership follows the project.
func (h *roomsHandlers) projectRoster(projectID string) []domain.ChatMember {
	members := []domain.ChatMember{}
	project, ok := h.repo.Project(projectID)
	if !ok {
		return members
	}
	seen := map[int64]bool{}
	add := func(id int64, role string) {
		if id <= 0 || seen[id] {
			return
		}
		user, found := h.repo.User(id)
		if !found {
			return
		}
		seen[id] = true
		name := strings.TrimSpace(user.LoginName)
		if cn := strings.TrimSpace(user.ChineseName); cn != "" && name != "" {
			name = name + "(" + cn + ")"
		} else if cn != "" {
			name = cn
		}
		members = append(members, domain.ChatMember{UserID: id, Name: name, Role: role})
	}
	add(project.OwnerID, "owner")
	for _, member := range project.Members {
		add(member.UserID, member.Role)
	}
	return members
}

func newChatID(prefix string) string {
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strings.NewReplacer("-", "", "_", "").Replace(randomID())
}

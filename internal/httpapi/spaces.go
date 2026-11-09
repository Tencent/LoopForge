package httpapi

import (
	"fmt"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// spacesHandlers serves workbench conversations. A conversation is a chat
// room that may carry one platform requirement (issue); access, dispatch and
// approvals reuse the room handlers so both surfaces stay consistent.
type spacesHandlers struct {
	*deps
	rooms *roomsHandlers
}

type spaceMemberView struct {
	domain.SpaceMember
	ProjectRole string `json:"projectRole,omitempty"`
}

type spaceThreadView struct {
	domain.SpaceThread
	Count int `json:"count"`
}

// spaceRequirement summarises the issue a conversation carries.
type spaceRequirement struct {
	ID       string    `json:"id"`
	Number   int       `json:"number"`
	Title    string    `json:"title"`
	Status   string    `json:"status"`
	Priority string    `json:"priority"`
	Updated  time.Time `json:"updatedAt"`
}

type spaceView struct {
	domain.RequirementSpace
	Title            string                 `json:"title"`
	Requirement      *spaceRequirement      `json:"requirement,omitempty"`
	ProjectName      string                 `json:"projectName,omitempty"`
	Members          []spaceMemberView      `json:"members"`
	Artifacts        []domain.SpaceArtifact `json:"artifacts"`
	Threads          []spaceThreadView      `json:"threads"`
	Lanes            []spaceLane            `json:"lanes"`
	PendingApprovals int                    `json:"pendingApprovals"`
	LiveRuns         int                    `json:"liveRuns"`
}

type spaceListItem struct {
	domain.RequirementSpace
	Title       string            `json:"title"`
	Requirement *spaceRequirement `json:"requirement,omitempty"`
	repo.RoomSignal
}

// loadSpace resolves the room behind :id and checks access. Rooms created
// before spaces existed read as a plain conversation.
func (h *spacesHandlers) loadSpace(c *gin.Context, write bool) (domain.ChatRoom, domain.RequirementSpace, bool) {
	room, ok := h.repo.Room(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return room, domain.RequirementSpace{}, false
	}
	if !h.rooms.roomAccess(c, room.ProjectID, write) {
		return room, domain.RequirementSpace{}, false
	}
	return room, h.spaceFor(room), true
}

func (h *spacesHandlers) spaceFor(room domain.ChatRoom) domain.RequirementSpace {
	if space, ok := h.repo.Space(room.ID); ok {
		return space
	}
	return domain.RequirementSpace{ID: room.ID, ProjectID: room.ProjectID,
		CreatedBy: room.CreatedBy, CreatedAt: room.CreatedAt, UpdatedAt: room.UpdatedAt}
}

// saveSpace persists the space and bumps the room so lists reorder.
func (h *spacesHandlers) saveSpace(room domain.ChatRoom, space domain.RequirementSpace) error {
	if err := h.repo.PutSpace(space); err != nil {
		return err
	}
	h.rooms.touchRoom(room)
	return nil
}

// requirementOf returns the linked issue, or nil when there is none or it
// was deleted.
func (h *spacesHandlers) requirementOf(space domain.RequirementSpace) *spaceRequirement {
	if space.IssueID == "" {
		return nil
	}
	issue, ok := h.repo.Issue(space.IssueID)
	if !ok {
		return nil
	}
	return &spaceRequirement{ID: issue.ID, Number: issue.Number, Title: issue.Title, Status: issue.Status, Priority: issue.Priority, Updated: issue.UpdatedAt}
}

func (h *spacesHandlers) post(room domain.ChatRoom, msg domain.ChatMessage) (domain.ChatMessage, error) {
	if msg.ID == "" {
		msg.ID = newChatID("msg")
	}
	msg.RoomID = room.ID
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	if err := h.repo.PutMessage(msg); err != nil {
		return msg, err
	}
	h.rooms.touchRoom(room)
	return msg, nil
}

func (h *spacesHandlers) spaces(c *gin.Context) {
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
		if !h.rooms.roomAccess(c, projectID, false) {
			return
		}
		rooms := h.repo.Rooms(projectID)
		ids := make([]string, len(rooms))
		for i, room := range rooms {
			ids[i] = room.ID
		}
		signals := h.repo.RoomSignals(ids)
		out := make([]spaceListItem, 0, len(rooms))
		for _, room := range rooms {
			space := h.spaceFor(room)
			space.UpdatedAt = room.UpdatedAt
			out = append(out, spaceListItem{RequirementSpace: space, Title: room.Name, Requirement: h.requirementOf(space), RoomSignal: signals[room.ID]})
		}
		c.JSON(http.StatusOK, out)
	case http.MethodPost:
		h.createSpace(c)
	default:
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}

type spaceMemberRequest struct {
	UserID int64  `json:"userId"`
	Title  string `json:"title"`
}

func (h *spacesHandlers) createSpace(c *gin.Context) {
	var req struct {
		ProjectID    string               `json:"projectId"`
		Title        string               `json:"title"`
		Summary      string               `json:"summary"`
		CreatorTitle string               `json:"creatorTitle"`
		Members      []spaceMemberRequest `json:"members"`
		// IssueID opens the conversation for an existing requirement; if one
		// already carries it, that conversation is returned instead.
		IssueID string `json:"issueId"`
	}
	if !decode(c, &req) {
		return
	}
	req.ProjectID, req.Title, req.Summary = strings.TrimSpace(req.ProjectID), strings.TrimSpace(req.Title), strings.TrimSpace(req.Summary)
	if req.ProjectID == "" || req.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写项目和会话标题"})
		return
	}
	if len([]rune(req.Title)) > 80 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "标题不能超过 80 个字"})
		return
	}
	if len([]rune(req.Summary)) > 4000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "描述不能超过 4000 个字"})
		return
	}
	if _, ok := h.repo.Project(req.ProjectID); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project not found"})
		return
	}
	if !h.rooms.roomAccess(c, req.ProjectID, true) {
		return
	}
	if req.IssueID = strings.TrimSpace(req.IssueID); req.IssueID != "" {
		issue, ok := h.repo.Issue(req.IssueID)
		if !ok || issue.ProjectID != req.ProjectID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "需求不存在或不属于这个项目"})
			return
		}
		if existing, ok := h.repo.SpaceByIssue(issue.ID); ok {
			if room, ok := h.repo.Room(existing.ID); ok {
				c.JSON(http.StatusOK, h.view(room))
				return
			}
		}
	}
	roster := h.rosterIndex(req.ProjectID)
	for _, m := range req.Members {
		if _, ok := roster[m.UserID]; !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "只能邀请项目成员，请先把 TA 加入项目"})
			return
		}
	}
	userID, author := h.rooms.roomActor(c)
	room := domain.ChatRoom{ID: newChatID("room"), ProjectID: req.ProjectID, Name: req.Title, CreatedBy: userID}
	if err := h.repo.PutRoom(room); err != nil {
		respond(c, err)
		return
	}
	room, _ = h.repo.Room(room.ID)
	space := h.spaceFor(room)
	space.Summary = req.Summary
	space.CreatedBy = userID
	space.IssueID = req.IssueID
	if err := h.saveSpace(room, space); err != nil {
		respond(c, err)
		return
	}
	if userID > 0 {
		name := author
		if m, ok := roster[userID]; ok {
			name = m.Name
		}
		_ = h.repo.PutSpaceMember(domain.SpaceMember{SpaceID: room.ID, UserID: userID, Name: name, Title: roomTrim(req.CreatorTitle, 20), AddedBy: userID})
	}
	for _, m := range req.Members {
		if m.UserID == userID {
			continue
		}
		_ = h.repo.PutSpaceMember(domain.SpaceMember{SpaceID: room.ID, UserID: m.UserID, Name: roster[m.UserID].Name, Title: roomTrim(m.Title, 20), AddedBy: userID})
	}
	if req := h.requirementOf(space); req != nil {
		_, _ = h.post(room, domain.ChatMessage{UserID: userID, Author: author, Kind: domain.ChatKindMilestone, IssueID: req.ID, IssueNumber: req.Number, IssueTitle: req.Title,
			Body: fmt.Sprintf("为需求 #%d「%s」开了这个会话", req.Number, req.Title)})
	}
	c.JSON(http.StatusCreated, h.view(room))
}

func (h *spacesHandlers) rosterIndex(projectID string) map[int64]domain.ChatMember {
	out := map[int64]domain.ChatMember{}
	for _, m := range h.rooms.projectRoster(projectID) {
		out[m.UserID] = m
	}
	return out
}

func (h *spacesHandlers) view(room domain.ChatRoom) spaceView {
	space := h.spaceFor(room)
	v := spaceView{RequirementSpace: space, Title: room.Name, Requirement: h.requirementOf(space)}
	if project, ok := h.repo.Project(room.ProjectID); ok {
		v.ProjectName = project.Name
	}
	roster := h.rosterIndex(room.ProjectID)
	for _, m := range h.repo.SpaceMembers(room.ID) {
		v.Members = append(v.Members, spaceMemberView{SpaceMember: m, ProjectRole: roster[m.UserID].Role})
	}
	if v.Members == nil {
		v.Members = []spaceMemberView{}
	}
	v.Artifacts = h.repo.SpaceArtifacts(room.ID)
	for i := range v.Artifacts {
		v.Artifacts[i].Body = ""
	}
	counts := h.repo.ThreadMessageCounts(room.ID)
	v.Threads = []spaceThreadView{}
	for _, t := range h.repo.SpaceThreads(room.ID) {
		v.Threads = append(v.Threads, spaceThreadView{SpaceThread: t, Count: counts[t.ID]})
	}
	v.Lanes = h.lanes(room, space)
	sig := h.repo.RoomSignals([]string{room.ID})[room.ID]
	v.PendingApprovals, v.LiveRuns = sig.PendingApprovals, sig.LiveRuns
	return v
}

func (h *spacesHandlers) space(c *gin.Context) {
	room, space, ok := h.loadSpace(c, c.Request.Method != http.MethodGet)
	if !ok {
		return
	}
	switch c.Request.Method {
	case http.MethodGet:
		c.JSON(http.StatusOK, h.view(room))
	case http.MethodPatch:
		var req struct {
			Title   *string `json:"title"`
			Summary *string `json:"summary"`
			Pinned  *bool   `json:"pinned"`
		}
		if !decode(c, &req) {
			return
		}
		if req.Title != nil {
			title := strings.TrimSpace(*req.Title)
			if title == "" || len([]rune(title)) > 80 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "标题需要 1 到 80 个字"})
				return
			}
			room.Name = title
			if err := h.repo.PutRoom(room); err != nil {
				respond(c, err)
				return
			}
		}
		if req.Summary != nil {
			if len([]rune(*req.Summary)) > 4000 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "描述不能超过 4000 个字"})
				return
			}
			space.Summary = strings.TrimSpace(*req.Summary)
		}
		if req.Pinned != nil {
			space.Pinned = *req.Pinned
		}
		if err := h.saveSpace(room, space); err != nil {
			respond(c, err)
			return
		}
		room, _ = h.repo.Room(room.ID)
		c.JSON(http.StatusOK, h.view(room))
	default:
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}

var issuePriorities = map[string]bool{"urgent": true, "high": true, "medium": true, "low": true}

// requirement turns the conversation into a platform requirement the user
// asked for: it creates the issue, links it and notes it on the timeline.
// A conversation carries at most one requirement.
func (h *spacesHandlers) requirement(c *gin.Context) {
	room, space, ok := h.loadSpace(c, true)
	if !ok {
		return
	}
	if existing := h.requirementOf(space); existing != nil {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("这个会话已经关联了需求 #%d", existing.Number)})
		return
	}
	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Priority    string `json:"priority"`
	}
	if !decode(c, &req) {
		return
	}
	req.Title, req.Description = strings.TrimSpace(req.Title), strings.TrimSpace(req.Description)
	if req.Title == "" || len([]rune(req.Title)) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需求标题需要 1 到 200 个字"})
		return
	}
	if len([]rune(req.Description)) > 8000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需求描述不能超过 8000 个字"})
		return
	}
	if req.Priority == "" {
		req.Priority = "medium"
	}
	if !issuePriorities[req.Priority] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "优先级只能是 urgent、high、medium 或 low"})
		return
	}
	userID, author := h.rooms.roomActor(c)
	issue, err := h.createIssue(room, req.Title, req.Description, req.Priority, userID, author)
	if err != nil {
		respond(c, err)
		return
	}
	space.IssueID = issue.ID
	if err := h.saveSpace(room, space); err != nil {
		respond(c, err)
		return
	}
	_, _ = h.post(room, domain.ChatMessage{UserID: userID, Author: author, Kind: domain.ChatKindMilestone,
		IssueID: issue.ID, IssueNumber: issue.Number, IssueTitle: issue.Title,
		Body: fmt.Sprintf("建了需求 #%d「%s」", issue.Number, issue.Title)})
	c.JSON(http.StatusCreated, h.view(room))
}

func (h *spacesHandlers) createIssue(room domain.ChatRoom, title, description, priority string, userID int64, author string) (domain.Issue, error) {
	issue := domain.Issue{
		ID: newChatID("iss"), ProjectID: room.ProjectID, Title: title, Description: description,
		Status: "todo", Priority: priority, Kind: "issue", Source: "local",
	}
	if userID > 0 {
		issue.AssigneeUserID = userID
	}
	issue.AppendOperation(domain.IssueOperation{Actor: author, ActorUserID: userID, Action: "create", Detail: "从会话「" + room.Name + "」建需求"})
	if err := h.repo.PutIssue(issue); err != nil {
		return issue, err
	}
	stored, _ := h.repo.Issue(issue.ID)
	return stored, nil
}

func (h *spacesHandlers) members(c *gin.Context) {
	room, _, ok := h.loadSpace(c, c.Request.Method != http.MethodGet)
	if !ok {
		return
	}
	roster := h.rosterIndex(room.ProjectID)
	switch c.Request.Method {
	case http.MethodGet:
		ids := make([]int64, 0, len(roster))
		for id := range roster {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		list := make([]domain.ChatMember, 0, len(ids))
		for _, id := range ids {
			list = append(list, roster[id])
		}
		c.JSON(http.StatusOK, gin.H{"members": h.view(room).Members, "roster": list})
	case http.MethodPost:
		var req spaceMemberRequest
		if !decode(c, &req) {
			return
		}
		member, ok := roster[req.UserID]
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "只能邀请项目成员，请先把 TA 加入项目"})
			return
		}
		userID, author := h.rooms.roomActor(c)
		existing := false
		for _, m := range h.repo.SpaceMembers(room.ID) {
			if m.UserID == req.UserID {
				existing = true
			}
		}
		if err := h.repo.PutSpaceMember(domain.SpaceMember{SpaceID: room.ID, UserID: req.UserID, Name: member.Name, Title: roomTrim(req.Title, 20), AddedBy: userID}); err != nil {
			respond(c, err)
			return
		}
		if !existing {
			body := "邀请了 " + member.Name
			if t := roomTrim(req.Title, 20); t != "" {
				body += "（" + t + "）"
			}
			_, _ = h.post(room, domain.ChatMessage{UserID: userID, Author: author, Kind: domain.ChatKindMilestone, Body: body})
		}
		c.JSON(http.StatusOK, h.view(room).Members)
	case http.MethodDelete:
		id, err := strconv.ParseInt(c.Query("userId"), 10, 64)
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "userId is required"})
			return
		}
		if err := h.repo.DeleteSpaceMember(room.ID, id); err != nil {
			respond(c, err)
			return
		}
		c.JSON(http.StatusOK, h.view(room).Members)
	default:
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}

func (h *spacesHandlers) threads(c *gin.Context) {
	room, _, ok := h.loadSpace(c, c.Request.Method != http.MethodGet)
	if !ok {
		return
	}
	switch c.Request.Method {
	case http.MethodGet:
		c.JSON(http.StatusOK, h.view(room).Threads)
	case http.MethodPost:
		var req struct {
			Title     string `json:"title"`
			RunID     string `json:"runId"`
			MessageID string `json:"messageId"`
		}
		if !decode(c, &req) {
			return
		}
		if id := strings.TrimSpace(req.MessageID); id != "" {
			origin, ok := h.repo.Message(room.ID, id)
			if !ok || origin.ThreadID != "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "只能从主线消息开子线程"})
				return
			}
			for _, t := range h.repo.SpaceThreads(room.ID) {
				if t.MessageID == id {
					c.JSON(http.StatusOK, t)
					return
				}
			}
			if strings.TrimSpace(req.Title) == "" {
				req.Title = roomTrim(strings.Join(strings.Fields(origin.Body), " "), 40)
			}
		}
		thread, err := h.ensureThread(c, room, strings.TrimSpace(req.Title), strings.TrimSpace(req.RunID))
		if err != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": err})
			return
		}
		if req.MessageID != "" && thread.RunID == "" {
			thread.MessageID = strings.TrimSpace(req.MessageID)
			if err := h.repo.PutSpaceThread(thread); err != nil {
				respond(c, err)
				return
			}
		}
		c.JSON(http.StatusOK, thread)
	default:
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}

// ensureThread returns the run's thread when runID is set (creating it on
// first use) or opens a new discussion thread.
func (h *spacesHandlers) ensureThread(c *gin.Context, room domain.ChatRoom, title, runID string) (domain.SpaceThread, string) {
	userID, _ := h.rooms.roomActor(c)
	if runID != "" {
		if t, ok := h.repo.SpaceRunThread(room.ID, runID); ok {
			return t, ""
		}
		dispatch, ok := h.repo.DispatchMessage(room.ID, runID)
		if !ok {
			return domain.SpaceThread{}, "这个运行不是从当前会话派发的"
		}
		if title == "" {
			title = fallbackName(dispatch.WorkflowName, dispatch.WorkflowID)
		}
	}
	if title == "" || len([]rune(title)) > 80 {
		return domain.SpaceThread{}, "子线程标题需要 1 到 80 个字"
	}
	t := domain.SpaceThread{ID: newChatID("thr"), SpaceID: room.ID, Title: title, RunID: runID, CreatedBy: userID}
	if err := h.repo.PutSpaceThread(t); err != nil {
		return t, err.Error()
	}
	return t, ""
}

func (h *spacesHandlers) messages(c *gin.Context) {
	room, space, ok := h.loadSpace(c, c.Request.Method != http.MethodGet)
	if !ok {
		return
	}
	threadID := strings.TrimSpace(c.Query("threadId"))
	switch c.Request.Method {
	case http.MethodGet:
		if threadID != "" {
			if _, ok := h.repo.SpaceThread(room.ID, threadID); !ok {
				c.JSON(http.StatusNotFound, gin.H{"error": "子线程不存在"})
				return
			}
			c.JSON(http.StatusOK, h.reconcileAI(room, h.repo.TimelineMessages(room.ID, threadID, 200)))
			return
		}
		synced := h.rooms.syncRoomRuns(room, h.repo.TimelineMessages(room.ID, "", 200))
		main := synced[:0:0]
		for _, m := range synced {
			if m.ThreadID == "" {
				main = append(main, m)
			}
		}
		c.JSON(http.StatusOK, h.reconcileAI(room, main))
	case http.MethodPost:
		var req struct {
			Body         string `json:"body"`
			ThreadID     string `json:"threadId"`
			ToAI         bool   `json:"toAi"`
			Model        string `json:"model"`
			ApprovalMode string `json:"approvalMode"`
			AISessionID  string `json:"aiSessionId"`
			Attachments  []struct {
				ID string `json:"id"`
			} `json:"attachments"`
		}
		if !decode(c, &req) {
			return
		}
		body := strings.TrimSpace(req.Body)
		if body == "" && len(req.Attachments) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "消息不能为空"})
			return
		}
		if len(req.Attachments) > workbenchAttachmentCount {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("一条消息最多附加 %d 个文件", workbenchAttachmentCount)})
			return
		}
		if len([]rune(body)) > 4000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "消息不能超过 4000 个字"})
			return
		}
		threadID = strings.TrimSpace(req.ThreadID)
		if threadID != "" {
			if _, ok := h.repo.SpaceThread(room.ID, threadID); !ok {
				c.JSON(http.StatusNotFound, gin.H{"error": "子线程不存在"})
				return
			}
		}
		userID, author := h.rooms.roomActor(c)
		var attachments []domain.AssistantAttachment
		for _, a := range req.Attachments {
			record, _, err := loadWorkbenchAttachment(userID, a.ID)
			if userID == 0 || err != nil || record.ProjectID != room.ProjectID {
				c.JSON(http.StatusBadRequest, gin.H{"error": "附件不存在或不属于当前用户和项目，请重新上传"})
				return
			}
			attachments = append(attachments, record.AssistantAttachment)
		}
		msg, err := h.post(room, domain.ChatMessage{UserID: userID, Author: author, Kind: "text", Body: body, ThreadID: threadID, ToAI: req.ToAI, Attachments: attachments})
		if err != nil {
			respond(c, err)
			return
		}
		out := gin.H{"message": msg}
		if req.ToAI {
			reply := h.askAI(c, room, space, msg, strings.TrimSpace(req.Model), strings.TrimSpace(req.ApprovalMode), strings.TrimSpace(req.AISessionID))
			out["reply"] = reply
		}
		c.JSON(http.StatusCreated, out)
	default:
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}

// attachment serves a file posted in the space to anyone who can read it.
func (h *spacesHandlers) attachment(c *gin.Context) {
	room, _, ok := h.loadSpace(c, false)
	if !ok {
		return
	}
	id := c.Param("attachmentId")
	if !validWorkbenchAttachmentID(id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "附件不存在"})
		return
	}
	msg, found := h.repo.MessageWithAttachment(room.ID, id)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "附件不在这个会话里"})
		return
	}
	record, file, err := openWorkbenchAttachment(msg.UserID, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "附件不存在"})
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || record.ProjectID != room.ProjectID {
		c.JSON(http.StatusNotFound, gin.H{"error": "附件不存在"})
		return
	}
	disposition := "attachment"
	if strings.HasPrefix(record.MimeType, "image/") && record.MimeType != "image/svg+xml" && c.Query("download") == "" {
		disposition = "inline"
	}
	c.Header("Content-Type", record.MimeType)
	c.Header("Cache-Control", "private, max-age=86400")
	c.Header("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": record.Name}))
	http.ServeContent(c.Writer, c.Request, record.Name, info.ModTime(), file)
}

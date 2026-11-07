package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

type spaceArtifactRequest struct {
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Format      string `json:"format"`
	Body        string `json:"body"`
	URL         string `json:"url"`
	BaseVersion int    `json:"baseVersion"`
	// Announce posts the new version to the main timeline.
	Announce bool `json:"announce"`
}

var spaceArtifactFormats = map[string]bool{"": true, "markdown": true, "html": true, "json": true, "text": true, "png": true, "jpeg": true, "gif": true, "webp": true}

func (h *spacesHandlers) artifacts(c *gin.Context) {
	room, _, ok := h.loadSpace(c, c.Request.Method != http.MethodGet)
	if !ok {
		return
	}
	switch c.Request.Method {
	case http.MethodGet:
		list := h.repo.SpaceArtifacts(room.ID)
		for i := range list {
			list[i].Body = ""
		}
		c.JSON(http.StatusOK, list)
	case http.MethodPost:
		var req spaceArtifactRequest
		if !decode(c, &req) {
			return
		}
		h.writeArtifact(c, room, domain.SpaceArtifact{ID: newChatID("art"), SpaceID: room.ID}, req, 0, true)
	default:
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}

func (h *spacesHandlers) artifact(c *gin.Context) {
	room, _, ok := h.loadSpace(c, c.Request.Method != http.MethodGet)
	if !ok {
		return
	}
	current, found := h.repo.SpaceArtifact(room.ID, c.Param("artifactId"))
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "产物不存在"})
		return
	}
	switch c.Request.Method {
	case http.MethodGet:
		c.JSON(http.StatusOK, current)
	case http.MethodPut:
		var req spaceArtifactRequest
		if !decode(c, &req) {
			return
		}
		if req.Kind == "" {
			req.Kind = current.Kind
		}
		h.writeArtifact(c, room, current, req, req.BaseVersion, req.Announce)
	case http.MethodDelete:
		if err := h.repo.DeleteSpaceArtifact(room.ID, current.ID); err != nil {
			respond(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	default:
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}

func (h *spacesHandlers) writeArtifact(c *gin.Context, room domain.ChatRoom, base domain.SpaceArtifact, req spaceArtifactRequest, baseVersion int, announce bool) {
	if !spaceArtifactFormats[req.Format] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "format 只能是 markdown、html、json、text 或图片"})
		return
	}
	userID, author := h.rooms.roomActor(c)
	a := base
	a.Kind, a.Title, a.Summary = req.Kind, strings.TrimSpace(req.Title), roomTrim(strings.TrimSpace(req.Summary), 500)
	a.Format, a.Body, a.URL = req.Format, req.Body, strings.TrimSpace(req.URL)
	a.AuthorID, a.AuthorName, a.ByAI = userID, author, false
	if err := a.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	saved, err := h.repo.PutSpaceArtifact(a, baseVersion)
	if errors.Is(err, repo.ErrArtifactConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		respond(c, err)
		return
	}
	if announce {
		verb := "发布了"
		if saved.Version > 1 {
			verb = "更新了"
		}
		_, _ = h.post(room, domain.ChatMessage{UserID: userID, Author: author, Kind: domain.ChatKindArtifact, ArtifactID: saved.ID, ArtifactVersion: saved.Version, Body: fmt.Sprintf("%s「%s」v%d", verb, saved.Title, saved.Version)})
	}
	status := http.StatusOK
	if saved.Version == 1 {
		status = http.StatusCreated
	}
	c.JSON(status, saved)
}

func (h *spacesHandlers) artifactRevisions(c *gin.Context) {
	room, _, ok := h.loadSpace(c, false)
	if !ok {
		return
	}
	id := c.Param("artifactId")
	if v := c.Param("version"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "version 无效"})
			return
		}
		item, ok := h.repo.SpaceArtifactRevision(room.ID, id, n)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "版本不存在"})
			return
		}
		c.JSON(http.StatusOK, item)
		return
	}
	c.JSON(http.StatusOK, h.repo.SpaceArtifactRevisions(room.ID, id))
}

// artifactRaw serves one artifact version as a document. HTML prototypes run
// in an opaque-origin sandbox: their scripts can drive the prototype but can
// never reach the platform's cookies, storage or API.
func (h *spacesHandlers) artifactRaw(c *gin.Context) {
	room, _, ok := h.loadSpace(c, false)
	if !ok {
		return
	}
	id := c.Param("artifactId")
	item, found := h.repo.SpaceArtifact(room.ID, id)
	if v := strings.TrimSpace(c.Query("version")); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			item, found = h.repo.SpaceArtifactRevision(room.ID, id, n)
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "产物不存在"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	if mime, ok := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp"}[item.Format]; ok {
		raw, err := decodeArtifactImage(item.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "图片内容无法读取"})
			return
		}
		c.Header("Content-Security-Policy", "default-src 'none'; img-src 'self'; frame-ancestors 'self'")
		c.Data(http.StatusOK, mime, raw)
		return
	}
	if item.Format == "html" {
		c.Header("Content-Security-Policy", "sandbox allow-scripts allow-forms; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: https:; font-src data: https:; frame-ancestors 'self'")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(item.Body))
		return
	}
	c.Header("Content-Security-Policy", "default-src 'none'; frame-ancestors 'self'")
	c.Header("X-Frame-Options", "SAMEORIGIN")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(item.Body))
}

func decodeArtifactImage(body string) ([]byte, error) {
	s := strings.TrimSpace(body)
	if strings.HasPrefix(s, "data:") {
		if i := strings.Index(s, ","); i >= 0 {
			s = s[i+1:]
		}
	}
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, s)
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("图片内容无法读取")
	}
	return raw, nil
}

func sniffImageFormat(raw []byte) string {
	switch {
	case len(raw) >= 8 && bytes.HasPrefix(raw, []byte{0x89, 'P', 'N', 'G'}):
		return "png"
	case len(raw) >= 3 && raw[0] == 0xff && raw[1] == 0xd8:
		return "jpeg"
	case len(raw) >= 6 && (bytes.HasPrefix(raw, []byte("GIF87a")) || bytes.HasPrefix(raw, []byte("GIF89a"))):
		return "gif"
	case len(raw) >= 12 && string(raw[8:12]) == "WEBP":
		return "webp"
	default:
		return ""
	}
}

func looksLikeLocalPath(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "\n") || len(s) > 500 {
		return false
	}
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/") || strings.Contains(s, ":\\")
}

// publishWorkbenchArtifact stores a demo, slide deck, image or document on
// the conversation so the browser can open it. A path on this machine is not
// something the user can see.
func (h *agentToolGateway) publishWorkbenchArtifact(session *AgentToolSession, raw json.RawMessage) (map[string]any, error) {
	if session == nil || session.Mode != AgentToolAssistant || strings.TrimSpace(session.SpaceID) == "" || session.terminalToolsOnly() {
		return mcpToolTextResult("当前会话不能发布产物", true), nil
	}
	var args struct {
		Title       string `json:"title"`
		Kind        string `json:"kind"`
		Format      string `json:"format"`
		Body        string `json:"body"`
		URL         string `json:"url"`
		Summary     string `json:"summary"`
		ImageBase64 string `json:"imageBase64"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpToolTextResult("参数不是 JSON", true), nil
	}
	room, ok := h.repo.Room(session.SpaceID)
	if !ok || room.ProjectID != session.ProjectID {
		return mcpToolTextResult("当前会话不存在，不能发布", true), nil
	}
	title := strings.TrimSpace(args.Title)
	if title == "" {
		return mcpToolTextResult("需要标题", true), nil
	}
	body, format, kind := args.Body, strings.TrimSpace(args.Format), strings.TrimSpace(args.Kind)
	if strings.TrimSpace(args.ImageBase64) != "" {
		img, err := decodeArtifactImage(args.ImageBase64)
		if err != nil {
			return mcpToolTextResult("imageBase64 不是有效的 png、jpeg、gif 或 webp", true), nil
		}
		if len(img) > 700<<10 {
			return mcpToolTextResult("图片不能超过 700 KB，侧边栏才能直接打开", true), nil
		}
		sniffed := sniffImageFormat(img)
		if sniffed == "" {
			return mcpToolTextResult("只接受 png、jpeg、gif 或 webp", true), nil
		}
		body, format = base64.StdEncoding.EncodeToString(img), sniffed
		if kind == "" {
			kind = "file"
		}
	} else if looksLikeLocalPath(body) && strings.TrimSpace(args.URL) == "" {
		return mcpToolTextResult("不要提交本机路径。把 HTML、Markdown 或图片内容本身传进来。", true), nil
	}
	if kind == "" {
		if strings.TrimSpace(args.URL) != "" && strings.TrimSpace(body) == "" {
			kind = "browser"
		} else {
			kind = "doc"
		}
	}
	if format == "" {
		switch kind {
		case "prototype":
			format = "html"
		case "ppt":
			format = "markdown"
		default:
			format = "markdown"
		}
	}
	if !spaceArtifactFormats[format] {
		return mcpToolTextResult("format 只能是 markdown、html、json、text 或图片", true), nil
	}
	author := strings.TrimSpace(session.User.ChineseName)
	if author == "" {
		author = strings.TrimSpace(session.User.LoginName)
	}
	if author == "" {
		author = spaceAIAuthor
	}
	item := domain.SpaceArtifact{
		ID: newChatID("art"), SpaceID: room.ID, Kind: kind, Title: title,
		Summary: roomTrim(strings.TrimSpace(args.Summary), 500), Format: format,
		Body: body, URL: strings.TrimSpace(args.URL), AuthorID: session.UserID,
		AuthorName: author, ByAI: true,
	}
	if err := item.Validate(); err != nil {
		return mcpToolTextResult(err.Error(), true), nil
	}
	saved, err := h.repo.PutSpaceArtifact(item, 0)
	if err != nil {
		return mcpToolTextResult(err.Error(), true), nil
	}
	now := time.Now().UTC()
	_ = h.repo.PutMessage(domain.ChatMessage{
		ID: newChatID("msg"), RoomID: room.ID, UserID: session.UserID, Author: spaceAIAuthor,
		Kind: domain.ChatKindArtifact, ArtifactID: saved.ID, ArtifactVersion: saved.Version,
		Body: fmt.Sprintf("发布了「%s」", saved.Title), CreatedAt: now,
	})
	room.UpdatedAt = now
	_ = h.repo.PutRoom(room)
	preview := "/api/spaces/" + room.ID + "/artifacts/" + saved.ID + "/raw"
	text := fmt.Sprintf("已发布到当前会话，用户可以在侧边栏打开。\n标题：%s\n类型：%s\n预览：%s\n回复里只提标题，不要再给本机路径。", saved.Title, saved.Kind, preview)
	return mcpToolTextResult(text, false), nil
}

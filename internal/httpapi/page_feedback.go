package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"agentflow/internal/auth"
	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// feedbackHandlers owns the floating page feedback widget: submit plus
// read-only listing, detail and image download. Reports belong to no project.
type feedbackHandlers struct {
	*deps
}

const (
	feedbackMaxBody        = 27 << 20 // 5 images × 5MB plus 2MB for the payload and multipart framing
	feedbackMaxImages      = 5
	feedbackMaxImageBytes  = 5 << 20
	feedbackMaxTitle       = 120
	feedbackMaxDescription = 5000
	feedbackMaxElementText = 200
	feedbackMaxURL         = 2048
	feedbackDefaultLimit   = 20
	feedbackMaxLimit       = 100
)

var feedbackImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// feedbackPayload is the JSON part of the submit form. It deliberately has no
// submitter fields: identity comes from the session only.
type feedbackPayload struct {
	Title       string                      `json:"title"`
	Description string                      `json:"description"`
	URL         string                      `json:"url"`
	Route       string                      `json:"route"`
	View        string                      `json:"view"`
	FrameURL    string                      `json:"frameUrl"`
	Context     domain.PageFeedbackContext  `json:"context"`
	Element     *domain.PageFeedbackElement `json:"element"`
}

func feedbackError(c *gin.Context, status int, message string) {
	c.JSON(status, map[string]string{"error": message})
}

// feedbackUser accepts only a signed-in, non-guest platform user. Without
// allowBearer an Authorization header is refused outright, because the global
// middleware has already resolved bearer users into the request context.
func (h *feedbackHandlers) feedbackUser(c *gin.Context, allowBearer bool) (*auth.User, bool) {
	if _, hasBearer := bearerToken(c.Request); hasBearer && !allowBearer {
		feedbackError(c, http.StatusUnauthorized, "提交反馈需使用登录会话，不支持 API Token")
		return nil, false
	}
	user, ok := h.requestUser(c.Request, allowBearer)
	if !ok || user == nil || user.IsGuest || user.ID <= 0 {
		feedbackError(c, http.StatusUnauthorized, "请先登录")
		return nil, false
	}
	return user, true
}

func (h *feedbackHandlers) submit(c *gin.Context) {
	user, ok := h.feedbackUser(c, false)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, feedbackMaxBody)
	if err := c.Request.ParseMultipartForm(8 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			feedbackError(c, http.StatusRequestEntityTooLarge, "提交内容过大，图片总大小请控制在 25MB 以内")
			return
		}
		feedbackError(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	defer c.Request.MultipartForm.RemoveAll()
	form := c.Request.MultipartForm

	var payload feedbackPayload
	raw := ""
	if values := form.Value["payload"]; len(values) > 0 {
		raw = values[0]
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		feedbackError(c, http.StatusBadRequest, "反馈内容格式不正确")
		return
	}
	fb, msg := normalizeFeedbackPayload(payload)
	if msg != "" {
		feedbackError(c, http.StatusBadRequest, msg)
		return
	}
	images, msg := readFeedbackImages(form.File["images"])
	if msg != "" {
		feedbackError(c, http.StatusBadRequest, msg)
		return
	}
	login := strings.TrimSpace(user.LoginName)
	chinese := strings.TrimSpace(user.ChineseName)
	if chinese == "" {
		chinese = login
	}
	fb.Submitter = domain.PageFeedbackUser{UserID: user.ID, LoginName: login, ChineseName: chinese}
	stored, err := h.repo.CreatePageFeedback(fb, images)
	if err != nil {
		feedbackError(c, http.StatusInternalServerError, "保存反馈失败，请稍后重试")
		return
	}
	c.JSON(http.StatusCreated, withFeedbackImageURLs(stored))
}

func (h *feedbackHandlers) list(c *gin.Context) {
	if _, ok := h.feedbackUser(c, true); !ok {
		return
	}
	limit := feedbackDefaultLimit
	if v, err := strconv.Atoi(strings.TrimSpace(c.Query("limit"))); err == nil && v > 0 {
		limit = min(v, feedbackMaxLimit)
	}
	offset := 0
	if v, err := strconv.Atoi(strings.TrimSpace(c.Query("offset"))); err == nil && v > 0 {
		offset = v
	}
	items, total, err := h.repo.PageFeedbacks(limit, offset)
	if err != nil {
		feedbackError(c, http.StatusInternalServerError, "读取反馈失败")
		return
	}
	for i := range items {
		items[i] = withFeedbackImageURLs(items[i])
	}
	c.JSON(http.StatusOK, map[string]any{
		"items": items, "total": total, "offset": offset, "limit": limit,
		"hasMore": int64(offset+len(items)) < total,
	})
}

func (h *feedbackHandlers) detail(c *gin.Context) {
	if _, ok := h.feedbackUser(c, true); !ok {
		return
	}
	fb, found, err := h.repo.PageFeedback(c.Param("id"))
	if err != nil {
		feedbackError(c, http.StatusInternalServerError, "读取反馈失败")
		return
	}
	if !found {
		feedbackError(c, http.StatusNotFound, "反馈不存在")
		return
	}
	c.JSON(http.StatusOK, withFeedbackImageURLs(fb))
}

func (h *feedbackHandlers) image(c *gin.Context) {
	if _, ok := h.feedbackUser(c, true); !ok {
		return
	}
	img, found, err := h.repo.PageFeedbackImage(c.Param("id"), c.Param("imageId"))
	if err != nil {
		feedbackError(c, http.StatusInternalServerError, "读取图片失败")
		return
	}
	if !found || !feedbackImageTypes[img.MimeType] {
		feedbackError(c, http.StatusNotFound, "图片不存在")
		return
	}
	disposition := "inline"
	if name := strings.TrimSpace(img.FileName); name != "" {
		if formatted := mime.FormatMediaType("inline", map[string]string{"filename": name}); formatted != "" {
			disposition = formatted
		}
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	c.Header("Content-Disposition", disposition)
	c.Header("Cache-Control", "private, max-age=86400")
	c.Data(http.StatusOK, img.MimeType, img.Data)
}

func withFeedbackImageURLs(fb domain.PageFeedback) domain.PageFeedback {
	if fb.Images == nil {
		fb.Images = []domain.PageFeedbackImage{}
	}
	for i := range fb.Images {
		fb.Images[i].URL = "/api/page-feedback/" + url.PathEscape(fb.ID) + "/images/" + url.PathEscape(fb.Images[i].ID)
	}
	return fb
}

// normalizeFeedbackPayload validates the user-written text and trims every
// captured field to its column budget. It returns a user-facing message on
// rejection.
func normalizeFeedbackPayload(p feedbackPayload) (domain.PageFeedback, string) {
	title := strings.TrimSpace(p.Title)
	if title == "" {
		return domain.PageFeedback{}, "请填写反馈内容"
	}
	if utf8.RuneCountInString(title) > feedbackMaxTitle {
		return domain.PageFeedback{}, fmt.Sprintf("标题不能超过 %d 字", feedbackMaxTitle)
	}
	description := strings.TrimSpace(p.Description)
	if utf8.RuneCountInString(description) > feedbackMaxDescription {
		return domain.PageFeedback{}, fmt.Sprintf("反馈内容不能超过 %d 字", feedbackMaxDescription)
	}
	fb := domain.PageFeedback{
		Title:       title,
		Description: description,
		URL:         clipRunes(sanitizeFeedbackURL(p.URL), feedbackMaxURL),
		Route:       clipRunes(sanitizeFeedbackRoute(p.Route), 512),
		View:        clipRunes(singleLine(p.View), 128),
		FrameURL:    clipRunes(sanitizeFeedbackURL(p.FrameURL), feedbackMaxURL),
		Context: domain.PageFeedbackContext{
			Scroll:     p.Context.Scroll,
			Viewport:   p.Context.Viewport,
			DPR:        p.Context.DPR,
			UserAgent:  clipRunes(singleLine(p.Context.UserAgent), 512),
			Language:   clipRunes(singleLine(p.Context.Language), 32),
			Theme:      clipRunes(singleLine(p.Context.Theme), 16),
			CapturedAt: clipRunes(singleLine(p.Context.CapturedAt), 40),
		},
	}
	if el := p.Element; el != nil && strings.TrimSpace(el.Selector) != "" {
		fb.Element = &domain.PageFeedbackElement{
			Selector: clipRunes(strings.TrimSpace(el.Selector), 1024),
			Tag:      clipRunes(singleLine(el.Tag), 32),
			Text:     clipRunes(strings.Join(strings.Fields(el.Text), " "), feedbackMaxElementText),
			Rect:     el.Rect,
			PageRect: el.PageRect,
			Point:    el.Point,
			InFrame:  el.InFrame,
		}
	}
	return fb, ""
}

// readFeedbackImages checks count, size and the sniffed content type. The
// client-declared type and extension are never trusted.
func readFeedbackImages(files []*multipart.FileHeader) ([]domain.PageFeedbackImage, string) {
	if len(files) > feedbackMaxImages {
		return nil, fmt.Sprintf("最多上传 %d 张图片", feedbackMaxImages)
	}
	out := make([]domain.PageFeedbackImage, 0, len(files))
	for _, header := range files {
		name := clipRunes(singleLine(filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/"))), 255)
		if name == "." || name == "/" {
			name = ""
		}
		label := name
		if label == "" {
			label = "图片"
		}
		if header.Size > feedbackMaxImageBytes {
			return nil, label + " 超过 5MB"
		}
		file, err := header.Open()
		if err != nil {
			return nil, "读取图片失败"
		}
		data, err := io.ReadAll(io.LimitReader(file, feedbackMaxImageBytes+1))
		file.Close()
		if err != nil {
			return nil, "读取图片失败"
		}
		if len(data) > feedbackMaxImageBytes {
			return nil, label + " 超过 5MB"
		}
		if len(data) == 0 {
			return nil, label + " 是空文件"
		}
		mimeType := http.DetectContentType(data)
		if !feedbackImageTypes[mimeType] {
			return nil, label + " 不是支持的图片格式（仅支持 PNG / JPEG / GIF / WebP）"
		}
		sum := sha256.Sum256(data)
		out = append(out, domain.PageFeedbackImage{
			FileName: name, MimeType: mimeType, Size: int64(len(data)),
			SHA256: hex.EncodeToString(sum[:]), Data: data,
		})
	}
	return out, ""
}

// Short words only match whole names or a _/- suffix ("auth_code", "api-key");
// long ones match anywhere ("access_token", "sessionId").
var (
	feedbackSensitiveExact     = []string{"code", "key", "sig", "pwd", "auth"}
	feedbackSensitiveFragments = []string{"token", "ticket", "session", "secret", "password", "passwd", "credential", "signature", "apikey"}
)

func feedbackSensitiveParam(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, key := range feedbackSensitiveExact {
		if name == key || strings.HasSuffix(name, "_"+key) || strings.HasSuffix(name, "-"+key) {
			return true
		}
	}
	for _, key := range feedbackSensitiveFragments {
		if strings.Contains(name, key) {
			return true
		}
	}
	return false
}

// stripSensitiveQuery drops credential-looking parameters from a raw query
// string and keeps the order of the rest.
func stripSensitiveQuery(raw string) string {
	if raw == "" {
		return ""
	}
	kept := make([]string, 0)
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		name := part
		if i := strings.IndexByte(part, '='); i >= 0 {
			name = part[:i]
		}
		if decoded, err := url.QueryUnescape(name); err == nil {
			name = decoded
		}
		if feedbackSensitiveParam(name) {
			continue
		}
		kept = append(kept, part)
	}
	return strings.Join(kept, "&")
}

// sanitizeFeedbackRoute cleans a hash route such as "#/runs?token=x".
func sanitizeFeedbackRoute(route string) string {
	route = singleLine(route)
	path, query, found := strings.Cut(route, "?")
	if !found {
		return route
	}
	if query = stripSensitiveQuery(query); query != "" {
		return path + "?" + query
	}
	return path
}

// sanitizeFeedbackURL removes credentials, sensitive query parameters (also
// inside a hash route) and anything that is not an http(s) URL.
func sanitizeFeedbackURL(raw string) string {
	raw = singleLine(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = stripSensitiveQuery(u.RawQuery)
	u.ForceQuery = false
	if u.Fragment != "" {
		u.Fragment = sanitizeFeedbackRoute(u.Fragment)
		u.RawFragment = ""
	}
	return u.String()
}

func singleLine(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s))
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

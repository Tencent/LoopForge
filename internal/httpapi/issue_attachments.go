package httpapi

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	issueAttachmentLimit      = 10 << 20
	issueAttachmentTotalLimit = 20 << 20
	issueAttachmentCountLimit = 8
)

type issueWriteRequest struct {
	domain.Issue
	SyncTarget string  `json:"syncTarget"`
	TapdID     *string `json:"tapdId"`
	BaseBranch *string `json:"baseBranch"`
}

type issueUpload struct {
	file      *multipart.FileHeader
	imageOnly bool
}

func decodeIssueWrite(c *gin.Context, req *issueWriteRequest) ([]issueUpload, bool) {
	r := c.Request
	if r.Method != http.MethodPost || c.ContentType() != "multipart/form-data" {
		return nil, decode(c, req)
	}
	r.Body = http.MaxBytesReader(c.Writer, r.Body, issueAttachmentTotalLimit+(2<<20)+(64<<10))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) || errors.Is(err, multipart.ErrMessageTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "需求附件总大小不能超过 20 MB"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法读取需求和附件"})
		}
		return nil, false
	}
	fields := r.MultipartForm.Value["issue"]
	if len(fields) != 1 || len(fields[0]) > 2<<20 || json.Unmarshal([]byte(fields[0]), req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无法读取需求内容"})
		return nil, false
	}
	var uploads []issueUpload
	for _, file := range r.MultipartForm.File["images"] {
		uploads = append(uploads, issueUpload{file: file, imageOnly: true})
	}
	for _, file := range r.MultipartForm.File["attachments"] {
		uploads = append(uploads, issueUpload{file: file})
	}
	return uploads, true
}

func readIssueAttachments(c *gin.Context, files []issueUpload) ([]repo.IssueAttachment, bool) {
	if len(files) > issueAttachmentCountLimit {
		c.JSON(http.StatusBadRequest, gin.H{"error": "每条需求最多添加 8 个附件"})
		return nil, false
	}
	attachments := make([]repo.IssueAttachment, 0, len(files))
	var total int64
	for _, upload := range files {
		header := upload.file
		total += header.Size
		if header.Size > issueAttachmentLimit || total > issueAttachmentTotalLimit {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "单个附件最多 10 MB，合计最多 20 MB"})
			return nil, false
		}
		file, err := header.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法打开需求附件"})
			return nil, false
		}
		data, err := io.ReadAll(io.LimitReader(file, issueAttachmentLimit+1))
		_ = file.Close()
		if err != nil || len(data) == 0 || len(data) > issueAttachmentLimit {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法读取需求附件或文件为空"})
			return nil, false
		}
		attachment := repo.IssueAttachment{ID: rand.Text(), Name: repo.IssueAttachmentName(header.Filename), MediaType: http.DetectContentType(data), Data: data}
		if !attachment.IsImage() && upload.imageOnly {
			c.JSON(http.StatusBadRequest, gin.H{"error": "图片格式须为 PNG、JPEG、WebP 或 GIF"})
			return nil, false
		}
		if !attachment.IsImage() {
			attachment.MediaType = "application/octet-stream"
		}
		attachments = append(attachments, attachment)
	}
	return attachments, true
}

func (h issuesHandlers) issueImage(c *gin.Context, issue domain.Issue) {
	h.serveIssueAttachment(c, issue, c.Param("imageId"), true)
}

func (h issuesHandlers) issueAttachment(c *gin.Context, issue domain.Issue) {
	h.serveIssueAttachment(c, issue, c.Param("attachmentId"), false)
}

func (h issuesHandlers) issueAttachmentReaderAllowed(c *gin.Context, issue domain.Issue) bool {
	if !authRequired() || h.isPlatformAdmin(c.Request) {
		return true
	}
	user, signedIn := h.requestUser(c.Request, true)
	project, found := h.repo.Project(issue.ProjectID)
	return signedIn && found && workbenchProjectAllowsUser(project, user.ID, false)
}

func (h issuesHandlers) serveIssueAttachment(c *gin.Context, issue domain.Issue, id string, inlineImage bool) {
	if !h.issueAttachmentReaderAllowed(c, issue) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问需求附件"})
		return
	}
	file, err := h.repo.IssueAttachment(issue.ID, id)
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && inlineImage && !file.IsImage()) {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到需求附件"})
		return
	}
	if err != nil {
		respond(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	if !inlineImage {
		c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file.Name}))
	}
	c.Data(http.StatusOK, file.MediaType, file.Data)
}

package httpapi

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	issueImageLimit      = 10 << 20
	issueImageTotalLimit = 20 << 20
	issueImageCountLimit = 8
)

type issueWriteRequest struct {
	domain.Issue
	SyncTarget string  `json:"syncTarget"`
	TapdID     *string `json:"tapdId"`
	BaseBranch *string `json:"baseBranch"`
}

func decodeIssueWrite(c *gin.Context, req *issueWriteRequest) ([]*multipart.FileHeader, bool) {
	r := c.Request
	if r.Method != http.MethodPost || c.ContentType() != "multipart/form-data" {
		return nil, decode(c, req)
	}
	r.Body = http.MaxBytesReader(c.Writer, r.Body, issueImageTotalLimit+(2<<20)+(64<<10))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) || errors.Is(err, multipart.ErrMessageTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "需求图片总大小不能超过 20 MB"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法读取需求和图片"})
		}
		return nil, false
	}
	fields := r.MultipartForm.Value["issue"]
	if len(fields) != 1 || len(fields[0]) > 2<<20 || json.Unmarshal([]byte(fields[0]), req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无法读取需求内容"})
		return nil, false
	}
	return r.MultipartForm.File["images"], true
}

func readIssueImages(c *gin.Context, files []*multipart.FileHeader) ([]repo.IssueImage, bool) {
	if len(files) > issueImageCountLimit {
		c.JSON(http.StatusBadRequest, gin.H{"error": "每条需求最多添加 8 张图片"})
		return nil, false
	}
	images := make([]repo.IssueImage, 0, len(files))
	var total int64
	for _, header := range files {
		total += header.Size
		if header.Size > issueImageLimit || total > issueImageTotalLimit {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "单张图片最多 10 MB，合计最多 20 MB"})
			return nil, false
		}
		file, err := header.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法打开需求图片"})
			return nil, false
		}
		data, err := io.ReadAll(io.LimitReader(file, issueImageLimit+1))
		_ = file.Close()
		if err != nil || len(data) > issueImageLimit {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法读取需求图片"})
			return nil, false
		}
		mediaType := http.DetectContentType(data)
		switch mediaType {
		case "image/png", "image/jpeg", "image/webp", "image/gif":
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "图片格式须为 PNG、JPEG、WebP 或 GIF"})
			return nil, false
		}
		name := []rune(strings.TrimSpace(header.Filename))
		images = append(images, repo.IssueImage{ID: rand.Text(), Name: string(name[:min(len(name), 255)]), MediaType: mediaType, Data: data})
	}
	return images, true
}

func (h issuesHandlers) issueImage(c *gin.Context, issue domain.Issue) {
	image, err := h.repo.IssueImage(issue.ID, c.Param("imageId"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到需求图片"})
		return
	}
	if err != nil {
		respond(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, image.MediaType, image.Data)
}

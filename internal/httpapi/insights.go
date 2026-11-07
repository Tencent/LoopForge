package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type insightsHandlers struct{ *deps }

const insightIngestPath = "/api/insights/v1/ingest"

func (h *insightsHandlers) manageProject(c *gin.Context, projectID string) bool {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return false
	}
	project, ok := h.repo.Project(projectID)
	if !ok {
		c.JSON(404, gin.H{"error": "项目不存在"})
		return false
	}
	if h.isPlatformAdmin(c.Request) || (user.ID > 0 && project.OwnerID == user.ID) {
		return true
	}
	for _, member := range project.Members {
		if member.UserID == user.ID && member.Role == "maintainer" {
			return true
		}
	}
	c.JSON(403, gin.H{"error": "只有项目负责人、维护者或平台管理员可以管理上报数据源"})
	return false
}
func (h *insightsHandlers) sources(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		if !h.manageProject(c, c.Query("projectId")) {
			return
		}
		sources, err := h.repo.InsightSources(c.Query("projectId"))
		if err != nil {
			respond(c, err)
			return
		}
		c.JSON(200, sources)
		return
	}
	var input struct {
		ProjectID     string           `json:"projectId"`
		Name          string           `json:"name"`
		UserMappings  map[string]int64 `json:"userMappings"`
		ExpiresInDays int              `json:"expiresInDays"`
	}
	if !decodeInsightJSON(c, &input) {
		return
	}
	if !h.manageProject(c, input.ProjectID) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 256 || len(input.UserMappings) > 500 {
		c.JSON(400, gin.H{"error": "名称须为 1–256 字节，用户映射最多 500 项"})
		return
	}
	project, _ := h.repo.Project(input.ProjectID)
	for externalID, uid := range input.UserMappings {
		member := uid > 0 && project.OwnerID == uid
		for _, m := range project.Members {
			if m.UserID == uid {
				member = true
			}
		}
		_, exists := h.repo.User(uid)
		if strings.TrimSpace(externalID) == "" || len(externalID) > 128 || !exists || !member {
			c.JSON(400, gin.H{"error": "外部用户须映射到本项目的现有成员或负责人"})
			return
		}
	}
	days := input.ExpiresInDays
	if days == 0 {
		days = 90
	}
	if days < 1 || days > 365 {
		c.JSON(400, gin.H{"error": "有效期须为 1–365 天"})
		return
	}
	token, err := newAPIToken()
	if err != nil {
		respond(c, err)
		return
	}
	token = "afi_" + strings.TrimPrefix(token, "afp_")
	now := time.Now().UTC()
	source := domain.InsightSource{ID: "src-" + randomID(), ProjectID: input.ProjectID, Name: input.Name, UserMappings: input.UserMappings, TokenPrefix: tokenPrefix(token), CreatedAt: now, ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour)}
	if err := h.repo.CreateInsightSource(source, insightTokenHash(token)); err != nil {
		respond(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(201, gin.H{"source": source, "token": token})
}
func (h *insightsHandlers) revokeSource(c *gin.Context) {
	source, err := h.repo.InsightSource(c.Param("id"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(404, gin.H{"error": "数据源不存在"})
		return
	}
	if err != nil {
		respond(c, err)
		return
	}
	if !h.manageProject(c, source.ProjectID) {
		return
	}
	if err := h.repo.RevokeInsightSource(source.ID, time.Now().UTC()); err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func insightTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func decodeInsightJSON(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(dst)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = errors.New("仅允许一个 JSON 对象")
		}
	}
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			c.JSON(413, gin.H{"error": "请求不能超过 1 MiB"})
		} else {
			c.JSON(400, gin.H{"error": "无效的上报 JSON: " + err.Error()})
		}
		return false
	}
	return true
}
func (h *insightsHandlers) ingest(c *gin.Context) {
	token, ok := bearerToken(c.Request)
	if !ok {
		c.JSON(401, gin.H{"error": "需要数据源 Bearer Token"})
		return
	}
	source, err := h.repo.ActiveInsightSource(insightTokenHash(token), time.Now().UTC())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(401, gin.H{"error": "数据源凭据无效、已过期或撤销"})
		return
	}
	if err != nil {
		respond(c, err)
		return
	}
	if _, ok := h.repo.Project(source.ProjectID); !ok {
		c.JSON(403, gin.H{"error": "数据源项目不存在"})
		return
	}
	var input domain.InsightExecution
	if !decodeInsightJSON(c, &input) {
		return
	}
	if reason := validateInsightExecution(&input, time.Now().UTC()); reason != "" {
		c.JSON(400, gin.H{"error": reason})
		return
	}
	if input.ExternalUserID != "" && source.UserMappings[input.ExternalUserID] <= 0 {
		c.JSON(400, gin.H{"error": "externalUserId 未配置用户映射；无责任人时请省略"})
		return
	}
	duplicate, err := h.repo.IngestInsightExecution(source, input)
	if errors.Is(err, repo.ErrInsightConflict) {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(401, gin.H{"error": "数据源凭据已失效"})
		return
	}
	if err != nil {
		respond(c, err)
		return
	}
	status := "accepted"
	code := 201
	if duplicate {
		status = "duplicate"
		code = 200
	}
	c.JSON(code, gin.H{"id": input.ID, "sourceId": source.ID, "status": status})
}
func validateInsightExecution(v *domain.InsightExecution, now time.Time) string {
	if v.Type != "workflow_execution" {
		return "type 必须为 workflow_execution"
	}
	for _, value := range []string{v.ID, v.ExternalExecutionID, v.Workflow.ID, v.Workflow.Name} {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return "记录 ID、执行 ID、工作流 ID 与名称必填且不超过 256 字节"
		}
	}
	if len(v.ExternalUserID) > 128 {
		return "externalUserId 不能超过 128 字节"
	}
	if v.Status != "succeeded" && v.Status != "failed" && v.Status != "cancelled" {
		return "只接受 succeeded、failed、cancelled 终态执行汇总"
	}
	if v.StartedAt.Year() < 2000 || v.FinishedAt.Before(v.StartedAt) || v.FinishedAt.After(now.Add(5*time.Minute)) {
		return "开始/结束时间无效，结束时间不能早于开始或晚于当前时间 5 分钟"
	}
	v.StartedAt = v.StartedAt.UTC().Truncate(time.Millisecond)
	v.FinishedAt = v.FinishedAt.UTC().Truncate(time.Millisecond)
	if v.ExecutionDurationMs != nil && (*v.ExecutionDurationMs < 0 || *v.ExecutionDurationMs > 315360000000) {
		return "executionDurationMs 超出范围"
	}
	if v.URL != "" {
		u, err := url.Parse(v.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || len(v.URL) > 2048 {
			return "url 必须为无凭据的 HTTP(S) 地址"
		}
	}
	if len(v.Usage) == 0 || len(v.Usage) > 64 {
		return "usage 须包含 1–64 项模型用量"
	}
	seen := map[string]bool{}
	for _, u := range v.Usage {
		if strings.TrimSpace(u.Provider) == "" || strings.TrimSpace(u.Model) == "" || len(u.Provider) > 128 || len(u.Model) > 128 {
			return "每项 usage 须提供 provider 和 model（不超过 128 字节）"
		}
		key := u.Provider + "\x00" + u.Model
		if seen[key] {
			return "同一 provider/model 请合并为一项"
		}
		seen[key] = true
		for _, n := range []int64{u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.ReasoningTokens} {
			if n < 0 || n > 1e12 {
				return "Token 数须在 0–10^12 之间"
			}
		}
		if u.CacheReadTokens+u.CacheWriteTokens > u.InputTokens || u.ReasoningTokens > u.OutputTokens {
			return "缓存 Token 必须包含在输入中，推理 Token 必须包含在输出中"
		}
		switch u.CostStatus {
		case "unknown":
			if u.CostUSD != nil {
				return "未知成本必须为 null"
			}
		case "reported", "estimated":
			if u.CostUSD == nil || math.IsNaN(*u.CostUSD) || math.IsInf(*u.CostUSD, 0) || *u.CostUSD < 0 || *u.CostUSD > 1e9 {
				return "已知成本须为 0–10^9 USD"
			}
		default:
			return "costStatus 必须为 reported、estimated 或 unknown"
		}
	}
	return ""
}

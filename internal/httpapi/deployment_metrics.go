package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"

	"github.com/gin-gonic/gin"
)

const (
	deploymentMetricsMaxPageSize = 100
	deploymentMetricsTimeout     = 45 * time.Second
	skillHubPipelineProjectID    = 10001
	skillHubDeploymentPipelineID = 10000001
	skillHubGongfengProjectID    = "example/team-project"
)

type deploymentMetricsHandlers struct{ *deps }

type deploymentMetricRecord struct {
	ID          string `json:"id"`
	RunNumber   int64  `json:"runNumber,omitempty"`
	Status      int    `json:"status"`
	StatusLabel string `json:"statusLabel"`
	Branch      string `json:"branch"`
	Revision    string `json:"revision,omitempty"`
	Starter     string `json:"starter,omitempty"`
	TriggerType string `json:"triggerType,omitempty"`
	Comment     string `json:"comment,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	DurationMs  int64  `json:"durationMs"`
	URL         string `json:"url,omitempty"`
}

type deploymentMetricSummary struct {
	Total               int     `json:"total"`
	Succeeded           int     `json:"succeeded"`
	Failed              int     `json:"failed"`
	Running             int     `json:"running"`
	SuccessRate         float64 `json:"successRate"`
	Deployments7d       int     `json:"deployments7d"`
	Average7dDurationMs int64   `json:"average7dDurationMs"`
	AverageDurationMs   int64   `json:"averageDurationMs"`
	P50DurationMs       int64   `json:"p50DurationMs"`
	P90DurationMs       int64   `json:"p90DurationMs"`
	LatestDurationMs    int64   `json:"latestDurationMs"`
}

type deploymentMetricsResponse struct {
	ProjectID       string                   `json:"projectId"`
	RepositoryID    string                   `json:"repositoryId"`
	RepositoryName  string                   `json:"repositoryName"`
	Branch          string                   `json:"branch"`
	PipelineProject int                      `json:"pipelineProjectId"`
	PipelineID      int                      `json:"pipelineId"`
	PipelineURL     string                   `json:"pipelineUrl,omitempty"`
	QueriedAt       string                   `json:"queriedAt"`
	Summary         deploymentMetricSummary  `json:"summary"`
	Records         []deploymentMetricRecord `json:"records"`
}

func (h *deploymentMetricsHandlers) deploymentMetrics(c *gin.Context) {
	current, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录后查询部署耗时"})
		return
	}
	storedUser, ok := h.repo.User(current.ID)
	if !ok {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "当前用户尚未完成平台注册"})
		return
	}
	projectID := strings.TrimSpace(c.Query("projectId"))
	repositoryID := strings.TrimSpace(c.Query("repositoryId"))
	branch := strings.TrimSpace(c.Query("branch"))
	if projectID == "" || repositoryID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "projectId 和 repositoryId 必填"})
		return
	}
	if len(branch) > 255 || strings.ContainsAny(branch, "\r\n\x00") {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "分支名格式不合法"})
		return
	}
	if _, ok := h.repo.Project(projectID); !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "项目不存在"})
		return
	}
	repository, ok := h.repo.Repository(repositoryID)
	if !ok || repository.ProjectID != projectID {
		c.JSON(http.StatusNotFound, map[string]string{"error": "当前项目中不存在该仓库"})
		return
	}
	if !isSkillHubRepository(repository) {
		c.JSON(http.StatusUnprocessableEntity, map[string]string{"error": "当前看板仅支持 SkillHub 仓库"})
		return
	}
	pipelineProject := skillHubPipelineProjectID
	pipelineID := skillHubDeploymentPipelineID
	pageSize := positiveQueryInt(c, "pageSize", 50)
	if pageSize > deploymentMetricsMaxPageSize {
		pageSize = deploymentMetricsMaxPageSize
	}

	server, ok := h.lookupQCIProjectMCP(projectID, current.ID)
	if !ok {
		c.JSON(http.StatusPreconditionFailed, map[string]string{"error": "当前项目尚未添加“智研QCI流水线” MCP，请先在 MCP 管理中添加"})
		return
	}
	bound, err := bindAssistantMCP(server, storedUser)
	if err != nil {
		c.JSON(http.StatusPreconditionFailed, map[string]string{"error": err.Error()})
		return
	}
	if bound.Headers == nil {
		bound.Headers = map[string]string{}
	}
	bound.Headers["X-Username"] = current.LoginName
	query := url.Values{}
	query.Set("page_size", strconv.Itoa(pageSize))
	query.Set("ordering", "-created")
	if branch != "" {
		query.Set("cur_branch", branch)
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), deploymentMetricsTimeout)
	defer cancel()
	client := mcpclient.New(bound.URL, bound.Headers).WithTimeout(deploymentMetricsTimeout)
	records := make([]deploymentMetricRecord, 0, pageSize)
	for page := 1; page <= 10; page++ {
		query.Set("page", strconv.Itoa(page))
		path := fmt.Sprintf("/pipeline/%d/totalresult?%s", pipelineID, query.Encode())
		raw, err := client.CallToolContext(ctx, "coding_mcp", map[string]any{
			"method": http.MethodGet, "path": path,
			"project_id": strconv.Itoa(pipelineProject), "username": current.LoginName,
		})
		if err != nil {
			c.JSON(http.StatusBadGateway, map[string]string{"error": "查询智研流水线失败：" + err.Error()})
			return
		}
		batch, err := normalizeDeploymentRecords(raw, branch, repository, pipelineProject, pipelineID)
		if err != nil {
			c.JSON(http.StatusBadGateway, map[string]string{"error": "智研流水线返回的数据无法解析：" + err.Error()})
			return
		}
		records = append(records, batch...)
		if len(batch) < pageSize {
			break
		}
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].StartedAt > records[j].StartedAt })
	response := deploymentMetricsResponse{
		ProjectID: projectID, RepositoryID: repository.ID, RepositoryName: repository.Name,
		Branch: branch, PipelineProject: pipelineProject, PipelineID: pipelineID,
		PipelineURL: deploymentPipelineURL(repository, pipelineProject, pipelineID),
		QueriedAt:   time.Now().UTC().Format(time.RFC3339), Records: records,
	}
	response.Summary = summarizeDeploymentMetricsAt(records, time.Now().UTC())
	c.JSON(http.StatusOK, response)
}

func positiveQueryInt(c *gin.Context, name string, fallback int) int {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func (h *deploymentMetricsHandlers) lookupQCIProjectMCP(projectID string, userID int64) (domain.MCPServerDefinition, bool) {
	for _, item := range assistantOutboundMCPServers(h.repo.MCPServers(), projectID, userID) {
		identity := strings.ToLower(strings.Join([]string{item.ID, item.Name, item.MarketID, item.URL}, " "))
		if item.MarketID == "7897" || strings.Contains(identity, "zhiyan/qci") || strings.Contains(identity, "zhiyan-pipeline") || strings.Contains(identity, "智研qci") {
			return item, true
		}
	}
	// The SkillHub dashboard has one fixed, read-only QCI integration. Reuse the
	// official market definition even when the project has not explicitly added
	// it yet; personal Taihu credentials are still required and bound per call.
	if item, ok := marketItem("zhiyan-pipeline"); ok {
		return domain.MCPServerDefinition{
			ID: "skillhub-deployment-qci", Name: item.Name, ProjectID: projectID,
			MarketID: item.ID, Scope: "project", Transport: item.Transport,
			URL: item.URL, AuthType: item.AuthType, TimeoutSec: 60,
		}, true
	}
	return domain.MCPServerDefinition{}, false
}

func isSkillHubRepository(repository domain.Repository) bool {
	project := strings.Trim(strings.ToLower(strings.TrimSpace(repository.GongfengProjectID)), "/")
	if project == skillHubGongfengProjectID {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(repository.Name), "skillhub") {
		return strings.Contains(strings.ToLower(repository.WebURL+" "+repository.CloneURL), "example/team-project")
	}
	return false
}

func deploymentPipelineURL(repository domain.Repository, projectID, pipelineID int) string {
	if pattern := strings.TrimSpace(repository.PipelineURLPattern); pattern != "" {
		return strings.ReplaceAll(pattern, "{build_id}", "current")
	}
	return fmt.Sprintf("https://zhiyan.example.com/qci/%d/pipeline/#/pipeline/detail/%d/build/current", projectID, pipelineID)
}

func deploymentBuildURL(repository domain.Repository, projectID, pipelineID int, buildID string) string {
	if pattern := strings.TrimSpace(repository.PipelineURLPattern); pattern != "" && strings.Contains(pattern, "{build_id}") {
		return strings.ReplaceAll(pattern, "{build_id}", url.PathEscape(buildID))
	}
	return fmt.Sprintf("https://zhiyan.example.com/qci/%d/pipeline/#/pipeline/detail/%d/build/%s", projectID, pipelineID, url.PathEscape(buildID))
}

func normalizeDeploymentRecords(raw, branch string, repository domain.Repository, pipelineProject, pipelineID int) ([]deploymentMetricRecord, error) {
	rows := deploymentReadableRows(raw)
	if rows == nil {
		payload, err := decodeMCPJSON(raw)
		if err != nil {
			return nil, err
		}
		rows = deploymentRows(payload)
		if rows == nil {
			return nil, fmt.Errorf("未找到构建记录列表")
		}
	}
	records := make([]deploymentMetricRecord, 0, len(rows))
	for _, row := range rows {
		recordBranch := stringField(row, "cur_branch", "curBranch", "branch", "source_branch")
		if branch != "" && recordBranch != "" && recordBranch != branch {
			continue
		}
		started := timeField(row, "start_time", "startTime", "started_at", "startedAt", "created", "created_at")
		finished := timeField(row, "finish_time", "finishTime", "finished_at", "finishedAt", "updated", "updated_at")
		durationMs := durationField(row, started, finished)
		status := deploymentStatus(row)
		id := stringField(row, "id", "totalresult_id", "totalResultId", "build_id", "buildId")
		record := deploymentMetricRecord{
			ID: id, RunNumber: int64(intField(row, "run_num", "runNumber")), Status: status,
			StatusLabel: coalesce(stringField(row, "status_label", "statusLabel"), deploymentStatusLabel(status)), Branch: coalesce(recordBranch, branch),
			Revision:    stringField(row, "cur_revision", "curRevision", "revision", "commit_id"),
			Starter:     stringField(row, "starter", "start_user", "creator"),
			TriggerType: stringField(row, "trigger_type", "triggerType"), Comment: stringField(row, "comment", "remark"),
			DurationMs: durationMs,
		}
		if !started.IsZero() {
			record.StartedAt = started.UTC().Format(time.RFC3339)
		}
		if !finished.IsZero() {
			record.FinishedAt = finished.UTC().Format(time.RFC3339)
		}
		if id != "" {
			record.URL = deploymentBuildURL(repository, pipelineProject, pipelineID, id)
		} else {
			record.URL = deploymentPipelineURL(repository, pipelineProject, pipelineID)
		}
		records = append(records, record)
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].StartedAt > records[j].StartedAt })
	return records, nil
}

func deploymentReadableRows(text string) []map[string]any {
	fieldNames := map[string]string{
		"构建结果ID": "id", "运行序列号": "run_num", "构建状态": "status",
		"触发方式": "trigger_type", "启动人": "starter", "触发分支": "cur_branch",
		"Commit ID": "cur_revision", "构建备注": "comment", "构建开始时间": "start_time",
		"构建结束时间": "finish_time", "构建耗时": "duration",
	}
	var rows []map[string]any
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		dot := strings.Index(line, ". ")
		if dot <= 0 {
			continue
		}
		if _, err := strconv.Atoi(line[:dot]); err != nil {
			continue
		}
		row := map[string]any{}
		for _, field := range strings.Split(line[dot+2:], " | ") {
			label, value, ok := strings.Cut(field, ": ")
			if !ok {
				continue
			}
			if key := fieldNames[strings.TrimSpace(label)]; key != "" {
				row[key] = strings.TrimSpace(value)
			}
		}
		if strings.TrimSpace(fmt.Sprint(row["id"])) != "" {
			rows = append(rows, row)
		}
	}
	return rows
}

func decodeMCPJSON(raw string) (any, error) {
	text := strings.TrimSpace(raw)
	if strings.HasPrefix(text, "```") {
		if nl := strings.IndexByte(text, '\n'); nl >= 0 {
			text = text[nl+1:]
		}
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "```"))
	}
	starts := []int{}
	if i := strings.IndexByte(text, '{'); i >= 0 {
		starts = append(starts, i)
	}
	if i := strings.IndexByte(text, '['); i >= 0 {
		starts = append(starts, i)
	}
	if len(starts) == 0 {
		return nil, fmt.Errorf("返回内容不是 JSON")
	}
	sort.Ints(starts)
	for _, start := range starts {
		var payload any
		decoder := json.NewDecoder(strings.NewReader(text[start:]))
		decoder.UseNumber()
		if decoder.Decode(&payload) == nil {
			return payload, nil
		}
	}
	return nil, fmt.Errorf("JSON 格式不完整")
}

func deploymentRows(payload any) []map[string]any {
	switch value := payload.(type) {
	case string:
		decoded, err := decodeMCPJSON(value)
		if err == nil {
			return deploymentRows(decoded)
		}
	case []any:
		rows := make([]map[string]any, 0, len(value))
		for _, item := range value {
			if row, ok := item.(map[string]any); ok {
				rows = append(rows, row)
			}
		}
		return rows
	case map[string]any:
		for _, key := range []string{"results", "items", "list", "records", "data", "result"} {
			if nested, ok := value[key]; ok {
				if rows := deploymentRows(nested); rows != nil {
					return rows
				}
			}
		}
	}
	return nil
}

func mapField(row map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := row[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

func stringField(row map[string]any, keys ...string) string {
	value := mapField(row, keys...)
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func intField(row map[string]any, keys ...string) int {
	value := mapField(row, keys...)
	switch n := value.(type) {
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case float64:
		return int(n)
	case int:
		return n
	}
	i, _ := strconv.Atoi(strings.TrimSpace(fmt.Sprint(value)))
	return i
}

func timeField(row map[string]any, keys ...string) time.Time {
	value := strings.TrimSpace(stringField(row, keys...))
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04:05.999999"} {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func deploymentStatus(row map[string]any) int {
	if status := intField(row, "status"); status != 0 {
		return status
	}
	label := strings.ToLower(stringField(row, "status_label", "statusLabel", "status"))
	switch {
	case strings.Contains(label, "成功"), strings.Contains(label, "通过"), label == "succeeded", label == "success", label == "passed":
		return 2
	case strings.Contains(label, "测试失败"):
		return 13
	case strings.Contains(label, "失败"), label == "failed":
		return 3
	case strings.Contains(label, "超时"), label == "timeout":
		return 6
	case strings.Contains(label, "中止"), strings.Contains(label, "取消"), label == "cancelled", label == "aborted":
		return 4
	case strings.Contains(label, "运行"), strings.Contains(label, "执行中"), label == "running":
		return 1
	case strings.Contains(label, "阻塞"), label == "blocked":
		return 7
	case strings.Contains(label, "就绪"), label == "ready":
		return 8
	}
	return 0
}

func durationField(row map[string]any, started, finished time.Time) int64 {
	if !started.IsZero() && !finished.IsZero() && finished.After(started) {
		return finished.Sub(started).Milliseconds()
	}
	if value := mapField(row, "duration_ms", "durationMs"); value != nil {
		return numericMilliseconds(value, 1)
	}
	value := mapField(row, "duration", "elapsed", "elapsed_seconds")
	if value == nil {
		return 0
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if parsed, err := time.ParseDuration(text); err == nil {
			return parsed.Milliseconds()
		}
		parts := strings.Split(text, ":")
		if len(parts) == 3 {
			hours, e1 := strconv.ParseFloat(parts[0], 64)
			minutes, e2 := strconv.ParseFloat(parts[1], 64)
			seconds, e3 := strconv.ParseFloat(parts[2], 64)
			if e1 == nil && e2 == nil && e3 == nil {
				return int64((hours*3600 + minutes*60 + seconds) * 1000)
			}
		}
	}
	return numericMilliseconds(value, 1000)
}

func numericMilliseconds(value any, multiplier float64) int64 {
	raw := strings.TrimSpace(fmt.Sprint(value))
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return int64(math.Round(n * multiplier))
}

func deploymentStatusLabel(status int) string {
	switch status {
	case 0:
		return "未执行"
	case 1:
		return "执行中"
	case 2:
		return "成功"
	case 3:
		return "失败"
	case 4:
		return "已中止"
	case 5:
		return "下发中"
	case 6:
		return "超时"
	case 7:
		return "阻塞"
	case 8:
		return "就绪"
	case 9:
		return "中止中"
	case 10:
		return "已忽略"
	case 13:
		return "测试失败"
	default:
		return "未知"
	}
}

func summarizeDeploymentMetrics(records []deploymentMetricRecord) deploymentMetricSummary {
	return summarizeDeploymentMetricsAt(records, time.Now().UTC())
}

func summarizeDeploymentMetricsAt(records []deploymentMetricRecord, now time.Time) deploymentMetricSummary {
	summary := deploymentMetricSummary{Total: len(records)}
	durations := make([]int64, 0, len(records))
	durations7d := make([]int64, 0, len(records))
	cutoff7d := now.AddDate(0, 0, -7)
	for _, record := range records {
		switch record.Status {
		case 2:
			summary.Succeeded++
		case 1, 5, 7, 8, 9:
			summary.Running++
		case 3, 4, 6, 13:
			summary.Failed++
		}
		if record.DurationMs > 0 && record.Status != 1 && record.Status != 5 && record.Status != 9 {
			durations = append(durations, record.DurationMs)
			if started, err := time.Parse(time.RFC3339, record.StartedAt); err == nil && !started.Before(cutoff7d) && !started.After(now) {
				durations7d = append(durations7d, record.DurationMs)
			}
		}
	}
	finished := summary.Succeeded + summary.Failed
	if finished > 0 {
		summary.SuccessRate = math.Round(float64(summary.Succeeded)*1000/float64(finished)) / 10
	}
	summary.Deployments7d = len(durations7d)
	if len(durations7d) > 0 {
		var total7d int64
		for _, value := range durations7d {
			total7d += value
		}
		summary.Average7dDurationMs = total7d / int64(len(durations7d))
	}
	if len(durations) == 0 {
		return summary
	}
	var total int64
	for _, value := range durations {
		total += value
	}
	summary.AverageDurationMs = total / int64(len(durations))
	sorted := append([]int64(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	summary.P50DurationMs = percentileDuration(sorted, 0.50)
	summary.P90DurationMs = percentileDuration(sorted, 0.90)
	for _, record := range records {
		if record.DurationMs > 0 {
			summary.LatestDurationMs = record.DurationMs
			break
		}
	}
	return summary
}

func percentileDuration(sorted []int64, percentile float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(math.Ceil(percentile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func coalesce(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

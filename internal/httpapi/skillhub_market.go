package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

const (
	skillhubDefaultPageSize = 24
	skillhubMaxPageSize     = 50
	skillhubMaxDescription  = 500
	// skillhubZipLimit bounds the downloaded archive; unpacked content is
	// still checked against the per-file and total caps.
	skillhubZipLimit = domain.SkillTotalMaxBytes + 1<<20
)

var (
	skillhubSlugPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	skillhubVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,63}$`)
	skillhubSkillIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	skillhubSortFields     = map[string]bool{"downloads": true, "score": true, "updated_at": true}
	skillhubReportTones    = map[string]bool{"benign": true}
)

// 路由: /api/skillhub/*（skillhub_market.go）
type skillhubHandlers struct {
	*deps
	client *skillhubClient
	// writeMu serialises the check-then-write phase of install/update so two
	// concurrent installs of one slug cannot both succeed.
	writeMu sync.Mutex
}

func (h *skillhubHandlers) userKey(c *gin.Context) string {
	user, ok := h.requestUser(c.Request, true)
	if !ok || user == nil {
		return ""
	}
	return skillhubUserKey(user.ID, user.LoginName)
}

func skillhubFail(c *gin.Context, err error) {
	var se *skillhubError
	if errors.As(err, &se) {
		c.JSON(se.Status, map[string]string{"error": se.Message})
		return
	}
	respond(c, err)
}

func skillhubHomepage(namespace, slug string) string {
	if namespace == "" {
		return skillhubSiteURL + "/skills/" + url.PathEscape(slug)
	}
	return skillhubSiteURL + "/skills/" + url.PathEscape(namespace) + "/" + url.PathEscape(slug)
}

func skillhubLabels(raw map[string]any) map[string]string {
	out := map[string]string{}
	for key, value := range raw {
		switch v := value.(type) {
		case string:
			out[key] = v
		case bool:
			out[key] = strconv.FormatBool(v)
		case float64:
			out[key] = strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return out
}

// skillhubTarget reads the :slug path parameter and the optional namespace
// query parameter; slugs are only unique per publisher namespace.
func skillhubTarget(c *gin.Context) (slug, namespace string, ok bool) {
	slug = strings.TrimSpace(c.Param("slug"))
	namespace = strings.TrimSpace(c.Query("namespace"))
	if !skillhubSlugPattern.MatchString(slug) || (namespace != "" && !skillhubSlugPattern.MatchString(namespace)) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "无效的 SkillHub 技能标识"})
		return "", "", false
	}
	return slug, namespace, true
}

func skillhubVersionParam(c *gin.Context, raw string) (string, bool) {
	version := strings.TrimSpace(raw)
	if version != "" && !skillhubVersionPattern.MatchString(version) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "无效的版本号"})
		return "", false
	}
	return version, true
}

// --- read-only proxy ---

type skillhubListItem struct {
	Slug        string            `json:"slug"`
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	IconURL     string            `json:"iconUrl,omitempty"`
	Category    string            `json:"category"`
	Version     string            `json:"version"`
	OwnerName   string            `json:"ownerName"`
	Homepage    string            `json:"homepage"`
	Downloads   int64             `json:"downloads"`
	Stars       int64             `json:"stars"`
	Labels      map[string]string `json:"labels"`
	Verified    bool              `json:"verified,omitempty"`
	UpdatedAt   int64             `json:"updatedAt,omitempty"`
}

// skillhubListQuery whitelists the list parameters forwarded upstream.
func skillhubListQuery(c *gin.Context) (url.Values, int, int) {
	q := url.Values{}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		if r := []rune(kw); len(r) > 100 {
			kw = string(r[:100])
		}
		q.Set("keyword", kw)
	}
	if cat := strings.TrimSpace(c.Query("category")); cat != "" && skillhubSlugPattern.MatchString(cat) {
		q.Set("category", cat)
	}
	sortBy := strings.TrimSpace(c.Query("sortBy"))
	if !skillhubSortFields[sortBy] {
		sortBy = "downloads"
	}
	q.Set("sortBy", sortBy)
	q.Set("order", "desc")
	page, err := strconv.Atoi(c.Query("page"))
	if err != nil || page < 1 {
		page = 1
	}
	size, err := strconv.Atoi(c.Query("pageSize"))
	if err != nil || size < 1 {
		size = skillhubDefaultPageSize
	}
	if size > skillhubMaxPageSize {
		size = skillhubMaxPageSize
	}
	q.Set("page", strconv.Itoa(page))
	q.Set("pageSize", strconv.Itoa(size))
	return q, page, size
}

func (h *skillhubHandlers) list(c *gin.Context) {
	query, page, size := skillhubListQuery(c)
	remote, err := h.client.list(c.Request.Context(), query, h.userKey(c))
	if err != nil {
		skillhubFail(c, err)
		return
	}
	items := make([]skillhubListItem, 0, len(remote.Data.Skills))
	for _, s := range remote.Data.Skills {
		items = append(items, skillhubListItem{
			Slug: s.Slug, Namespace: s.Namespace.Handle, Name: strings.TrimSpace(firstNonEmpty(s.Name, s.Slug)),
			Description: strings.TrimSpace(firstNonEmpty(s.DescriptionZh, s.Description)), IconURL: s.IconURL,
			Category: s.Category, Version: s.Version,
			OwnerName: strings.TrimSpace(firstNonEmpty(s.Namespace.DisplayName, s.OwnerName, s.Namespace.Handle)),
			Homepage:  skillhubHomepage(s.Namespace.Handle, s.Slug), Downloads: s.Downloads, Stars: s.Stars,
			Labels: skillhubLabels(s.Labels), Verified: s.Verified, UpdatedAt: s.UpdatedAt,
		})
	}
	c.JSON(http.StatusOK, map[string]any{"total": remote.Data.Total, "page": page, "pageSize": size, "items": items})
}

func (h *skillhubHandlers) categories(c *gin.Context) {
	items, err := h.client.categoryList(c.Request.Context(), h.userKey(c))
	if err != nil {
		skillhubFail(c, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"items": items})
}

type skillhubSecurityReport struct {
	Status     string `json:"status"`
	StatusText string `json:"statusText"`
	ReportURL  string `json:"reportUrl,omitempty"`
	Safe       bool   `json:"safe"`
}

// skillhubSecurityReports normalises the undocumented securityReports field;
// anything unexpected is dropped rather than failing the response.
func skillhubSecurityReports(raw json.RawMessage) map[string]skillhubSecurityReport {
	if len(raw) == 0 {
		return nil
	}
	var in map[string]struct {
		Status     string `json:"status"`
		StatusText string `json:"statusText"`
		ReportURL  string `json:"reportUrl"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || len(in) == 0 {
		return nil
	}
	out := make(map[string]skillhubSecurityReport, len(in))
	for vendor, r := range in {
		status := strings.TrimSpace(r.Status)
		report := skillhubSecurityReport{Status: status, StatusText: strings.TrimSpace(firstNonEmpty(r.StatusText, status)), Safe: skillhubReportTones[status]}
		if u, err := url.Parse(strings.TrimSpace(r.ReportURL)); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			report.ReportURL = u.String()
		}
		out[vendor] = report
	}
	return out
}

type skillhubVersionView struct {
	Version         string                            `json:"version"`
	Changelog       string                            `json:"changelog"`
	CreatedAt       int64                             `json:"createdAt"`
	SecurityReports map[string]skillhubSecurityReport `json:"securityReports,omitempty"`
}

func skillhubVersionOut(v skillhubRemoteVersion) skillhubVersionView {
	return skillhubVersionView{Version: v.Version, Changelog: v.Changelog, CreatedAt: v.CreatedAt, SecurityReports: skillhubSecurityReports(v.SecurityReports)}
}

func (h *skillhubHandlers) detail(c *gin.Context) {
	slug, namespace, ok := skillhubTarget(c)
	if !ok {
		return
	}
	d, err := h.client.detail(c.Request.Context(), slug, namespace, h.userKey(c))
	if err != nil {
		skillhubFail(c, err)
		return
	}
	ns := strings.TrimSpace(firstNonEmpty(d.Namespace.Handle, namespace, d.Owner.Handle))
	var latest *skillhubVersionView
	if d.LatestVersion != nil && d.LatestVersion.Version != "" {
		v := skillhubVersionOut(*d.LatestVersion)
		latest = &v
	}
	s := d.Skill
	c.JSON(http.StatusOK, map[string]any{
		"skill": map[string]any{
			"slug": firstNonEmpty(s.Slug, slug), "namespace": ns, "name": strings.TrimSpace(firstNonEmpty(s.DisplayName, s.Slug, slug)),
			"summary": strings.TrimSpace(firstNonEmpty(s.SummaryZh, s.Summary)), "iconUrl": s.IconURL, "category": s.Category,
			"subCategories": s.SubCategories, "labels": skillhubLabels(s.Labels), "createdAt": s.CreatedAt,
			"downloads": s.Stats.Downloads, "stars": s.Stats.Stars, "installs": s.Stats.Installs, "versionCount": s.Stats.Versions,
			"homepage": skillhubHomepage(ns, firstNonEmpty(s.Slug, slug)),
		},
		"latestVersion":   latest,
		"owner":           map[string]string{"displayName": strings.TrimSpace(firstNonEmpty(d.Owner.DisplayName, d.Namespace.DisplayName, ns)), "handle": d.Owner.Handle},
		"securityReports": skillhubSecurityReports(d.SecurityReports),
	})
}

func (h *skillhubHandlers) versions(c *gin.Context) {
	slug, namespace, ok := skillhubTarget(c)
	if !ok {
		return
	}
	remote, err := h.client.versions(c.Request.Context(), slug, namespace, h.userKey(c))
	if err != nil {
		skillhubFail(c, err)
		return
	}
	out := make([]skillhubVersionView, 0, len(remote))
	for _, v := range remote {
		out = append(out, skillhubVersionOut(v))
	}
	c.JSON(http.StatusOK, map[string]any{"versions": out})
}

func (h *skillhubHandlers) files(c *gin.Context) {
	slug, namespace, ok := skillhubTarget(c)
	if !ok {
		return
	}
	version, ok := skillhubVersionParam(c, c.Query("version"))
	if !ok {
		return
	}
	files, err := h.client.files(c.Request.Context(), slug, namespace, version, h.userKey(c))
	if err != nil {
		skillhubFail(c, err)
		return
	}
	if files == nil {
		files = []skillhubFileEntry{}
	}
	c.JSON(http.StatusOK, map[string]any{"version": version, "files": files})
}

// file returns one text file inline. Oversized or binary files answer
// previewable=false instead of an error so the drawer can explain why.
func (h *skillhubHandlers) file(c *gin.Context) {
	slug, namespace, ok := skillhubTarget(c)
	if !ok {
		return
	}
	version, ok := skillhubVersionParam(c, c.Query("version"))
	if !ok {
		return
	}
	filePath := strings.TrimSpace(c.Query("path"))
	if err := domain.ValidateRelativeFilePath(filePath); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "无效的文件路径"})
		return
	}
	content, err := h.client.file(c.Request.Context(), slug, namespace, version, filePath, h.userKey(c), domain.SkillFileMaxBytes)
	var se *skillhubError
	if errors.As(err, &se) && se.Upstream == http.StatusRequestEntityTooLarge {
		c.JSON(http.StatusOK, map[string]any{"path": filePath, "previewable": false, "reason": "文件超过 1MB，不支持预览"})
		return
	}
	if err != nil {
		skillhubFail(c, err)
		return
	}
	if !utf8.Valid(content) {
		c.JSON(http.StatusOK, map[string]any{"path": filePath, "size": len(content), "previewable": false, "reason": "二进制文件，不支持预览"})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"path": filePath, "size": len(content), "previewable": true, "content": string(content)})
}

func (h *skillhubHandlers) evaluation(c *gin.Context) {
	slug, namespace, ok := skillhubTarget(c)
	if !ok {
		return
	}
	e, err := h.client.evaluation(c.Request.Context(), slug, namespace, h.userKey(c))
	if isSkillhubNotFound(err) {
		c.JSON(http.StatusOK, map[string]any{"available": false})
		return
	}
	if err != nil {
		skillhubFail(c, err)
		return
	}
	if len(e.Dimensions) == 0 || string(e.Dimensions) == "null" {
		c.JSON(http.StatusOK, map[string]any{"available": false})
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"available": true, "dimensions": e.Dimensions, "summary": e.Summary, "userSummary": e.UserSummary,
		"updatedAt": max(e.UpdatedAt, e.CreatedAt),
	})
}

// --- install / update ---

// skillhubInstalled finds the project's skill installed from slug in the
// given namespace (an empty namespace on either side matches any).
func skillhubInstalled(skills []domain.SkillDefinition, projectID, namespace, slug string) (domain.SkillDefinition, bool) {
	for _, s := range skills {
		src := s.Source
		if s.ProjectID != projectID || src == nil || src.Provider != domain.SkillSourceSkillHub || src.Slug != slug {
			continue
		}
		if namespace == "" || src.Namespace == "" || src.Namespace == namespace {
			return s, true
		}
	}
	return domain.SkillDefinition{}, false
}

// skillhubPackage downloads and unpacks one version under the platform caps.
func (h *skillhubHandlers) skillhubPackage(c *gin.Context, slug, namespace, version string) ([]repo.SkillFileInput, error) {
	raw, err := h.client.download(c.Request.Context(), slug, namespace, version, h.userKey(c), skillhubZipLimit)
	if err != nil {
		return nil, err
	}
	files, err := readSkillZipBytes(raw, slug+"-"+version+".zip", nil)
	if err != nil {
		return nil, HTTPError{Status: http.StatusUnprocessableEntity, Message: "技能包无法安装：" + err.Error()}
	}
	if len(files) > domain.SkillMaxFileCount {
		return nil, HTTPError{Status: http.StatusUnprocessableEntity, Message: fmt.Sprintf("技能包无法安装：包含 %d 个文件，超过单个技能最多 %d 个文件的限额", len(files), domain.SkillMaxFileCount)}
	}
	return files, nil
}

func truncateSkillhubText(text string) string {
	text = strings.TrimSpace(text)
	if r := []rune(text); len(r) > skillhubMaxDescription {
		return string(r[:skillhubMaxDescription]) + "…"
	}
	return text
}

func (h *skillhubHandlers) install(c *gin.Context) {
	var input struct {
		Slug      string `json:"slug"`
		Namespace string `json:"namespace"`
		Version   string `json:"version"`
		ProjectID string `json:"projectId"`
		ID        string `json:"id"`
		Name      string `json:"name"`
	}
	if !decode(c, &input) {
		return
	}
	slug, namespace := strings.TrimSpace(input.Slug), strings.TrimSpace(input.Namespace)
	if !skillhubSlugPattern.MatchString(slug) || (namespace != "" && !skillhubSlugPattern.MatchString(namespace)) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "无效的 SkillHub 技能标识"})
		return
	}
	version, ok := skillhubVersionParam(c, input.Version)
	if !ok {
		return
	}
	projectID := strings.TrimSpace(input.ProjectID)
	if projectID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "请先选择项目"})
		return
	}
	if _, exists := h.repo.Project(projectID); !exists {
		c.JSON(http.StatusNotFound, map[string]string{"error": "项目不存在"})
		return
	}
	id := strings.TrimSpace(firstNonEmpty(input.ID, slug))
	if !skillhubSkillIDPattern.MatchString(id) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "技能标识只能包含字母、数字、点、下划线和连字符，且以字母或数字开头（最长 64 位）"})
		return
	}
	if err := h.installConflict(projectID, namespace, slug, id); err != nil {
		respond(c, err)
		return
	}

	detail, err := h.client.detail(c.Request.Context(), slug, namespace, h.userKey(c))
	if err != nil {
		skillhubFail(c, err)
		return
	}
	namespace = strings.TrimSpace(firstNonEmpty(namespace, detail.Namespace.Handle, detail.Owner.Handle))
	if version == "" && detail.LatestVersion != nil {
		version = strings.TrimSpace(detail.LatestVersion.Version)
	}
	if version == "" {
		c.JSON(http.StatusBadGateway, map[string]string{"error": "SkillHub 未返回可安装的版本"})
		return
	}
	files, err := h.skillhubPackage(c, slug, namespace, version)
	if err != nil {
		skillhubFail(c, err)
		return
	}

	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = strings.TrimSpace(firstNonEmpty(detail.Skill.DisplayName, slug))
	}
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}
	now := time.Now().UTC()
	skill := domain.SkillDefinition{
		ID: id, Name: name, ProjectID: projectID,
		Description: truncateSkillhubText(firstNonEmpty(detail.Skill.SummaryZh, detail.Skill.Summary)),
		Source: &domain.SkillSource{
			Provider: domain.SkillSourceSkillHub, Namespace: namespace, Slug: slug, Version: version,
			Homepage: skillhubHomepage(namespace, slug), InstalledAt: now, UpdatedAt: now,
		},
	}

	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	if err := h.installConflict(projectID, namespace, slug, id); err != nil {
		respond(c, err)
		return
	}
	if err := h.repo.PutSkillSnapshot(skill, files, true); err != nil {
		if errors.Is(err, repo.ErrSkillExists) {
			c.JSON(http.StatusConflict, map[string]string{"error": fmt.Sprintf("技能标识「%s」已被占用，请换一个", id)})
			return
		}
		respond(c, err)
		return
	}
	saved, _ := h.repo.Skill(id)
	c.JSON(http.StatusCreated, saved)
}

func (h *skillhubHandlers) installConflict(projectID, namespace, slug, id string) error {
	if existing, found := skillhubInstalled(h.repo.Skills(), projectID, namespace, slug); found {
		return HTTPError{Status: http.StatusConflict, Message: fmt.Sprintf("本项目已安装该技能（标识 %s），如需新版本请使用更新", existing.ID)}
	}
	if existing, found := h.repo.Skill(id); found {
		owner := "其他项目"
		if existing.ProjectID == projectID {
			owner = "本项目"
		} else if p, ok := h.repo.Project(existing.ProjectID); ok && strings.TrimSpace(p.Name) != "" {
			owner = "项目「" + p.Name + "」"
		}
		return HTTPError{Status: http.StatusConflict, Message: fmt.Sprintf("技能标识「%s」已被%s使用，请换一个", id, owner)}
	}
	return nil
}

func (h *skillhubHandlers) update(c *gin.Context) {
	var input struct {
		SkillID string `json:"skillId"`
		Version string `json:"version"`
	}
	if !decode(c, &input) {
		return
	}
	version, ok := skillhubVersionParam(c, input.Version)
	if !ok {
		return
	}
	skillID := strings.TrimSpace(input.SkillID)
	skill, found := h.repo.Skill(skillID)
	if !found {
		c.JSON(http.StatusNotFound, map[string]string{"error": "skill not found"})
		return
	}
	if skill.Source == nil || skill.Source.Provider != domain.SkillSourceSkillHub {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "该技能不是从 SkillHub 安装的，无法更新"})
		return
	}
	src := *skill.Source
	if version == "" {
		detail, err := h.client.detail(c.Request.Context(), src.Slug, src.Namespace, h.userKey(c))
		if err != nil {
			skillhubFail(c, err)
			return
		}
		if detail.LatestVersion != nil {
			version = strings.TrimSpace(detail.LatestVersion.Version)
		}
		if version == "" {
			c.JSON(http.StatusBadGateway, map[string]string{"error": "SkillHub 未返回可安装的版本"})
			return
		}
	}
	files, err := h.skillhubPackage(c, src.Slug, src.Namespace, version)
	if err != nil {
		skillhubFail(c, err)
		return
	}

	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	// Re-read so concurrent metadata edits (name, sharing) are kept.
	current, found := h.repo.Skill(skillID)
	if !found || current.Source == nil {
		c.JSON(http.StatusConflict, map[string]string{"error": "技能已被删除或修改，请刷新后重试"})
		return
	}
	next := *current.Source
	next.Version = version
	next.UpdatedAt = time.Now().UTC()
	if next.Homepage == "" {
		next.Homepage = skillhubHomepage(next.Namespace, next.Slug)
	}
	current.Source = &next
	if err := h.repo.PutSkillSnapshot(current, files, false); err != nil {
		respond(c, err)
		return
	}
	saved, _ := h.repo.Skill(skillID)
	c.JSON(http.StatusOK, saved)
}

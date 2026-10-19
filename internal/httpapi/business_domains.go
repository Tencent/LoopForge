package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"
)

type businessDomainsHandlers struct{ *deps }

var businessDomainIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func hasBusinessUser(ids []int64, id int64) bool {
	for _, v := range ids {
		if id > 0 && v == id {
			return true
		}
	}
	return false
}
func (h *businessDomainsHandlers) canView(d domain.BusinessDomain, id int64, admin bool) bool {
	return admin || hasBusinessUser(d.ManagerIDs, id) || hasBusinessUser(d.ViewerIDs, id)
}
func (h *businessDomainsHandlers) domains(c *gin.Context) {
	id, admin := businessDomainIdentity(h.deps, c.Request)
	if c.Request.Method == http.MethodGet {
		items, err := h.repo.BusinessDomains()
		if err != nil {
			respond(c, err)
			return
		}
		out := []map[string]any{}
		for _, d := range items {
			out = append(out, map[string]any{"domain": d, "canManage": admin || hasBusinessUser(d.ManagerIDs, id), "canView": h.canView(d, id, admin)})
		}
		c.JSON(200, map[string]any{"items": out, "canAdmin": admin, "userId": id})
		return
	}
	var v domain.BusinessDomain
	if !decode(c, &v) {
		return
	}
	v.ID = strings.TrimSpace(v.ID)
	v.Name = strings.TrimSpace(v.Name)
	v.Description = strings.TrimSpace(v.Description)
	if !businessDomainIDPattern.MatchString(v.ID) || v.ID == "all" || v.ID == "unclassified" || v.Name == "" || len([]rune(v.Name)) > 100 || len([]rune(v.Description)) > 2000 {
		c.JSON(400, gin.H{"error": "标识须为 1–64 位字母、数字、下划线或短横线；名称须为 1–100 字，说明不超过 2000 字"})
		return
	}
	old, err := h.repo.BusinessDomain(v.ID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		respond(c, err)
		return
	}
	createOnly := c.Query("createOnly") == "true"
	if createOnly && err == nil {
		c.JSON(409, gin.H{"error": "业务域标识已存在"})
		return
	}
	if !admin {
		if err != nil || !hasBusinessUser(old.ManagerIDs, id) {
			c.JSON(403, gin.H{"error": "只有平台管理员或本业务域管理员可以维护业务域"})
			return
		}
		if v.Active != old.Active || !reflect.DeepEqual(v.ManagerIDs, old.ManagerIDs) {
			c.JSON(403, gin.H{"error": "只有平台管理员可以调整业务域状态和管理员"})
			return
		}
	}
	seen := map[int64]bool{}
	for _, ids := range [][]int64{v.ManagerIDs, v.ViewerIDs} {
		for _, uid := range ids {
			if uid <= 0 || seen[uid] {
				c.JSON(400, gin.H{"error": "管理员和查看者必须为不重复的有效用户"})
				return
			}
			if _, ok := h.repo.User(uid); !ok {
				c.JSON(400, gin.H{"error": "授权用户不存在"})
				return
			}
			seen[uid] = true
		}
	}
	var expected *time.Time
	if err == nil && !createOnly {
		expected = &old.UpdatedAt
	}
	if err := h.repo.PutBusinessDomain(v, expected); err != nil {
		if errors.Is(err, repo.ErrBusinessDomainConflict) {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			c.JSON(409, gin.H{"error": "业务域名称已存在"})
		} else {
			respond(c, err)
		}
		return
	}
	saved, err := h.repo.BusinessDomain(v.ID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, saved)
}
func (h *businessDomainsHandlers) report(c *gin.Context) {
	uid, admin := businessDomainIdentity(h.deps, c.Request)
	domains, err := h.repo.BusinessDomains()
	if err != nil {
		respond(c, err)
		return
	}
	requested := strings.TrimSpace(c.Query("businessDomainId"))
	if requested == "" {
		requested = "all"
	}
	visible := map[string]bool{}
	for _, d := range domains {
		if h.canView(d, uid, admin) {
			visible[d.ID] = true
		}
	}
	if requested != "all" && requested != "unclassified" {
		found := false
		for _, d := range domains {
			if d.ID == requested {
				found = true
			}
		}
		if !found {
			c.JSON(404, gin.H{"error": "业务域不存在"})
			return
		}
		if !visible[requested] {
			c.JSON(403, gin.H{"error": "未授权查看该业务域汇总"})
			return
		}
	}
	filter := domain.AnalyticsFilter{ProjectIDs: []string{}, Grain: strings.TrimSpace(c.Query("grain"))}
	if filter.Grain == "" {
		filter.Grain = "day"
	}
	if filter.Grain != "day" && filter.Grain != "week" && filter.Grain != "month" {
		c.JSON(400, gin.H{"error": "grain 必须为 day、week 或 month"})
		return
	}
	for _, target := range []struct {
		key   string
		value *time.Time
	}{{"from", &filter.From}, {"to", &filter.To}} {
		if raw := c.Query(target.key); raw != "" {
			at, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				c.JSON(400, gin.H{"error": "时间须使用 RFC3339 格式"})
				return
			}
			*target.value = at
		}
	}
	if !filter.From.IsZero() && !filter.To.IsZero() && filter.From.After(filter.To) {
		c.JSON(400, gin.H{"error": "开始时间不能晚于结束时间"})
		return
	}
	projects := h.repo.Projects()
	for _, p := range projects {
		selected := requested == "all" || requested == p.BusinessDomainID || (requested == "unclassified" && p.BusinessDomainID == "")
		allowed := admin || visible[p.BusinessDomainID] || (p.BusinessDomainID == "" && uid > 0 && p.OwnerID == uid)
		if selected && allowed {
			filter.ProjectIDs = append(filter.ProjectIDs, p.ID)
		}
	}
	result, err := h.repo.BusinessDomainReport(filter, projects, domains)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, result)
}

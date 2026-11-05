package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type businessDomainsHandlers struct {
	*deps
	reports businessReportCache
}

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
	filter.TimeZone = strings.TrimSpace(c.Query("timeZone"))
	if filter.TimeZone == "" {
		filter.TimeZone = "UTC"
	}
	if _, err := time.LoadLocation(filter.TimeZone); err != nil || filter.TimeZone == "Local" {
		c.JSON(400, gin.H{"error": "timeZone 须为有效的 IANA 时区"})
		return
	}
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
	result, err := h.cachedBusinessReport(businessReportCacheKey(requested, filter, projects, domains), func() (domain.BusinessDomainReport, error) {
		return h.repo.BusinessDomainReport(filter, projects, domains)
	})
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, result)
}

// businessReportCacheTTL 只用于合并同一瞬间的重复打开：报表本身仍由同一个
// 函数计算，数值口径不变。业务域名称/启停、项目名称与归属都进 key，改了立即
// 重新计算；运行与需求数据最多晚 10s。
const businessReportCacheTTL = 10 * time.Second
const businessReportCacheMaxEntries = 128

type businessReportCacheEntry struct {
	at     time.Time
	result domain.BusinessDomainReport
}

type businessReportCall struct {
	done   chan struct{}
	result domain.BusinessDomainReport
	err    error
}

type businessReportCache struct {
	mu       sync.Mutex
	entries  map[string]businessReportCacheEntry
	inflight map[string]*businessReportCall
}

func businessReportCacheKey(requested string, filter domain.AnalyticsFilter, projects []domain.Project, domains []domain.BusinessDomain) string {
	ids := append([]string(nil), filter.ProjectIDs...)
	sort.Strings(ids)
	// 报表直接展示业务域与所选项目的这些字段：把它们一起哈希进 key，任何写入
	// 路径（业务域编辑、项目改归属、MCP 工具）改动后都不会命中旧结果。
	selected := map[string]bool{}
	for _, id := range ids {
		selected[id] = true
	}
	sum := sha256.New()
	for _, d := range domains {
		active := "0"
		if d.Active {
			active = "1"
		}
		sum.Write([]byte("d\x00" + d.ID + "\x00" + d.Name + "\x00" + active + "\x01"))
	}
	for _, p := range projects {
		if selected[p.ID] {
			sum.Write([]byte("p\x00" + p.ID + "\x00" + p.Name + "\x00" + p.BusinessDomainID + "\x01"))
		}
	}
	catalog := hex.EncodeToString(sum.Sum(nil))
	stamp := func(at time.Time) string {
		if at.IsZero() {
			return ""
		}
		return at.UTC().Format(time.RFC3339Nano)
	}
	// 可见项目集合与查询参数共同构成 key：不同权限不会复用彼此的结果。
	return strings.Join([]string{requested, filter.Grain, filter.TimeZone, stamp(filter.From), stamp(filter.To), strings.Join(ids, ","), catalog}, "\x00")
}

// cachedBusinessReport 命中 10s 内的成功结果，并让并发的同口径请求共享一次
// 计算（首屏打开、多人同时刷新）。失败不缓存，调用方拿到的仍是同一次计算结果。
func (h *businessDomainsHandlers) cachedBusinessReport(key string, build func() (domain.BusinessDomainReport, error)) (domain.BusinessDomainReport, error) {
	c := &h.reports
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]businessReportCacheEntry{}
		c.inflight = map[string]*businessReportCall{}
	}
	if entry, ok := c.entries[key]; ok {
		if time.Since(entry.at) < businessReportCacheTTL {
			c.mu.Unlock()
			return entry.result, nil
		}
		delete(c.entries, key)
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-call.done
		return call.result, call.err
	}
	call := &businessReportCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	// If build panics, waiters must still be released and the key freed;
	// otherwise every later request for this key would block forever.
	finished := false
	defer func() {
		c.mu.Lock()
		if !finished {
			call.err = errors.New("业务域报表计算失败")
		}
		delete(c.inflight, key)
		close(call.done)
		c.mu.Unlock()
	}()
	result, err := build()
	finished = true

	c.mu.Lock()
	call.result, call.err = result, err
	if err == nil {
		now := time.Now()
		c.entries[key] = businessReportCacheEntry{at: now, result: result}
		if len(c.entries) > businessReportCacheMaxEntries {
			for k, entry := range c.entries {
				if now.Sub(entry.at) >= businessReportCacheTTL {
					delete(c.entries, k)
				}
			}
		}
	}
	c.mu.Unlock()
	return result, err
}

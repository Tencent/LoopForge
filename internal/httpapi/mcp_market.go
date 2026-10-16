package httpapi

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// Snapshot of Knot 太湖认证 MCP market (tag taihu_certified). Replaced by
// Knot open API once an application key is available.
//
//go:embed mcp_market_catalog.json
var mcpMarketCatalogJSON []byte

type mcpMarketItem struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags"`
	IconURL     string   `json:"iconUrl,omitempty"`
	DetailURL   string   `json:"detailUrl,omitempty"`
	URL         string   `json:"url"`
	Transport   string   `json:"transport"`
	AuthType    string   `json:"authType"`
	Official    bool     `json:"official"`
	Aliases     []string `json:"aliases,omitempty"`
}

type mcpMarketCatalogFile struct {
	Source    string          `json:"source"`
	Tag       string          `json:"tag"`
	FetchedAt string          `json:"fetchedAt"`
	Items     []mcpMarketItem `json:"items"`
}

var (
	mcpMarketCatalog mcpMarketCatalogFile
	taihuMCPMarket   []mcpMarketItem
)

func init() {
	if err := json.Unmarshal(mcpMarketCatalogJSON, &mcpMarketCatalog); err != nil {
		panic("mcp market catalog: " + err.Error())
	}
	taihuMCPMarket = mcpMarketCatalog.Items
}

var mcpInstallIDUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func mcpInstallID(marketID, scope, owner string) string {
	clean := func(v string) string { return strings.Trim(mcpInstallIDUnsafe.ReplaceAllString(v, "-"), "-") }
	id := fmt.Sprintf("mcp-%s-%s-%s", clean(marketID), clean(scope), clean(owner))
	if len(id) > 64 {
		// Keep existing IDs stable while bounding compound project/custom IDs
		// to the persisted key size. The digest includes the entire scope/owner.
		sum := sha256.Sum256([]byte(id))
		id = fmt.Sprintf("%.31s-%x", id, sum[:16])
	}
	return id
}

// defaultProjectMarketMCPAliases are installed on every project so new users
// can create Gongfeng MRs and sync TAPD without opening the MCP market.
var defaultProjectMarketMCPAliases = []string{}

// installMarketMCP adds one Taihu-certified market item to a project. If the
// same catalog entry is already installed there, it returns the existing row
// and created=false.
func installMarketMCP(st *repo.Store, marketID, projectID string) (domain.MCPServerDefinition, bool, error) {
	item, ok := marketItem(strings.TrimSpace(marketID))
	if !ok {
		return domain.MCPServerDefinition{}, false, fmt.Errorf("MCP 市场条目不存在")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return domain.MCPServerDefinition{}, false, fmt.Errorf("项目级 MCP 需要当前项目")
	}
	if st == nil {
		return domain.MCPServerDefinition{}, false, fmt.Errorf("项目不存在")
	}
	if _, exists := st.Project(projectID); !exists {
		return domain.MCPServerDefinition{}, false, fmt.Errorf("项目不存在")
	}
	for _, existing := range st.MCPServers() {
		if sameMarketItem(existing.MarketID, item.ID) && existing.Scope == "project" && existing.ProjectID == projectID {
			return existing, false, nil
		}
	}
	server := domain.MCPServerDefinition{
		ID: mcpInstallID(item.ID, "project", projectID), MarketID: item.ID, Name: item.Name,
		Description: item.Description, IconURL: item.IconURL, Transport: item.Transport,
		URL: item.URL, AuthType: item.AuthType, Scope: "project", ProjectID: projectID,
		TimeoutSec: 60, UpdatedAt: time.Now().UTC(),
	}
	if err := st.PutMCPServer(server); err != nil {
		return domain.MCPServerDefinition{}, false, err
	}
	return server, true, nil
}

func ensureDefaultProjectMCPs(st *repo.Store, projectID string) error {
	for _, alias := range defaultProjectMarketMCPAliases {
		if _, _, err := installMarketMCP(st, alias, projectID); err != nil {
			return err
		}
	}
	return nil
}

func ensureDefaultMCPsForAllProjects(st *repo.Store) error {
	if st == nil {
		return nil
	}
	for _, project := range st.Projects() {
		if err := ensureDefaultProjectMCPs(st, project.ID); err != nil {
			return err
		}
	}
	return nil
}

func marketItem(id string) (mcpMarketItem, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return mcpMarketItem{}, false
	}
	for _, item := range taihuMCPMarket {
		if item.ID == id {
			return item, true
		}
		for _, alias := range item.Aliases {
			if alias == id {
				return item, true
			}
		}
	}
	return mcpMarketItem{}, false
}

// 路由: g.GET("/api/mcp-market", h.mcpMarket)
type marketHandlers struct {
	*deps
}

func sameMarketItem(a, b string) bool {
	itemA, okA := marketItem(a)
	itemB, okB := marketItem(b)
	if okA && okB {
		return itemA.ID == itemB.ID
	}
	return a != "" && a == b
}

func mcpMarketSearchText(item mcpMarketItem) string {
	parts := []string{item.ID, item.Name, item.Description, item.Category, item.URL}
	parts = append(parts, item.Tags...)
	parts = append(parts, item.Aliases...)
	return strings.ToLower(strings.Join(parts, " "))
}

func filterMCPMarket(items []mcpMarketItem, q string) []mcpMarketItem {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		out := make([]mcpMarketItem, len(items))
		copy(out, items)
		return out
	}
	terms := strings.Fields(q)
	out := make([]mcpMarketItem, 0)
	for _, item := range items {
		hay := mcpMarketSearchText(item)
		matched := true
		for _, term := range terms {
			if !strings.Contains(hay, term) {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, item)
		}
	}
	return out
}

func (h *marketHandlers) mcpMarket(c *gin.Context) {
	items := filterMCPMarket(taihuMCPMarket, c.Query("q"))
	c.JSON(http.StatusOK, map[string]any{
		"source":    mcpMarketCatalog.Source,
		"fetchedAt": mcpMarketCatalog.FetchedAt,
		"total":     len(taihuMCPMarket),
		"items":     items,
	})
}

// 路由: g.POST("/api/mcp-market/install", h.mcpMarketInstall)
func (h *marketHandlers) mcpMarketInstall(c *gin.Context) {
	var input struct {
		MarketID  string `json:"marketId"`
		Scope     string `json:"scope"`
		ProjectID string `json:"projectId"`
	}
	if !decode(c, &input) {
		return
	}
	scope := strings.TrimSpace(input.Scope)
	if scope != "project" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "市场 MCP 只能添加到项目；个人范围请添加自定义 MCP"})
		return
	}
	server, created, err := installMarketMCP(h.repo, input.MarketID, input.ProjectID)
	if err != nil {
		if strings.Contains(err.Error(), "MCP 市场条目不存在") {
			c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		if strings.Contains(err.Error(), "项目级 MCP") {
			c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if strings.Contains(err.Error(), "项目不存在") {
			c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		respond(c, err)
		return
	}
	if !created {
		c.JSON(http.StatusConflict, map[string]string{"error": "当前范围已经添加过这个 MCP"})
		return
	}
	h.configureSync(h.repo.SyncConfig())
	c.JSON(http.StatusCreated, domain.PublicMCPServer(server))
}

// 路由: g.POST("/api/mcp-servers/custom", h.mcpCustomInstall)
func (h *marketHandlers) mcpCustomInstall(c *gin.Context) {
	var input struct {
		Server    domain.MCPServerDefinition `json:"server"`
		Scope     string                     `json:"scope"`
		ProjectID string                     `json:"projectId"`
	}
	if !decode(c, &input) {
		return
	}
	server := input.Server
	baseID := strings.TrimSpace(server.ID)
	server.MarketID = ""
	server.Scope = strings.TrimSpace(input.Scope)
	server.ProjectID = ""
	server.OwnerUserID = 0
	ownerKey := ""
	if server.Scope == "project" {
		server.ProjectID = strings.TrimSpace(input.ProjectID)
		if server.ProjectID == "" {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "项目级 MCP 需要当前项目"})
			return
		}
		if _, exists := h.repo.Project(server.ProjectID); !exists {
			c.JSON(http.StatusNotFound, map[string]string{"error": "项目不存在"})
			return
		}
		ownerKey = server.ProjectID
	} else if server.Scope == "personal" {
		user, authenticated := h.requestUser(c.Request, true)
		if !authenticated || user.ID <= 0 {
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "添加个人 MCP 前请先登录"})
			return
		}
		server.OwnerUserID = user.ID
		ownerKey = fmt.Sprintf("%d", user.ID)
	} else {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "请选择项目级或个人级"})
		return
	}
	if baseID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "自定义 MCP 需要标识"})
		return
	}
	server.ID = mcpInstallID("custom-"+baseID, server.Scope, ownerKey)
	if _, exists := h.repo.MCPServer(server.ID); exists {
		c.JSON(http.StatusConflict, map[string]string{"error": "当前范围已经存在相同标识的自定义 MCP"})
		return
	}
	if err := h.repo.PutMCPServer(server); err != nil {
		respond(c, err)
		return
	}
	h.configureSync(h.repo.SyncConfig())
	c.JSON(http.StatusCreated, domain.PublicMCPServer(server))
}

func visibleMCPServersForUser(items []domain.MCPServerDefinition, userID int64) []domain.MCPServerDefinition {
	out := make([]domain.MCPServerDefinition, 0, len(items))
	for _, item := range items {
		if item.Scope != "personal" || (userID > 0 && item.OwnerUserID == userID) {
			out = append(out, item)
		}
	}
	return out
}

func withMarketIcons(items []domain.MCPServerDefinition) []domain.MCPServerDefinition {
	out := append([]domain.MCPServerDefinition(nil), items...)
	for i := range out {
		item, ok := marketItem(out[i].MarketID)
		if ok && strings.TrimSpace(item.IconURL) != "" {
			out[i].IconURL = item.IconURL
		}
	}
	return out
}

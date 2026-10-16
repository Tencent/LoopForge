package tracker

import (
	"fmt"
	"maps"
	"net/url"
	"sort"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

type mcpBinding struct {
	URL     string
	Headers map[string]string
	Source  string
}

func bindTrackerMCP(kind string, cfg domain.SyncConfig, servers []domain.MCPServerDefinition, envToken, codebuddyAuth string) mcpBinding {
	kind = strings.ToLower(strings.TrimSpace(kind))
	cfgURL := strings.TrimSpace(cfg.GongfengMCPURL)
	exact := []string{"gongfeng-delivery", "gongfeng"}
	needles := []string{"gongfeng"}
	if kind == "tapd" {
		cfgURL = strings.TrimSpace(cfg.TAPDMCPURL)
		exact = []string{"tapd-delivery", "tapd_taihu", "tapd"}
		needles = []string{"tapd"}
	}

	catalog, hasCatalog := findCatalogHTTP(servers, exact, needles)
	out := mcpBinding{URL: cfgURL, Headers: map[string]string{}}
	if hasCatalog {
		if u := strings.TrimSpace(catalog.URL); u != "" {
			out.URL = u
		}
		if hdrs, err := runtime.ResolveMCPHeaders(catalog); err == nil && len(hdrs) > 0 {
			out.Headers = maps.Clone(hdrs)
			out.Source = "catalog:" + catalog.ID
		}
	}

	if kind == "tapd" && !headerHas(out.Headers, "Authorization") {
		if gf, ok := findCatalogHTTP(servers, []string{"gongfeng-delivery", "gongfeng"}, []string{"gongfeng"}); ok {
			gfURL := strings.TrimSpace(gf.URL)
			if gfURL == "" {
				gfURL = strings.TrimSpace(cfg.GongfengMCPURL)
			}
			if out.URL != "" && sameMCPHost(out.URL, gfURL) {
				if hdrs, err := runtime.ResolveMCPHeaders(gf); err == nil && headerHas(hdrs, "Authorization") {
					out.Headers = maps.Clone(hdrs)
					out.Source = "catalog:" + gf.ID + "+tapd-url"
				}
			}
		}
	}

	if out.URL == "" {
		return out
	}
	if !headerHas(out.Headers, "Authorization") {
		out.Headers = applyAuthFallback(out.Headers, envToken, codebuddyAuth)
		if out.Source == "" {
			if strings.TrimSpace(envToken) != "" {
				out.Source = "env:AGENTFLOW_MCP_TOKEN"
			} else if strings.TrimSpace(codebuddyAuth) != "" {
				out.Source = "codebuddy:mcp.json"
			} else {
				out.Source = "none"
			}
		}
	}
	return out
}

// bindProjectOwnerTrackerMCP resolves only assets owned by projectID and uses
// that project's owner's Taihu PAT for the tracker request. Taihu credentials
// are injected into the in-memory binding and are never persisted on the MCP
// asset itself.
func bindProjectOwnerTrackerMCP(kind string, cfg domain.SyncConfig, servers []domain.MCPServerDefinition, projectID string, owner domain.User) (mcpBinding, error) {
	binding, err := bindProjectUserTrackerMCP(kind, cfg, servers, projectID, owner, "Owner")
	if err != nil {
		return binding, err
	}
	binding.Source = "project-owner:" + strings.TrimSpace(owner.LoginName)
	return binding, nil
}

func bindProjectUserTrackerMCP(kind string, cfg domain.SyncConfig, servers []domain.MCPServerDefinition, projectID string, user domain.User, role string) (mcpBinding, error) {
	projectID = strings.TrimSpace(projectID)
	role = strings.TrimSpace(role)
	if role == "" {
		role = "用户"
	}
	projectServers := make([]domain.MCPServerDefinition, 0)
	for _, server := range servers {
		if server.Scope == "project" && strings.TrimSpace(server.ProjectID) == projectID && projectTrackerKindMatch(kind, server) {
			projectServers = append(projectServers, server)
		}
	}
	binding := bindTrackerMCP(kind, cfg, projectServers, "", "")
	if strings.TrimSpace(binding.URL) == "" {
		return binding, fmt.Errorf("项目 %s 未配置 %s MCP 地址", projectID, trackerDisplayName(kind))
	}
	token := strings.TrimSpace(user.TaihuPAT)
	if token == "" {
		return binding, fmt.Errorf("项目 %s 的 %s %s 未配置太湖个人令牌", projectID, role, strings.TrimSpace(user.LoginName))
	}
	binding.Headers = maps.Clone(binding.Headers)
	for key := range binding.Headers {
		if strings.EqualFold(strings.TrimSpace(key), "authorization") {
			delete(binding.Headers, key)
		}
	}
	binding.Headers["Authorization"] = bearerHeader(token)
	binding.Source = "project-user:" + strings.TrimSpace(user.LoginName)
	return binding, nil
}

func projectTrackerKindMatch(kind string, server domain.MCPServerDefinition) bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		return false
	}
	market := strings.ToLower(strings.TrimSpace(server.MarketID))
	if market == kind || market == kind+"-delivery" {
		return true
	}
	if kind == "tapd" && (market == "70" || market == "tapd_taihu") {
		return true
	}
	if kind == "gongfeng" && (market == "61" || market == "gongfeng-delivery") {
		return true
	}
	key := strings.ToLower(strings.TrimSpace(server.ID + " " + server.Name + " " + server.URL + " " + server.MarketID))
	return strings.Contains(key, kind)
}

func trackerDisplayName(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), "tapd") {
		return "TAPD"
	}
	return "工蜂"
}

func findCatalogHTTP(servers []domain.MCPServerDefinition, exactIDs, needles []string) (domain.MCPServerDefinition, bool) {
	byID := map[string]domain.MCPServerDefinition{}
	for _, srv := range servers {
		if !strings.EqualFold(strings.TrimSpace(srv.Transport), "http") {
			continue
		}
		byID[srv.ID] = srv
	}
	for _, id := range exactIDs {
		if srv, ok := byID[id]; ok {
			return srv, true
		}
	}
	var matches []domain.MCPServerDefinition
	for _, srv := range servers {
		if !strings.EqualFold(strings.TrimSpace(srv.Transport), "http") {
			continue
		}
		key := strings.ToLower(srv.ID + " " + srv.Name + " " + srv.URL)
		for _, needle := range needles {
			if needle != "" && strings.Contains(key, strings.ToLower(needle)) {
				matches = append(matches, srv)
				break
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	if len(matches) == 0 {
		return domain.MCPServerDefinition{}, false
	}
	return matches[0], true
}

func applyAuthFallback(hdrs map[string]string, envToken, codebuddyAuth string) map[string]string {
	out := hdrs
	if out == nil {
		out = map[string]string{}
	} else {
		out = maps.Clone(out)
	}
	if headerHas(out, "Authorization") {
		return out
	}
	if token := strings.TrimSpace(envToken); token != "" {
		out["Authorization"] = bearerHeader(token)
		return out
	}
	if auth := strings.TrimSpace(codebuddyAuth); auth != "" {
		out["Authorization"] = bearerHeader(auth)
	}
	return out
}

func bearerHeader(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return value
	}
	return "Bearer " + value
}

func headerHas(headers map[string]string, name string) bool {
	for k, v := range headers {
		if strings.EqualFold(k, name) && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

func sameMCPHost(a, b string) bool {
	ua, err1 := url.Parse(strings.TrimSpace(a))
	ub, err2 := url.Parse(strings.TrimSpace(b))
	if err1 != nil || err2 != nil || ua.Host == "" || ub.Host == "" {
		return false
	}
	return strings.EqualFold(ua.Host, ub.Host)
}

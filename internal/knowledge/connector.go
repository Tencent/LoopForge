package knowledge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

// CallTimeout bounds upstream discovery and read-only retrieval calls.
const CallTimeout = 30 * time.Second

var (
	// ErrConnectorNotFound is returned when a connector does not exist under
	// the caller's user scope.
	ErrConnectorNotFound = errors.New("知识库连接器不存在或无权访问")
	// ErrConnectorDisabled is returned when a referenced connector is disabled.
	ErrConnectorDisabled = errors.New("知识库连接器已停用")
)

// Source represents one sub-knowledge-base/source inside a Connector.
type Source struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
	DocCount    int    `json:"docCount,omitempty"`
}

// SourceScope constrains a Connector tool invocation to all accessible sources
// or an explicit allowlist.
type SourceScope struct {
	Mode    string
	Sources []string
}

// Normalized returns a deduplicated allowlist when Mode == "allowlist".
func (s SourceScope) Allowlist() []string {
	if strings.TrimSpace(s.Mode) != "allowlist" {
		return nil
	}
	return CleanList(s.Sources)
}

// Allows reports whether sourceID is permitted by this scope.
func (s SourceScope) Allows(sourceID string) bool {
	allowed := s.Allowlist()
	if len(allowed) == 0 {
		return strings.TrimSpace(s.Mode) != "allowlist"
	}
	return slices.Contains(allowed, strings.TrimSpace(sourceID))
}

// FilterIDs intersects requested source IDs with the scope allowlist.
// Empty requested expands to the allowlist; explicit out-of-scope IDs fail.
func (s SourceScope) FilterIDs(requested []string) ([]string, error) {
	req := CleanList(requested)
	allowed := s.Allowlist()
	if len(allowed) == 0 {
		if strings.TrimSpace(s.Mode) == "allowlist" {
			return nil, errors.New("知识库范围为空")
		}
		return req, nil
	}
	if len(req) == 0 {
		return allowed, nil
	}
	for _, id := range req {
		if !slices.Contains(allowed, id) {
			return nil, fmt.Errorf("知识库来源 %q 不在当前授权范围内", id)
		}
	}
	return req, nil
}

// CheckResult describes a live connectivity probe for one Connector.
type CheckResult struct {
	OK          bool     `json:"ok"`
	Error       string   `json:"error,omitempty"`
	LatencyMs   int64    `json:"latencyMs"`
	Sources     []Source `json:"sources,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	ConnectorID string   `json:"connectorId,omitempty"`
}

// CallResult is the unified output of Connector.CallTool. Returning structured
// Evidences lets every connector backend provide accurate source/document/chunk
// metadata without JSON re-parsing heuristics at the HTTP layer.
type CallResult struct {
	Status    string
	Text      string
	Hits      int
	Warning   string
	Targets   []string
	Evidences []domain.KnowledgeEvidence
	Citeable  bool
	LatencyMs int64
}

// ConnectorSpec holds the decrypted configuration passed to ConnectorFactory.New.
// It lives only inside server memory and is never serialized to clients.
type ConnectorSpec struct {
	ID          string
	OwnerUserID int64
	Name        string
	Type        string
	Endpoint    string
	Config      domain.KnowledgeConnectorConfig
	Secret      domain.KnowledgeConnectorSecret
	UpdatedAt   time.Time
	// ResolveSecret returns a live credential (refreshing OAuth tokens when
	// needed). Nil means the connector uses the static Secret above.
	ResolveSecret func(ctx context.Context, forceRefresh bool, failedToken string) (domain.KnowledgeConnectorSecret, error)
}

// Connector is a runtime handle to a user-configured read-only knowledge backend.
// Implementations must be concurrency-safe, strictly read-only, and never leak
// secrets into returned texts or error strings.
type Connector interface {
	Type() string
	Check(ctx context.Context) CheckResult
	ListSources(ctx context.Context) ([]Source, error)
	Tools(ctx context.Context) ([]mcpclient.Tool, error)
	CallTool(ctx context.Context, tool string, args map[string]any, scope SourceScope) (CallResult, error)
}

// ConnectorFactory validates configuration and constructs runtime Connector
// instances for one connector type. Adding a new backend requires implementing
// ConnectorFactory + Connector and registering it on Registry.
type ConnectorFactory interface {
	Meta() domain.ConnectorTypeMeta
	ValidateAndNormalize(endpoint string, cfg domain.KnowledgeConnectorConfig, secret domain.KnowledgeConnectorSecret, hasExistingSecret bool) (string, domain.KnowledgeConnectorConfig, domain.KnowledgeConnectorSecret, error)
	New(spec ConnectorSpec) (Connector, error)
}

// Registry maps connector type strings to registered ConnectorFactory handlers.
type Registry struct {
	mu        sync.RWMutex
	order     []string
	factories map[string]ConnectorFactory
}

// NewRegistry creates an empty Connector Registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: map[string]ConnectorFactory{},
	}
}

// DefaultRegistry returns a Registry preloaded with built-in connector backends.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(newObsidianFactory())
	r.Register(newNotionFactory())
	r.Register(newSiYuanFactory())
	r.Register(newFeishuFactory())
	r.Register(newWeKnoraFactory())
	r.Register(newMCPFactory())
	return r
}

// Register registers or replaces a ConnectorFactory by Meta().Type.
func (r *Registry) Register(factory ConnectorFactory) {
	if factory == nil {
		return
	}
	meta := factory.Meta()
	typ := strings.ToLower(strings.TrimSpace(meta.Type))
	if typ == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[typ]; !exists {
		r.order = append(r.order, typ)
	}
	r.factories[typ] = factory
}

// Factory looks up a registered ConnectorFactory case-insensitively.
func (r *Registry) Factory(typ string) (ConnectorFactory, bool) {
	key := strings.ToLower(strings.TrimSpace(typ))
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[key]
	return f, ok
}

// Types returns metadata for all registered connector types in registration order.
func (r *Registry) Types() []domain.ConnectorTypeMeta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]domain.ConnectorTypeMeta, 0, len(r.order))
	for _, typ := range r.order {
		if f, ok := r.factories[typ]; ok {
			out = append(out, f.Meta())
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func clipText(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if n <= 0 || len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if max <= 0 || len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// CleanList trims, drops empties, and preserves first-seen order without duplicates.
func CleanList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func stripBearer(raw string) string {
	s := strings.TrimSpace(raw)
	if len(s) >= 7 && strings.EqualFold(s[:7], "Bearer ") {
		return strings.TrimSpace(s[7:])
	}
	return s
}

func normalizeHeaderMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

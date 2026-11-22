package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

const (
	// weknoraSentinelID is a well-formed but zero knowledge base/document id.
	// Every capability probe targets it so a probe can never mutate real data.
	weknoraSentinelID = "00000000-0000-0000-0000-000000000000"
	// weknoraCapabilityTTL is how long one probed capability set stays fresh.
	weknoraCapabilityTTL = 5 * time.Minute
	// weknoraCapabilityUnknownTTL is the shorter TTL used when any probe was
	// inconclusive, so a recovering upstream is retried sooner.
	weknoraCapabilityUnknownTTL = time.Minute
	// weknoraCapabilityProbeTimeout bounds a single dry-run request.
	weknoraCapabilityProbeTimeout = 10 * time.Second
	// capabilityUnsupportedProviderReason is returned for MCP-only providers.
	capabilityUnsupportedProviderReason = "provider does not support WeKnora REST management"
)

// capabilityProbe is one zero-side-effect request against a sentinel id. A
// structured 4xx proves the route exists and the credential passed its gate.
type capabilityProbe struct {
	capability domain.KnowledgeCapability
	method     string
	path       string
	payload    any
}

func weKnoraCapabilityProbes() []capabilityProbe {
	id := weknoraSentinelID
	return []capabilityProbe{
		{domain.CapKBManage, http.MethodPatch, "/api/v1/knowledge-bases/" + id, map[string]any{}},
		{domain.CapDocWrite, http.MethodPut, "/api/v1/knowledge/manual/" + id, map[string]any{}},
		{domain.CapReparse, http.MethodPost, "/api/v1/knowledge/" + id + "/reparse", map[string]any{}},
		{domain.CapChunkRead, http.MethodGet, "/api/v1/chunks/" + id + "?page=1&page_size=1", nil},
		// Preview and download are two upstream routes with independent
		// permissions, so each gets its own probe and verdict; the aggregate
		// preview_download entry is derived from both afterwards.
		{domain.CapPreview, http.MethodGet, "/api/v1/knowledge/" + id + "/preview", nil},
		{domain.CapDownload, http.MethodGet, "/api/v1/knowledge/" + id + "/download", nil},
	}
}

// probeConnectionCapabilities discovers the management capabilities of one
// connection. Probes run concurrently with an individual timeout, and a failed
// probe only yields unknown/unavailable for that capability: it never turns the
// connection itself into a failure.
func probeConnectionCapabilities(ctx context.Context, conn domain.KnowledgeConnection, client *WeKnoraClient) domain.ConnectionCapabilities {
	return probeConnectionCapabilitiesWithin(ctx, conn, client, weknoraCapabilityProbeTimeout)
}

func probeConnectionCapabilitiesWithin(ctx context.Context, conn domain.KnowledgeConnection, client *WeKnoraClient, timeout time.Duration) domain.ConnectionCapabilities {
	if conn.EffectiveProvider() != domain.KnowledgeProviderWeKnora {
		return capabilitiesWithReason(domain.CapabilityUnavailable, capabilityUnsupportedProviderReason)
	}
	if client == nil {
		return capabilitiesWithReason(domain.CapabilityUnknown, "未配置 WeKnora 客户端，无法探测")
	}
	out := domain.ConnectionCapabilities{
		CheckedAt:    time.Now().UTC().Format(time.RFC3339),
		Capabilities: make(map[domain.KnowledgeCapability]domain.CapabilityStatus, len(domain.KnowledgeCapabilities)),
	}
	probes := weKnoraCapabilityProbes()
	results := make(chan capabilityProbeResult, len(probes))
	for _, probe := range probes {
		go func(probe capabilityProbe) {
			results <- capabilityProbeResult{
				capability: probe.capability,
				status:     runCapabilityProbe(ctx, client, probe, timeout),
			}
		}(probe)
	}
	for range probes {
		result := <-results
		out.Capabilities[result.capability] = result.status
	}
	// Aggregate for consoles that only know the older single dimension.
	out.Capabilities[domain.CapPreviewDownload] = mergeCapabilityStatuses(
		out.Capabilities[domain.CapPreview],
		out.Capabilities[domain.CapDownload],
	)
	return out
}

// mergeCapabilityStatuses folds two probe verdicts for one capability.
// Available wins because one reachable endpoint already proves the credential
// scope; the capability is only denied when every probed endpoint denies it.
func mergeCapabilityStatuses(a, b domain.CapabilityStatus) domain.CapabilityStatus {
	if a.Status == domain.CapabilityAvailable || b.Status == domain.CapabilityAvailable {
		return availableStatus()
	}
	if a.Status == domain.CapabilityUnavailable && b.Status == domain.CapabilityUnavailable {
		reasons := make([]string, 0, 2)
		for _, reason := range []string{a.Reason, b.Reason} {
			reason = strings.TrimSpace(reason)
			if reason != "" && !slices.Contains(reasons, reason) {
				reasons = append(reasons, reason)
			}
		}
		return unavailableStatus(strings.Join(reasons, "；"))
	}
	// At least one probe was inconclusive: keep the unknown verdict so the
	// shorter TTL retries, unless the other probe already proved availability.
	if a.Status == domain.CapabilityUnknown && strings.TrimSpace(a.Reason) != "" {
		return a
	}
	if b.Status == domain.CapabilityUnknown {
		return b
	}
	return a
}

type capabilityProbeResult struct {
	capability domain.KnowledgeCapability
	status     domain.CapabilityStatus
}

func runCapabilityProbe(ctx context.Context, client *WeKnoraClient, probe capabilityProbe, timeout time.Duration) domain.CapabilityStatus {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return classifyCapabilityProbe(client.doJSON(probeCtx, probe.method, probe.path, probe.payload, nil))
}

// classifyCapabilityProbe maps a probe outcome to a tri-state verdict:
//
//	2xx, 400, structured 404 -> available (the route answered business logic)
//	401/403, bare 404, 405, 501 -> unavailable (route/scope missing)
//	5xx, timeout, transport failure -> unknown (probe inconclusive)
//
// Any other status (409/422/429/3xx) is inconclusive on purpose: it proves
// neither that the route exists nor that the credential is missing a scope.
func classifyCapabilityProbe(err error) domain.CapabilityStatus {
	if err == nil {
		return availableStatus()
	}
	if status, denied := capabilityDenialStatus(err); denied {
		return status
	}
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		switch {
		case upstream.Status == http.StatusBadRequest:
			return availableStatus()
		case upstream.Status == http.StatusNotFound && upstream.Structured:
			return availableStatus()
		case upstream.Status >= 500:
			return unknownStatus(fmt.Sprintf("探测请求上游异常（HTTP %d），结果未知", upstream.Status))
		default:
			return unknownStatus(fmt.Sprintf("探测请求返回异常状态（HTTP %d），结果未知", upstream.Status))
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return unknownStatus("探测请求超时，结果未知")
	}
	return unknownStatus("探测请求失败，结果未知")
}

// capabilityDenialStatus recognizes failures that prove the capability is not
// usable: missing credential scope (401/403), a route the upstream does not
// serve (bare 404/405/501). A structured 404 is a real missing resource and
// must not be treated as a denial.
func capabilityDenialStatus(err error) (domain.CapabilityStatus, bool) {
	var upstream *UpstreamError
	if !errors.As(err, &upstream) {
		return domain.CapabilityStatus{}, false
	}
	switch {
	case upstream.Status == http.StatusUnauthorized || upstream.Status == http.StatusForbidden:
		return unavailableStatus(fmt.Sprintf("上游拒绝该操作（HTTP %d），连接凭据缺少对应权限", upstream.Status)), true
	case upstream.Status == http.StatusNotFound && !upstream.Structured:
		return unavailableStatus("上游未提供该管理接口（HTTP 404），探测路由缺失"), true
	case upstream.Status == http.StatusMethodNotAllowed || upstream.Status == http.StatusNotImplemented:
		return unavailableStatus(fmt.Sprintf("上游未提供该管理接口（HTTP %d）", upstream.Status)), true
	default:
		return domain.CapabilityStatus{}, false
	}
}

// capabilityUnavailableError is the structured 503 contract returned when a
// management action targets a capability the connection does not expose.
func capabilityUnavailableError(connectionID string, capability domain.KnowledgeCapability, status domain.CapabilityStatus) *apperr.Error {
	reason := strings.TrimSpace(status.Reason)
	message := "当前知识库连接不支持该管理操作：" + string(capability)
	if reason != "" {
		message += "（" + reason + "）"
	}
	return apperr.New(apperr.Unavailable, message).
		With("code", "capability_unavailable").
		With("capability", string(capability)).
		With("connectionId", connectionID).
		With("status", string(status.Status)).
		With("reason", reason)
}

func availableStatus() domain.CapabilityStatus {
	return domain.CapabilityStatus{Status: domain.CapabilityAvailable}
}

func unavailableStatus(reason string) domain.CapabilityStatus {
	return domain.CapabilityStatus{Status: domain.CapabilityUnavailable, Reason: reason}
}

func unknownStatus(reason string) domain.CapabilityStatus {
	return domain.CapabilityStatus{Status: domain.CapabilityUnknown, Reason: reason}
}

// capabilitiesWithReason builds a full five-entry set with one verdict, used
// when a probe cannot run at all.
func capabilitiesWithReason(state domain.CapabilityState, reason string) domain.ConnectionCapabilities {
	caps := domain.ConnectionCapabilities{
		CheckedAt:    time.Now().UTC().Format(time.RFC3339),
		Capabilities: make(map[domain.KnowledgeCapability]domain.CapabilityStatus, len(domain.KnowledgeCapabilities)),
	}
	for _, capability := range domain.KnowledgeCapabilities {
		caps.Capabilities[capability] = domain.CapabilityStatus{Status: state, Reason: reason}
	}
	return caps
}

func cloneCapabilitySet(in domain.ConnectionCapabilities) domain.ConnectionCapabilities {
	out := domain.ConnectionCapabilities{CheckedAt: in.CheckedAt}
	out.Capabilities = make(map[domain.KnowledgeCapability]domain.CapabilityStatus, len(in.Capabilities))
	for capability, status := range in.Capabilities {
		out.Capabilities[capability] = status
	}
	return out
}

// capabilityFingerprint invalidates a cached set as soon as any connection
// field that can change the upstream identity is edited.
func capabilityFingerprint(conn domain.KnowledgeConnection) string {
	raw := strings.Join([]string{
		conn.EffectiveProvider(),
		NormalizeWeKnoraBaseURL(conn.URL),
		strings.TrimSpace(conn.Token),
		strconv.FormatBool(conn.Enabled),
	}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

type capabilityCacheEntry struct {
	fingerprint string
	generation  uint64
	expiresAt   time.Time
	caps        domain.ConnectionCapabilities
}

type capabilityFlight struct {
	done       chan struct{}
	generation uint64
	caps       domain.ConnectionCapabilities
}

// capabilityCache is the in-process capability cache mounted on Service.
// Entries are keyed by connection id and invalidated by TTL, connection
// fingerprint, or an explicit generation bump. Concurrent callers share one
// in-flight probe per connection/config/generation.
type capabilityCache struct {
	mu          sync.RWMutex
	items       map[string]capabilityCacheEntry
	inflight    map[string]*capabilityFlight
	generations map[string]uint64
	ttl         time.Duration
	now         func() time.Time
}

func newCapabilityCache() *capabilityCache {
	return &capabilityCache{
		items:       map[string]capabilityCacheEntry{},
		inflight:    map[string]*capabilityFlight{},
		generations: map[string]uint64{},
		ttl:         weknoraCapabilityTTL,
		now:         time.Now,
	}
}

// get returns the cached capability set for conn, probing once when the entry
// is missing, expired or bound to a stale configuration fingerprint.
func (c *capabilityCache) get(ctx context.Context, conn domain.KnowledgeConnection, probe func(context.Context) domain.ConnectionCapabilities) domain.ConnectionCapabilities {
	key := connectionCacheKey(conn)
	fingerprint := capabilityFingerprint(conn)

	c.mu.RLock()
	generation := c.generations[key]
	if entry, ok := c.items[key]; ok && entry.fingerprint == fingerprint && entry.generation == generation && c.now().Before(entry.expiresAt) {
		caps := cloneCapabilitySet(entry.caps)
		c.mu.RUnlock()
		return caps
	}
	flightKey := capabilityFlightKey(key, fingerprint, generation)
	flight, running := c.inflight[flightKey]
	c.mu.RUnlock()
	if running {
		return c.await(ctx, flight)
	}

	c.mu.Lock()
	generation = c.generations[key]
	if entry, ok := c.items[key]; ok && entry.fingerprint == fingerprint && entry.generation == generation && c.now().Before(entry.expiresAt) {
		caps := cloneCapabilitySet(entry.caps)
		c.mu.Unlock()
		return caps
	}
	flightKey = capabilityFlightKey(key, fingerprint, generation)
	if flight, running := c.inflight[flightKey]; running {
		c.mu.Unlock()
		return c.await(ctx, flight)
	}
	flight = &capabilityFlight{done: make(chan struct{}), generation: generation}
	c.inflight[flightKey] = flight
	c.mu.Unlock()

	// The probe result is shared by every waiter, so it runs detached from the
	// caller's request context: one canceled request must not cancel the probe
	// for everyone else. It stays bounded by the probe timeout, and the caller
	// below still returns as soon as its own context ends.
	go func() {
		var caps domain.ConnectionCapabilities
		defer func() {
			// A probe (or its transport) must never take the process down or
			// leave waiters blocked, so cleanup and completion live in defer.
			if recovered := recover(); recovered != nil {
				caps = capabilitiesWithReason(domain.CapabilityUnknown, "能力探测异常（内部错误）")
			}
			c.mu.Lock()
			if c.inflight[flightKey] == flight {
				delete(c.inflight, flightKey)
			}
			if c.generations[key] == flight.generation {
				c.rememberLocked(key, fingerprint, flight.generation, caps)
			}
			flight.caps = cloneCapabilitySet(caps)
			close(flight.done)
			c.mu.Unlock()
		}()
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), weknoraCapabilityProbeTimeout)
		defer cancel()
		caps = probe(probeCtx)
	}()
	return c.await(ctx, flight)
}

func capabilityFlightKey(key, fingerprint string, generation uint64) string {
	return key + "|" + fingerprint + "|" + strconv.FormatUint(generation, 10)
}

func (c *capabilityCache) await(ctx context.Context, flight *capabilityFlight) domain.ConnectionCapabilities {
	select {
	case <-flight.done:
		return cloneCapabilitySet(flight.caps)
	case <-ctx.Done():
		return capabilitiesWithReason(domain.CapabilityUnknown, "等待能力探测结果超时")
	}
}

func (c *capabilityCache) rememberLocked(key, fingerprint string, generation uint64, caps domain.ConnectionCapabilities) {
	now := c.now()
	// A slow probe for an older configuration must not clobber a newer entry;
	// fingerprint matching already protects readers, but skipping the write
	// also avoids a pointless re-probe.
	if entry, ok := c.items[key]; ok && entry.fingerprint != fingerprint && now.Before(entry.expiresAt) {
		return
	}
	c.items[key] = capabilityCacheEntry{
		fingerprint: fingerprint,
		generation:  generation,
		expiresAt:   now.Add(c.ttlFor(caps)),
		caps:        cloneCapabilitySet(caps),
	}
}

func (c *capabilityCache) ttlFor(caps domain.ConnectionCapabilities) time.Duration {
	for _, status := range caps.Capabilities {
		if status.Status == domain.CapabilityUnknown {
			return weknoraCapabilityUnknownTTL
		}
	}
	return c.ttl
}

// invalidate drops the cached set of one connection and bumps its generation so
// an in-flight probe started before the invalidation cannot write back.
func (c *capabilityCache) invalidate(connectionID string) {
	connectionID = strings.TrimSpace(connectionID)
	if connectionID == "" {
		connectionID = "default"
	}
	c.mu.Lock()
	delete(c.items, connectionID)
	c.generations[connectionID]++
	c.mu.Unlock()
}

// demoteDenied rewrites one cached capability to unavailable after a live
// management call was refused even though the cache said available. It reports
// a denial for any recognizable refusal, whether or not an entry was cached.
func (c *capabilityCache) demoteDenied(conn domain.KnowledgeConnection, capability domain.KnowledgeCapability, err error) (domain.CapabilityStatus, bool) {
	status, denied := capabilityDenialStatus(err)
	if !denied {
		return domain.CapabilityStatus{}, false
	}
	c.demote(conn, capability, status)
	return status, true
}

func (c *capabilityCache) demote(conn domain.KnowledgeConnection, capability domain.KnowledgeCapability, status domain.CapabilityStatus) bool {
	key := connectionCacheKey(conn)
	fingerprint := capabilityFingerprint(conn)
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		// A concurrent refresh may have dropped the entry between the gated
		// request and this denial. The probe it started must not write back an
		// available verdict that predates the live denial, so invalidate the
		// generation even though there is no entry to rewrite. The fingerprint
		// guard below still protects other configurations of the same key.
		c.generations[key]++
		return false
	}
	if entry.fingerprint != fingerprint {
		return false
	}
	caps := cloneCapabilitySet(entry.caps)
	caps.Capabilities[capability] = status
	// preview_download is derived from the two endpoint verdicts, so a live
	// denial must recompute it. Without this, denying both endpoints one after
	// the other would leave the aggregate stuck at available and keep older
	// consoles (which only read the aggregate) permissive.
	if capability == domain.CapPreview || capability == domain.CapDownload {
		caps.Capabilities[domain.CapPreviewDownload] = mergeCapabilityStatuses(
			caps.Capabilities[domain.CapPreview],
			caps.Capabilities[domain.CapDownload],
		)
	}
	// Bump the generation so a probe that started before the denial cannot
	// overwrite the demotion when it finishes, and refresh the TTL so the
	// demotion is not immediately treated as an expired entry.
	c.generations[key]++
	c.items[key] = capabilityCacheEntry{
		fingerprint: fingerprint,
		generation:  c.generations[key],
		expiresAt:   c.now().Add(c.ttlFor(caps)),
		caps:        caps,
	}
	return true
}

func connectionCacheKey(conn domain.KnowledgeConnection) string {
	if key := strings.TrimSpace(conn.ID); key != "" {
		return key
	}
	return "default"
}

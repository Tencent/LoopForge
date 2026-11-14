package preview

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"agentflow/internal/domain"
)

var ErrIsolatedOriginRequired = errors.New("preview requires AGENTFLOW_PREVIEW_PUBLIC_URL on a separate hostname and explicit studio origins in AGENTFLOW_PREVIEW_FRAME_ANCESTORS")

// ErrStudioBackend rejects preview backends on a studio hostname. It is an
// ErrInvalidPreviewBackendURL so callers keep answering 400.
var ErrStudioBackend = fmt.Errorf("%w: preview backend must not use a studio hostname", domain.ErrInvalidPreviewBackendURL)

// PublicOrigin returns the validated serving origin for the studio's frame-src
// policy. Reuse the gateway's isolation checks; invalid config must not widen CSP.
func (s *Service) PublicOrigin() string {
	if !s.Enabled() {
		return ""
	}
	public, _, err := s.isolatedOrigins()
	if err != nil {
		return ""
	}
	return public.Scheme + "://" + public.Host
}

// IsolationError explains why online previews are unavailable, or returns nil
// when the configuration can issue and serve sessions.
func (s *Service) IsolationError() error {
	if !s.Enabled() {
		return ErrIsolatedOriginRequired
	}
	_, _, err := s.isolatedOrigins()
	return err
}

// ReservedHost returns the hostname reserved for preview content, or "" when
// none is configured or the configured one is a studio hostname.
func (s *Service) ReservedHost() string {
	if !s.Enabled() {
		return ""
	}
	if public := s.reservedOrigin(); public != nil {
		return public.Host
	}
	return ""
}

// configuredOrigin is AGENTFLOW_PREVIEW_PUBLIC_URL on its own, before any
// check against the studio origins.
func (s *Service) configuredOrigin() *url.URL {
	if s.cfg.PublicHost == "" {
		return nil
	}
	return parseOrigin(s.cfg.PublicScheme + "://" + s.cfg.PublicHost)
}

// reservedOrigin keeps the preview hostname reserved even when the embedding
// settings are missing or invalid: that disables sessions, but must not reopen
// studio pages or APIs on a hostname that may still hold preview documents.
// A hostname also configured as a studio is never reserved; older deployments
// pointed AGENTFLOW_PREVIEW_PUBLIC_URL at the studio itself.
func (s *Service) reservedOrigin() *url.URL {
	public := s.configuredOrigin()
	if public == nil || s.isStudioHostname(public.Host) {
		return nil
	}
	return public
}

// studioHostnames lists the hosts known to serve the studio: every parseable
// FRAME_ANCESTORS origin, and AGENTFLOW_PUBLIC_URL when it is set.
func (s *Service) studioHostnames() []string {
	var hosts []string
	for _, raw := range strings.Fields(s.cfg.FrameAncestors) {
		if parent := parseOrigin(raw); parent != nil {
			hosts = append(hosts, parent.Host)
		}
	}
	if raw := strings.TrimSpace(s.cfg.StudioURL); raw != "" {
		if _, host, ok := SplitPublicURL(raw); ok {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func (s *Service) isStudioHostname(host string) bool {
	for _, studio := range s.studioHostnames() {
		if sameHostname(host, studio) {
			return true
		}
	}
	return false
}

// isolatedOrigins accepts only explicit origins. In particular, a different
// port on the studio hostname does not isolate its host-only session cookie.
func (s *Service) isolatedOrigins() (*url.URL, []*url.URL, error) {
	public := s.configuredOrigin()
	if public == nil {
		return nil, nil, fmt.Errorf("%w (AGENTFLOW_PREVIEW_PUBLIC_URL is missing or not an http(s) origin)", ErrIsolatedOriginRequired)
	}
	if s.isStudioHostname(public.Host) {
		return nil, nil, fmt.Errorf("%w (AGENTFLOW_PREVIEW_PUBLIC_URL uses a studio hostname)", ErrIsolatedOriginRequired)
	}
	var parents []*url.URL
	for _, raw := range strings.Fields(s.cfg.FrameAncestors) {
		parent := parseOrigin(raw)
		if parent == nil {
			return nil, nil, fmt.Errorf("%w (AGENTFLOW_PREVIEW_FRAME_ANCESTORS entry %q is not an exact http(s) origin)", ErrIsolatedOriginRequired, raw)
		}
		parents = append(parents, parent)
	}
	if len(parents) == 0 {
		return nil, nil, fmt.Errorf("%w (AGENTFLOW_PREVIEW_FRAME_ANCESTORS is empty)", ErrIsolatedOriginRequired)
	}
	return public, parents, nil
}

func parseOrigin(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" ||
		strings.ContainsAny(u.Host, "*;\\\t\r\n ") {
		return nil
	}
	u.Path = ""
	return u
}

func sameHostname(a, b string) bool {
	hostname := func(raw string) string {
		u, err := url.Parse("http://" + raw)
		if err != nil {
			return ""
		}
		return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	}
	a, b = hostname(a), hostname(b)
	return a != "" && a == b
}

// IsPreviewHost also reserves other ports on the configured hostname. No
// platform page or API may be served there, even with a valid studio cookie,
// and even while the embedding settings are incomplete.
func (s *Service) IsPreviewHost(req *http.Request) bool {
	public := s.reservedOrigin()
	return public != nil && req != nil && sameHostname(req.Host, public.Host)
}

func (s *Service) validateSessionOrigin(req *http.Request) error {
	_, parents, err := s.isolatedOrigins()
	if err != nil {
		return err
	}
	for _, parent := range parents {
		if req != nil && sameHostname(req.Host, parent.Host) {
			return nil
		}
	}
	return fmt.Errorf("%w (the requesting studio hostname is not listed in AGENTFLOW_PREVIEW_FRAME_ANCESTORS)", ErrIsolatedOriginRequired)
}

func (s *Service) validateBackendOrigin(backend string) error {
	if _, _, err := s.isolatedOrigins(); err != nil {
		return err
	}
	u, err := url.Parse(backend)
	if err != nil {
		return domain.ErrInvalidPreviewBackendURL
	}
	if u.Host != "" && s.isStudioHostname(u.Host) {
		return ErrStudioBackend
	}
	return nil
}

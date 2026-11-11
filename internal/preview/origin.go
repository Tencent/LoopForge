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

// isolatedOrigins accepts only explicit origins. In particular, a different
// port on the studio hostname does not isolate its host-only session cookie.
func (s *Service) isolatedOrigins() (*url.URL, []*url.URL, error) {
	public := parseOrigin(s.cfg.PublicScheme + "://" + s.cfg.PublicHost)
	if public == nil {
		return nil, nil, ErrIsolatedOriginRequired
	}
	var parents []*url.URL
	for _, raw := range strings.Fields(s.cfg.FrameAncestors) {
		parent := parseOrigin(raw)
		if parent == nil || sameHostname(parent.Host, public.Host) {
			return nil, nil, ErrIsolatedOriginRequired
		}
		parents = append(parents, parent)
	}
	if len(parents) == 0 {
		return nil, nil, ErrIsolatedOriginRequired
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
// platform page or API may be served there, even with a valid studio cookie.
func (s *Service) IsPreviewHost(req *http.Request) bool {
	public, _, err := s.isolatedOrigins()
	return err == nil && req != nil && sameHostname(req.Host, public.Host)
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
	return ErrIsolatedOriginRequired
}

func (s *Service) validateBackendOrigin(backend string) error {
	_, parents, err := s.isolatedOrigins()
	if err != nil {
		return err
	}
	u, err := url.Parse(backend)
	if err != nil {
		return domain.ErrInvalidPreviewBackendURL
	}
	for _, parent := range parents {
		if sameHostname(u.Host, parent.Host) {
			return fmt.Errorf("%w: preview backend must not use a studio hostname", domain.ErrInvalidPreviewBackendURL)
		}
	}
	return nil
}

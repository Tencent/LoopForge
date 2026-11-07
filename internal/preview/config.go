package preview

import (
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultMaxFiles       = 20_000
	DefaultMaxCompressed  = 100 << 20
	DefaultMaxExpanded    = 500 << 20
	HardMaxFiles          = 50_000
	HardMaxCompressed     = 500 << 20
	HardMaxExpanded       = 2 << 30
	DefaultRetention      = 30 * 24 * time.Hour
	MaxRetention          = 30 * 24 * time.Hour
	DefaultFinalizeAfter  = 15 * time.Minute
	DefaultSessionTTL     = 30 * time.Minute
	DefaultIdleGCInterval = time.Minute
)

// Config is the control-plane preview policy. Defaults are conservative and
// every field is overridable; the UI must not hard-code TTLs or size caps.
type Config struct {
	PublicHost          string
	PublicScheme        string
	PublicURL           string
	FrameAncestors      string
	MaxFiles            int
	MaxCompressed       int64
	MaxExpanded         int64
	DefaultRetention    time.Duration
	FinalizationTimeout time.Duration
	SessionTTL          time.Duration
}

func LoadConfig() Config {
	cfg := Config{
		PublicScheme:        strings.TrimSpace(os.Getenv("AGENTFLOW_PREVIEW_PUBLIC_SCHEME")),
		PublicURL:           strings.TrimSpace(os.Getenv("AGENTFLOW_PREVIEW_PUBLIC_URL")),
		FrameAncestors:      strings.TrimSpace(os.Getenv("AGENTFLOW_PREVIEW_FRAME_ANCESTORS")),
		MaxFiles:            intFromEnv("AGENTFLOW_PREVIEW_MAX_FILES", DefaultMaxFiles),
		MaxCompressed:       int64FromEnv("AGENTFLOW_PREVIEW_MAX_COMPRESSED", DefaultMaxCompressed),
		MaxExpanded:         int64FromEnv("AGENTFLOW_PREVIEW_MAX_EXPANDED", DefaultMaxExpanded),
		DefaultRetention:    durationFromEnv("AGENTFLOW_PREVIEW_TTL", DefaultRetention),
		FinalizationTimeout: durationFromEnv("AGENTFLOW_PREVIEW_FINALIZE_TIMEOUT", DefaultFinalizeAfter),
		SessionTTL:          durationFromEnv("AGENTFLOW_PREVIEW_SESSION_TTL", DefaultSessionTTL),
	}
	if cfg.DefaultRetention <= 0 || cfg.DefaultRetention > MaxRetention {
		cfg.DefaultRetention = DefaultRetention
	}
	if cfg.MaxFiles < 1 || cfg.MaxFiles > HardMaxFiles {
		cfg.MaxFiles = DefaultMaxFiles
	}
	if cfg.MaxCompressed < 1 || cfg.MaxCompressed > HardMaxCompressed {
		cfg.MaxCompressed = DefaultMaxCompressed
	}
	if cfg.MaxExpanded < 1 || cfg.MaxExpanded > HardMaxExpanded {
		cfg.MaxExpanded = DefaultMaxExpanded
	}
	if cfg.PublicScheme == "" {
		cfg.PublicScheme = "https"
	}
	if cfg.FrameAncestors == "" {
		cfg.FrameAncestors = "'self'"
	}
	if scheme, host, ok := SplitPublicURL(cfg.PublicURL); ok {
		cfg.PublicScheme = scheme
		cfg.PublicHost = host
	}
	return cfg
}

func intFromEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func int64FromEnv(key string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func durationFromEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// SplitPublicURL parses an optional preview serving origin such as
// https://loopforge.example.com. Path and query are ignored; the gateway still
// serves /preview/{token}/ on that host.
func SplitPublicURL(raw string) (scheme, host string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", "", false
	}
	return u.Scheme, u.Host, true
}

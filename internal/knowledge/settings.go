package knowledge

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

var errLightCloudNotConfigured = errors.New("LightCloud 知识库未接入：请在知识中心配置地址和 Token 并启用")

type settingsStore interface {
	KnowledgeSettings() domain.KnowledgeSettings
	PutKnowledgeSettings(domain.KnowledgeSettings) error
}

func (s *Service) Settings() domain.KnowledgeSettings {
	if s != nil {
		if s.settings != nil {
			return *s.settings
		}
		if store, ok := s.store.(settingsStore); ok {
			return store.KnowledgeSettings()
		}
	}
	return domain.KnowledgeSettings{}
}

// Snapshot keeps routing and authorization on the same configuration for an
// operation. Subsequent requests read the stored settings again.
func (s *Service) Snapshot() *Service {
	if s == nil || s.settings != nil {
		return s
	}
	snapshot := *s
	settings := s.Settings()
	snapshot.settings = &settings
	return &snapshot
}

func (s *Service) Builtin() bool {
	return s.Settings().EffectiveProvider() == domain.KnowledgeProviderBuiltin
}

func validateLightCloudURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return apperr.New(apperr.Invalid, "知识库地址必须是 http(s)://host/mcp 形式，凭据请填写到 Token 字段")
	}
	return nil
}

type SettingsInput struct {
	Provider       string              `json:"provider"`
	Enabled        *bool               `json:"enabled"`
	URL            *string             `json:"url"`
	Token          string              `json:"token"`
	ClearToken     bool                `json:"clearToken"`
	WriteEnabled   *bool               `json:"writeEnabled"`
	DefaultSources []string            `json:"defaultSources"`
	ProjectSources map[string][]string `json:"projectSources"`
}

// UpdateSettings replaces the settings. An empty token keeps the saved one
// unless ClearToken is set.
func (s *Service) UpdateSettings(input SettingsInput, actor string) (domain.KnowledgeSettings, error) {
	settingsStore, ok := s.store.(settingsStore)
	if !ok {
		return domain.KnowledgeSettings{}, apperr.New(apperr.Unavailable, "知识引擎配置不可用")
	}
	current := settingsStore.KnowledgeSettings()
	provider := strings.TrimSpace(input.Provider)
	if provider == "" {
		provider = domain.KnowledgeProviderLightCloud // legacy connection-settings API
	}
	if provider != domain.KnowledgeProviderBuiltin && provider != domain.KnowledgeProviderLightCloud {
		return current, apperr.New(apperr.Invalid, "知识引擎必须为 builtin 或 lightcloud")
	}
	next := current
	next.Provider = provider
	next.UpdatedBy = actor
	if provider == domain.KnowledgeProviderLightCloud {
		if input.URL != nil {
			next.URL = strings.TrimSpace(*input.URL)
		}
		if err := validateLightCloudURL(next.URL); err != nil {
			return current, err
		}
		if input.Enabled != nil {
			next.Enabled = *input.Enabled
		}
		if input.WriteEnabled != nil {
			next.WriteEnabled = *input.WriteEnabled
		}
		next.Token = resolveSettingsToken(input, current.Token)
		if input.DefaultSources != nil {
			next.DefaultSources = cleanSources(input.DefaultSources)
		}
		if input.ProjectSources != nil {
			next.ProjectSources = cleanProjectSources(input.ProjectSources)
		}
		if next.Enabled && !next.ExternalReady() {
			return current, apperr.New(apperr.Invalid, "启用 LightCloud 前需要填写知识库地址和 Token")
		}
	}
	if err := settingsStore.PutKnowledgeSettings(next); err != nil {
		return current, err
	}
	return s.Settings(), nil
}

// cleanProjectSources normalizes the per-project source lists, dropping
// blank project keys. A nil map stays nil so "no overrides" is preserved.
func cleanProjectSources(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := map[string][]string{}
	for project, list := range in {
		if project = strings.TrimSpace(project); project != "" {
			out[project] = cleanSources(list)
		}
	}
	return out
}

// resolveSettingsToken picks the token to save: a new token (without its
// Bearer prefix), else the current one unless the input asks to clear it.
func resolveSettingsToken(input SettingsInput, current string) string {
	switch token := strings.TrimSpace(input.Token); {
	case token != "":
		return stripBearer(token)
	case !input.ClearToken:
		return current
	}
	return ""
}

// CheckLightCloud tests prospective credentials without changing saved settings.
func (s *Service) CheckLightCloud(ctx context.Context, overrideURL, overrideToken string) CheckResult {
	settings := s.Settings()
	if value := strings.TrimSpace(overrideURL); value != "" {
		settings.URL = value
	}
	if value := strings.TrimSpace(overrideToken); value != "" {
		settings.Token = stripBearer(value)
	}
	if err := validateLightCloudURL(settings.URL); err != nil {
		return CheckResult{Error: err.Error()}
	}
	client, err := NewClient(settings)
	return s.checkUpstream(ctx, client, err)
}

func (s *Service) AgentToolAllowed(name string) bool {
	if s.Builtin() {
		return IsAgentTool(name)
	}
	switch name {
	case ToolSearch, ToolFetch, ToolListSources, ToolGetSource:
		return true
	}
	return false
}

func (s *Service) RequireBuiltin() error {
	if !s.Builtin() {
		return apperr.New(apperr.Invalid, "此功能仅内置知识引擎可用，当前使用 LightCloud").With("code", "knowledge_builtin_only")
	}
	return nil
}

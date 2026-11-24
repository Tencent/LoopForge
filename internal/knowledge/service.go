package knowledge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

// Store is the persistence contract required by the knowledge Service.
type Store interface {
	ListKnowledgeConnectors(ownerUserID int64) []domain.KnowledgeConnector
	GetKnowledgeConnector(ownerUserID int64, id string) (domain.KnowledgeConnector, bool)
	PutKnowledgeConnector(conn domain.KnowledgeConnector) error
	DeleteKnowledgeConnector(ownerUserID int64, id string) (bool, error)
}

// ConnectorInput captures create/update/check requests for a user's Connector.
type ConnectorInput struct {
	ID          string                           `json:"id,omitempty"`
	Name        string                           `json:"name"`
	Type        string                           `json:"type"`
	Endpoint    string                           `json:"endpoint"`
	Config      *domain.KnowledgeConnectorConfig `json:"config,omitempty"`
	Secret      domain.KnowledgeConnectorSecret  `json:"secret,omitempty"`
	ClearSecret bool                             `json:"clearSecret,omitempty"`
	Enabled     *bool                            `json:"enabled,omitempty"`
}

type cachedConnectorEntry struct {
	stamp    string
	instance Connector
}

// Service manages user-owned knowledge connectors, credential sealing,
// validation via ConnectorFactory, and runtime Connector lifecycle.
type Service struct {
	store    Store
	registry *Registry
	now      func() time.Time
	newID    func() string

	mu        sync.Mutex
	instances map[string]cachedConnectorEntry
}

// NewService constructs a Service backed by Store and DefaultRegistry().
func NewService(store Store, newID func() string) *Service {
	return NewServiceWithRegistry(store, DefaultRegistry(), newID)
}

// NewServiceWithRegistry constructs a Service with a custom Registry (useful
// for registering additional connector plugins or test mocks).
func NewServiceWithRegistry(store Store, registry *Registry, newID func() string) *Service {
	if registry == nil {
		registry = DefaultRegistry()
	}
	return &Service{
		store:     store,
		registry:  registry,
		now:       time.Now,
		newID:     newID,
		instances: map[string]cachedConnectorEntry{},
	}
}

// Registry returns the underlying ConnectorFactory registry.
func (s *Service) Registry() *Registry {
	return s.registry
}

// ConnectorTypes returns declarative UI/validation metadata for every
// registered connector type.
func (s *Service) ConnectorTypes() []domain.ConnectorTypeMeta {
	return s.registry.Types()
}

// ListConnectors returns client-safe connectors owned by ownerUserID.
func (s *Service) ListConnectors(ownerUserID int64) []domain.KnowledgeConnector {
	rows := s.store.ListKnowledgeConnectors(ownerUserID)
	out := make([]domain.KnowledgeConnector, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Public())
	}
	return out
}

// GetConnector returns one client-safe connector only if owned by ownerUserID.
func (s *Service) GetConnector(ownerUserID int64, id string) (domain.KnowledgeConnector, bool) {
	c, ok := s.store.GetKnowledgeConnector(ownerUserID, id)
	if !ok {
		return domain.KnowledgeConnector{}, false
	}
	return c.Public(), true
}

// CreateConnector validates, seals credentials, and saves a new connector
// owned by ownerUserID.
func (s *Service) CreateConnector(_ context.Context, ownerUserID int64, input ConnectorInput) (domain.KnowledgeConnector, error) {
	typ := strings.ToLower(strings.TrimSpace(input.Type))
	factory, ok := s.registry.Factory(typ)
	if !ok {
		return domain.KnowledgeConnector{}, apperr.New(apperr.Invalid, fmt.Sprintf("不支持的知识库连接器类型 %q", input.Type))
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.KnowledgeConnector{}, apperr.New(apperr.Invalid, "连接器名称不能为空")
	}

	var rawCfg domain.KnowledgeConnectorConfig
	if input.Config != nil {
		rawCfg = *input.Config
	}
	endpoint, cfg, secret, err := factory.ValidateAndNormalize(input.Endpoint, rawCfg, input.Secret, false)
	if err != nil {
		return domain.KnowledgeConnector{}, err
	}

	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = s.generateID(typ)
	}
	if _, exists := s.store.GetKnowledgeConnector(ownerUserID, id); exists {
		return domain.KnowledgeConnector{}, apperr.New(apperr.Invalid, fmt.Sprintf("连接器标识 %q 已存在", id))
	}

	sealed, err := SealSecret(ownerUserID, id, secret)
	if err != nil {
		return domain.KnowledgeConnector{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := s.now().UTC()
	conn := domain.KnowledgeConnector{
		ID:               id,
		OwnerUserID:      ownerUserID,
		Name:             name,
		Type:             factory.Meta().Type,
		Endpoint:         endpoint,
		Config:           cfg,
		SecretEnc:        sealed,
		SecretConfigured: sealed != "",
		Enabled:          enabled,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.store.PutKnowledgeConnector(conn); err != nil {
		return domain.KnowledgeConnector{}, apperr.New(apperr.Invalid, err.Error())
	}
	saved, ok := s.store.GetKnowledgeConnector(ownerUserID, id)
	if !ok {
		return conn.Public(), nil
	}
	return saved.Public(), nil
}

// UpdateConnector updates an existing connector belonging to ownerUserID.
// Omitting secret preserves the existing encrypted credential unless ClearSecret is true.
func (s *Service) UpdateConnector(_ context.Context, ownerUserID int64, id string, input ConnectorInput) (domain.KnowledgeConnector, error) {
	existing, ok := s.store.GetKnowledgeConnector(ownerUserID, id)
	if !ok {
		return domain.KnowledgeConnector{}, apperr.New(apperr.NotFound, ErrConnectorNotFound.Error())
	}
	typ := strings.ToLower(strings.TrimSpace(firstNonEmpty(input.Type, existing.Type)))
	factory, ok := s.registry.Factory(typ)
	if !ok {
		return domain.KnowledgeConnector{}, apperr.New(apperr.Invalid, fmt.Sprintf("不支持的知识库连接器类型 %q", typ))
	}
	name := strings.TrimSpace(firstNonEmpty(input.Name, existing.Name))
	if name == "" {
		return domain.KnowledgeConnector{}, apperr.New(apperr.Invalid, "连接器名称不能为空")
	}
	endpointIn := strings.TrimSpace(input.Endpoint)
	if endpointIn == "" {
		endpointIn = existing.Endpoint
	}

	rawCfg := existing.Config
	if input.Config != nil {
		rawCfg = *input.Config
	}
	keepExistingSecret := !input.ClearSecret && !input.Secret.HasCredential() && strings.TrimSpace(existing.SecretEnc) != ""
	endpoint, cfg, secret, err := factory.ValidateAndNormalize(endpointIn, rawCfg, input.Secret, keepExistingSecret)
	if err != nil {
		return domain.KnowledgeConnector{}, err
	}

	var sealed string
	switch {
	case input.Secret.HasCredential():
		sealed, err = SealSecret(ownerUserID, existing.ID, secret)
		if err != nil {
			return domain.KnowledgeConnector{}, err
		}
	case keepExistingSecret:
		sealed = existing.SecretEnc
	default:
		sealed = ""
	}

	enabled := existing.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	updated := domain.KnowledgeConnector{
		ID:               existing.ID,
		OwnerUserID:      ownerUserID,
		Name:             name,
		Type:             factory.Meta().Type,
		Endpoint:         endpoint,
		Config:           cfg,
		SecretEnc:        sealed,
		SecretConfigured: sealed != "",
		Enabled:          enabled,
		CreatedAt:        existing.CreatedAt,
	}
	if err := s.store.PutKnowledgeConnector(updated); err != nil {
		return domain.KnowledgeConnector{}, err
	}
	s.evictInstance(ownerUserID, existing.ID)
	saved, ok := s.store.GetKnowledgeConnector(ownerUserID, existing.ID)
	if !ok {
		return updated.Public(), nil
	}
	return saved.Public(), nil
}

// DeleteConnector deletes a connector only if owned by ownerUserID.
func (s *Service) DeleteConnector(ownerUserID int64, id string) error {
	deleted, err := s.store.DeleteKnowledgeConnector(ownerUserID, id)
	if err != nil {
		return err
	}
	if !deleted {
		return apperr.New(apperr.NotFound, ErrConnectorNotFound.Error())
	}
	s.evictInstance(ownerUserID, id)
	return nil
}

// CheckConnector runs a live connectivity probe for either an existing saved
// connector (with optional unsaved overrides) or an unsaved form input.
func (s *Service) CheckConnector(ctx context.Context, ownerUserID int64, id string, override *ConnectorInput) CheckResult {
	id = strings.TrimSpace(id)
	var spec ConnectorSpec
	var factory ConnectorFactory

	if id != "" {
		saved, ok := s.store.GetKnowledgeConnector(ownerUserID, id)
		if !ok {
			return CheckResult{ConnectorID: id, OK: false, Error: ErrConnectorNotFound.Error()}
		}
		typ := saved.Type
		endpoint := saved.Endpoint
		cfg := saved.Config
		var secret domain.KnowledgeConnectorSecret
		hasSavedSecret := strings.TrimSpace(saved.SecretEnc) != ""
		if hasSavedSecret {
			opened, err := OpenSecret(ownerUserID, saved.ID, saved.SecretEnc)
			if err != nil {
				return CheckResult{ConnectorID: id, OK: false, Error: err.Error()}
			}
			secret = opened
		}
		useSavedSecret := hasSavedSecret
		if override != nil {
			if strings.TrimSpace(override.Type) != "" {
				typ = override.Type
			}
			if strings.TrimSpace(override.Endpoint) != "" {
				endpoint = override.Endpoint
			}
			if override.Config != nil {
				cfg = *override.Config
			}
			if override.ClearSecret {
				secret = domain.KnowledgeConnectorSecret{}
				useSavedSecret = false
			} else if override.Secret.HasCredential() {
				secret = override.Secret
				useSavedSecret = false
			}
		}
		f, ok := s.registry.Factory(typ)
		if !ok {
			return CheckResult{ConnectorID: id, OK: false, Error: fmt.Sprintf("不支持的知识库连接器类型 %q", typ)}
		}
		factory = f
		normEndpoint, normCfg, normSecret, err := factory.ValidateAndNormalize(endpoint, cfg, secret, hasSavedSecret && useSavedSecret)
		if err != nil {
			return CheckResult{ConnectorID: id, OK: false, Error: err.Error()}
		}
		if useSavedSecret {
			normSecret = secret
		}
		spec = ConnectorSpec{
			ID:          saved.ID,
			OwnerUserID: ownerUserID,
			Name:        saved.Name,
			Type:        factory.Meta().Type,
			Endpoint:    normEndpoint,
			Config:      normCfg,
			Secret:      normSecret,
			UpdatedAt:   saved.UpdatedAt,
		}
	} else {
		if override == nil {
			return CheckResult{OK: false, Error: "请提供连接器配置参数"}
		}
		f, ok := s.registry.Factory(override.Type)
		if !ok {
			return CheckResult{OK: false, Error: fmt.Sprintf("不支持的知识库连接器类型 %q", override.Type)}
		}
		factory = f
		var rawCfg domain.KnowledgeConnectorConfig
		if override.Config != nil {
			rawCfg = *override.Config
		}
		normEndpoint, normCfg, normSecret, err := factory.ValidateAndNormalize(override.Endpoint, rawCfg, override.Secret, false)
		if err != nil {
			return CheckResult{OK: false, Error: err.Error()}
		}
		spec = ConnectorSpec{
			ID:          "probe",
			OwnerUserID: ownerUserID,
			Name:        override.Name,
			Type:        factory.Meta().Type,
			Endpoint:    normEndpoint,
			Config:      normCfg,
			Secret:      normSecret,
		}
	}

	conn, err := factory.New(spec)
	if err != nil {
		return CheckResult{ConnectorID: spec.ID, OK: false, Error: RedactSecrets(err.Error(), spec.Secret)}
	}
	res := conn.Check(ctx)
	res.ConnectorID = id
	return res
}

// ListConnectorSources lists sub-sources exposed by one connector belonging
// to ownerUserID (regardless of whether it is currently enabled, so admins/owners
// can browse sources in management UI).
func (s *Service) ListConnectorSources(ctx context.Context, ownerUserID int64, id string) ([]Source, error) {
	saved, conn, err := s.resolveConnector(ownerUserID, id, false)
	if err != nil {
		return nil, err
	}
	_ = saved
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	return conn.ListSources(cctx)
}

// ResolveEnabledConnector loads and builds the runtime Connector for id only if
// owned by ownerUserID and currently enabled.
func (s *Service) ResolveEnabledConnector(ownerUserID int64, id string) (domain.KnowledgeConnector, Connector, error) {
	return s.resolveConnector(ownerUserID, id, true)
}

func (s *Service) resolveConnector(ownerUserID int64, id string, requireEnabled bool) (domain.KnowledgeConnector, Connector, error) {
	saved, ok := s.store.GetKnowledgeConnector(ownerUserID, id)
	if !ok {
		return domain.KnowledgeConnector{}, nil, apperr.New(apperr.NotFound, ErrConnectorNotFound.Error())
	}
	if requireEnabled && !saved.Enabled {
		return domain.KnowledgeConnector{}, nil, apperr.New(apperr.Forbidden, ErrConnectorDisabled.Error())
	}
	cacheKey := instanceKey(ownerUserID, saved.ID)
	stamp := fmt.Sprintf("%d|%s|%s|%s", saved.UpdatedAt.UTC().UnixNano(), saved.Type, saved.Endpoint, saved.SecretEnc)

	s.mu.Lock()
	if entry, ok := s.instances[cacheKey]; ok && entry.stamp == stamp {
		s.mu.Unlock()
		return saved.Public(), entry.instance, nil
	}
	s.mu.Unlock()

	factory, ok := s.registry.Factory(saved.Type)
	if !ok {
		return domain.KnowledgeConnector{}, nil, apperr.New(apperr.Unavailable, fmt.Sprintf("不支持的知识库连接器类型 %q", saved.Type))
	}
	secret, err := OpenSecret(ownerUserID, saved.ID, saved.SecretEnc)
	if err != nil {
		return domain.KnowledgeConnector{}, nil, apperr.New(apperr.Unavailable, err.Error())
	}
	spec := ConnectorSpec{
		ID:          saved.ID,
		OwnerUserID: ownerUserID,
		Name:        saved.Name,
		Type:        saved.Type,
		Endpoint:    saved.Endpoint,
		Config:      saved.Config,
		Secret:      secret,
		UpdatedAt:   saved.UpdatedAt,
	}
	instance, err := factory.New(spec)
	if err != nil {
		return domain.KnowledgeConnector{}, nil, err
	}
	s.mu.Lock()
	if len(s.instances) >= 256 {
		s.instances = map[string]cachedConnectorEntry{}
	}
	s.instances[cacheKey] = cachedConnectorEntry{stamp: stamp, instance: instance}
	s.mu.Unlock()
	return saved.Public(), instance, nil
}

func (s *Service) evictInstance(ownerUserID int64, id string) {
	s.mu.Lock()
	delete(s.instances, instanceKey(ownerUserID, id))
	s.mu.Unlock()
}

func instanceKey(ownerUserID int64, id string) string {
	return fmt.Sprintf("%d:%s", ownerUserID, strings.TrimSpace(id))
}

func (s *Service) generateID(typ string) string {
	prefix := "kc"
	if typ != "" {
		prefix = typ
	}
	if s.newID != nil {
		if raw := strings.TrimSpace(s.newID()); raw != "" {
			return prefix + "-" + raw
		}
	}
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return prefix + "-" + hex.EncodeToString(buf)
}

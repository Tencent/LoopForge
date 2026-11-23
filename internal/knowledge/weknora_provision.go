package knowledge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

var defaultManagedWeKnoraMu sync.Mutex

// EnsureDefaultManagedWeKnora auto-provisions the built-in WeKnora connection
// when LOOPFORGE_WEKNORA_URL is configured and KnowledgeSettings have never
// been configured or touched by an administrator. The final persistence goes
// through Store.MutateKnowledgeSettings (SELECT ... FOR UPDATE in MySQL) with
// OnlyIfFresh=true so a concurrent admin edit or multi-replica race can never
// overwrite modified settings.
func (s *Service) EnsureDefaultManagedWeKnora(ctx context.Context) (domain.KnowledgeSettings, bool) {
	if strings.TrimSpace(os.Getenv("LOOPFORGE_WEKNORA_URL")) == "" {
		return s.Settings(), false
	}
	cur := s.Settings()
	if !freshInstallSettings(cur) {
		return cur, false
	}
	defaultManagedWeKnoraMu.Lock()
	defer defaultManagedWeKnoraMu.Unlock()
	cur = s.Settings()
	if !freshInstallSettings(cur) {
		return cur, false
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := s.ProvisionWeKnora(pctx, WeKnoraProvisionInput{
		Mode:         "managed",
		Name:         "内置 WeKnora",
		WriteEnabled: true,
		OnlyIfFresh:  true,
	}, "system:auto-weknora")
	if err != nil || !res.Claimed {
		return s.Settings(), false
	}
	return s.Settings(), true
}

func freshInstallSettings(cur domain.KnowledgeSettings) bool {
	return cur.UpdatedAt.IsZero() &&
		strings.TrimSpace(cur.UpdatedBy) == "" &&
		len(cur.Connections) == 0 &&
		strings.TrimSpace(cur.URL) == "" &&
		strings.TrimSpace(cur.Token) == "" &&
		!cur.TokenConfigured
}

type WeKnoraProvisionInput struct {
	Mode         string `json:"mode"` // "managed" | "external"
	Name         string `json:"name"`
	URL          string `json:"url"`
	Token        string `json:"token"`
	Email        string `json:"email"`
	Password     string `json:"password"`
	WriteEnabled bool   `json:"writeEnabled"`
	OnlyIfFresh  bool   `json:"onlyIfFresh,omitempty"`
}

type WeKnoraProvisionResult struct {
	Settings domain.KnowledgeSettings `json:"settings"`
	UIURL    string                   `json:"uiUrl,omitempty"`
	Message  string                   `json:"message"`
	Claimed  bool                     `json:"claimed,omitempty"`
}

// ProvisionWeKnora either starts a managed WeKnora stack in the current Docker
// network and auto-provisions an API key, or links an existing WeKnora server
// using either a direct API key or email/password login.
func (s *Service) ProvisionWeKnora(ctx context.Context, input WeKnoraProvisionInput, actor string) (WeKnoraProvisionResult, error) {
	mode := strings.ToLower(strings.TrimSpace(input.Mode))
	if mode == "" {
		mode = "external"
	}

	var targetURL, apiKey, uiURL, msg, account, connName string
	bootstrap := func() error {
		switch mode {
		case "managed":
			network := strings.TrimSpace(os.Getenv("AGENTFLOW_DOCKER_NETWORK"))
			if network == "" {
				network = "loopforge_default"
			}
			baseURL := strings.TrimSpace(os.Getenv("LOOPFORGE_WEKNORA_URL"))
			if baseURL == "" {
				baseURL = "http://weknora-app:8080"
			}
			validated, err := ValidateOutboundWeKnoraURL(baseURL)
			if err != nil {
				return apperr.New(apperr.Invalid, err.Error())
			}
			baseURL = validated
			uiURL = strings.TrimSpace(os.Getenv("LOOPFORGE_WEKNORA_UI_URL"))
			if uiURL == "" {
				uiURL = "http://localhost:18081"
			}

			if err := ensureManagedWeKnoraContainers(ctx, network, baseURL); err != nil {
				return apperr.New(apperr.Upstream, err.Error())
			}
			email := firstNonEmpty(strings.TrimSpace(input.Email), "admin@loopforge.local")
			password := strings.TrimSpace(input.Password)
			if password == "" {
				password = strings.TrimSpace(os.Getenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD"))
			}
			if password == "" {
				password = strings.TrimSpace(s.store.ManagedWeKnoraAdminPassword())
			}
			if password == "" {
				password = generateManagedWeKnoraPassword()
				if err := s.store.PutManagedWeKnoraAdminPassword(password); err != nil {
					return err
				}
			}
			key, err := BootstrapWeKnoraAPIKey(ctx, baseURL, email, password, "loopforge")
			if err != nil {
				return apperr.New(apperr.Upstream, "自动初始化 WeKnora API Key 失败："+err.Error())
			}
			targetURL = baseURL
			apiKey = key
			msg = "已启动内置 WeKnora 实例并自动写入知识中心配置"

		case "external":
			validated, err := ValidateOutboundWeKnoraURL(input.URL)
			if err != nil {
				if errors.Is(err, ErrNotConfigured) {
					return apperr.New(apperr.Invalid, "请填写已有 WeKnora 服务地址")
				}
				return apperr.New(apperr.Invalid, err.Error())
			}
			targetURL = validated
			if tok := strings.TrimSpace(input.Token); tok != "" {
				apiKey = stripBearer(tok)
			} else if strings.TrimSpace(input.Email) != "" && strings.TrimSpace(input.Password) != "" {
				key, err := BootstrapWeKnoraAPIKey(ctx, targetURL, strings.TrimSpace(input.Email), input.Password, "")
				if err != nil {
					return apperr.New(apperr.Upstream, "登录 WeKnora 获取 API Key 失败："+err.Error())
				}
				apiKey = key
			} else {
				return apperr.New(apperr.Invalid, "请填写 API Key，或填写 WeKnora 账号密码以自动获取")
			}
			check := s.CheckWithProvider(ctx, domain.KnowledgeProviderWeKnora, targetURL, apiKey)
			if !check.OK {
				return apperr.New(apperr.Upstream, "连接 WeKnora 失败："+check.Error)
			}
			msg = "已成功链接外部 WeKnora 实例并保存配置"

		default:
			return apperr.New(apperr.Invalid, "无效的接入模式（仅支持 managed 或 external）")
		}

		account = strings.TrimSpace(input.Email)
		if mode == "managed" && account == "" {
			account = "admin@loopforge.local"
		}
		connName = strings.TrimSpace(input.Name)
		if connName == "" {
			if mode == "managed" {
				connName = "内置 WeKnora"
			} else if account != "" {
				connName = account
			} else {
				connName = targetURL
			}
		}
		return nil
	}

	var (
		beforeConns          []domain.KnowledgeConnection
		afterConns           []domain.KnowledgeConnection
		migratedLegacyConnID string
		claimed              bool
	)
	applyMutation := func(current *domain.KnowledgeSettings) bool {
		if input.OnlyIfFresh && !freshInstallSettings(*current) {
			return false
		}
		beforeConns = append([]domain.KnowledgeConnection(nil), current.Connections...)
		conns := make([]domain.KnowledgeConnection, 0, len(current.Connections)+1)
		migratedLegacyConnID = ""
		if len(current.Connections) > 0 {
			conns = append(conns, current.Connections...)
		} else if strings.TrimSpace(current.URL) != "" && (strings.TrimSpace(current.Token) != "" || current.TokenConfigured) {
			migratedLegacyConnID = "conn-" + randomHex(4)
			conns = append(conns, domain.KnowledgeConnection{
				ID:       migratedLegacyConnID,
				Name:     firstNonEmpty(current.URL, "默认连接"),
				Provider: current.EffectiveProvider(),
				URL:      current.URL,
				Token:    current.Token,
				Enabled:  true,
			})
		}

		updatedExisting := false
		for i := range conns {
			if conns[i].URL == targetURL && conns[i].Name == connName {
				conns[i].Token = apiKey
				conns[i].Account = firstNonEmpty(account, conns[i].Account)
				conns[i].Provider = domain.KnowledgeProviderWeKnora
				conns[i].Enabled = true
				updatedExisting = true
				break
			}
		}
		if !updatedExisting {
			conns = append(conns, domain.KnowledgeConnection{
				ID:       "wk-" + randomHex(4),
				Name:     connName,
				Account:  account,
				Provider: domain.KnowledgeProviderWeKnora,
				URL:      targetURL,
				Token:    apiKey,
				Enabled:  true,
			})
		}

		defaultSources := current.DefaultSources
		projectSources := current.ProjectSources
		if migratedLegacyConnID != "" {
			defaultSources = prefixBareSources(defaultSources, migratedLegacyConnID)
			projectSources = prefixBareProjectSources(projectSources, migratedLegacyConnID)
		}

		*current = domain.KnowledgeSettings{
			Provider:       domain.KnowledgeProviderWeKnora,
			Enabled:        true,
			WriteEnabled:   input.WriteEnabled || current.WriteEnabled,
			URL:            conns[0].URL,
			Token:          conns[0].Token,
			Connections:    conns,
			DefaultSources: defaultSources,
			ProjectSources: projectSources,
			UpdatedBy:      actor,
		}
		afterConns = append([]domain.KnowledgeConnection(nil), conns...)
		claimed = true
		return true
	}

	var err error
	if input.OnlyIfFresh {
		// Serialize first-install bootstrap under the fresh-install row lock so
		// concurrent replicas do not create duplicate orphan API keys on WeKnora
		// and never overwrite an admin who cleared connections.
		claimed, err = s.store.MutateKnowledgeSettingsIfFresh(func(current *domain.KnowledgeSettings) (bool, error) {
			if !freshInstallSettings(*current) {
				return false, nil
			}
			if bErr := bootstrap(); bErr != nil {
				return false, bErr
			}
			return applyMutation(current), nil
		})
	} else {
		if err = bootstrap(); err == nil {
			err = s.store.MutateKnowledgeSettings(applyMutation)
		}
	}
	if err != nil {
		return WeKnoraProvisionResult{}, err
	}
	if !claimed {
		saved := s.store.KnowledgeSettings()
		return WeKnoraProvisionResult{
			Settings: saved.Public(),
			UIURL:    uiURL,
			Message:  "知识中心配置已存在，跳过默认初始化",
			Claimed:  false,
		}, nil
	}
	s.invalidateChangedConnectionCapabilities(beforeConns, afterConns)
	if migratedLegacyConnID != "" {
		s.migrateLegacySubmissionSources(migratedLegacyConnID)
	}
	saved := s.store.KnowledgeSettings()
	return WeKnoraProvisionResult{
		Settings: saved.Public(),
		UIURL:    uiURL,
		Message:  msg,
		Claimed:  true,
	}, nil
}

// DeleteConnection removes one saved connection by id.
func (s *Service) DeleteConnection(connID, actor string) (domain.KnowledgeSettings, error) {
	current := s.store.KnowledgeSettings()
	connID = strings.TrimSpace(connID)
	kept := make([]domain.KnowledgeConnection, 0, len(current.Connections))
	for _, c := range current.Connections {
		if c.ID != connID {
			kept = append(kept, c)
		}
	}
	current.Connections = kept
	current.UpdatedBy = actor
	if len(kept) > 0 {
		current.URL = kept[0].URL
		current.Token = kept[0].Token
		current.Provider = kept[0].Provider
	} else {
		current.URL = ""
		current.Token = ""
		current.Enabled = false
	}
	if err := s.store.PutKnowledgeSettings(current); err != nil {
		return current, err
	}
	s.capabilities.invalidate(connID)
	if err := s.store.DeleteKnowledgeConnectionToken(connID); err != nil {
		return current, err
	}
	return s.store.KnowledgeSettings(), nil
}

// EncodeScopedSourceID namespaces a KB/document ID with its connection ID.
// It always prefixes with connID:: unless connID is empty or "default", and is
// idempotent if rawID already carries the same prefix.
func EncodeScopedSourceID(_ domain.KnowledgeSettings, connID, rawID string) string {
	rawID = strings.TrimSpace(rawID)
	connID = strings.TrimSpace(connID)
	if rawID == "" || connID == "" || connID == "default" {
		return rawID
	}
	prefix := connID + "::"
	if strings.HasPrefix(rawID, prefix) {
		return rawID
	}
	return prefix + rawID
}

// DecodeScopedSourceID extracts the target connection and raw KB/document ID.
// Prefixed IDs must match an active connection by ID; bare IDs are only valid
// in pure legacy single-connection mode (len(settings.Connections) == 0).
func DecodeScopedSourceID(settings domain.KnowledgeSettings, scopedID string) (domain.KnowledgeConnection, string, bool) {
	conns := settings.ActiveConnections()
	if len(conns) == 0 {
		return domain.KnowledgeConnection{}, scopedID, false
	}
	trimmed := strings.TrimSpace(scopedID)
	if connID, raw, ok := strings.Cut(trimmed, "::"); ok {
		for _, c := range conns {
			if c.ID == connID {
				return c, raw, true
			}
		}
		return domain.KnowledgeConnection{}, raw, false
	}
	if len(settings.Connections) > 0 {
		return domain.KnowledgeConnection{}, trimmed, false
	}
	return conns[0], trimmed, true
}

func prefixBareSources(sources []string, connID string) []string {
	if len(sources) == 0 {
		return sources
	}
	out := make([]string, len(sources))
	for i, src := range sources {
		trimmed := strings.TrimSpace(src)
		if trimmed != "" && !strings.Contains(trimmed, "::") {
			out[i] = connID + "::" + trimmed
		} else {
			out[i] = trimmed
		}
	}
	return out
}

func prefixBareProjectSources(m map[string][]string, connID string) map[string][]string {
	if len(m) == 0 {
		return m
	}
	out := make(map[string][]string, len(m))
	for k, v := range m {
		out[k] = prefixBareSources(v, connID)
	}
	return out
}

func (s *Service) migrateLegacySubmissionSources(connID string) {
	for _, status := range []string{domain.KnowledgeSubmissionPending, domain.KnowledgeSubmissionRejected} {
		subs := s.store.KnowledgeSubmissions("", status, 500)
		for _, sub := range subs {
			src := strings.TrimSpace(sub.Source)
			if src == "" || strings.Contains(src, "::") {
				continue
			}
			if full, ok := s.store.KnowledgeSubmission(sub.ID); ok {
				full.Source = connID + "::" + strings.TrimSpace(full.Source)
				_ = s.store.PutKnowledgeSubmission(full)
			}
		}
	}
}

func generateManagedWeKnoraPassword() string {
	return "Lf!" + randomHex(12)
}

func settingsForConnection(base domain.KnowledgeSettings, conn domain.KnowledgeConnection) domain.KnowledgeSettings {
	out := base
	out.Provider = conn.Provider
	out.URL = conn.URL
	out.Token = conn.Token
	return out
}

// BootstrapWeKnoraAPIKey registers (if username is non-empty and account does not
// exist yet), logs in, and issues a full_access tenant API key.
func BootstrapWeKnoraAPIKey(ctx context.Context, baseURL, email, password, username string) (string, error) {
	baseURL = NormalizeWeKnoraBaseURL(baseURL)
	hc := &http.Client{Timeout: 20 * time.Second}

	if username != "" {
		regBody, _ := json.Marshal(map[string]string{
			"username": username,
			"email":    email,
			"password": password,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/auth/register", bytes.NewReader(regBody))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			if resp, err := hc.Do(req); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}
	}

	loginBody, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/auth/login", bytes.NewReader(loginBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", parseWeKnoraError(resp.StatusCode, raw)
	}
	var loginOut struct {
		Success bool   `json:"success"`
		Token   string `json:"token"`
		Message string `json:"message"`
		User    *struct {
			TenantID uint64 `json:"tenant_id"`
		} `json:"user"`
		ActiveTenant *struct {
			ID uint64 `json:"id"`
		} `json:"active_tenant"`
	}
	if err := json.Unmarshal(raw, &loginOut); err != nil {
		return "", err
	}
	if !loginOut.Success || strings.TrimSpace(loginOut.Token) == "" {
		return "", fmt.Errorf("WeKnora 登录失败：%s", firstNonEmpty(loginOut.Message, "未返回 token"))
	}
	var tenantID uint64
	if loginOut.ActiveTenant != nil && loginOut.ActiveTenant.ID > 0 {
		tenantID = loginOut.ActiveTenant.ID
	} else if loginOut.User != nil && loginOut.User.TenantID > 0 {
		tenantID = loginOut.User.TenantID
	}
	if tenantID == 0 {
		return "", fmt.Errorf("WeKnora 登录响应缺少 tenant_id")
	}

	keyBody, _ := json.Marshal(map[string]any{
		"name":        "loopforge-kb",
		"full_access": true,
	})
	keyReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/api/v1/tenants/%d/api-keys", baseURL, tenantID), bytes.NewReader(keyBody))
	if err != nil {
		return "", err
	}
	keyReq.Header.Set("Content-Type", "application/json")
	keyReq.Header.Set("Authorization", "Bearer "+loginOut.Token)
	keyResp, err := hc.Do(keyReq)
	if err != nil {
		return "", err
	}
	keyRaw, _ := io.ReadAll(io.LimitReader(keyResp.Body, 1<<20))
	keyResp.Body.Close()
	if keyResp.StatusCode < 200 || keyResp.StatusCode >= 300 {
		return "", parseWeKnoraError(keyResp.StatusCode, keyRaw)
	}
	var keyOut struct {
		Success bool `json:"success"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(keyRaw, &keyOut); err != nil {
		return "", err
	}
	if strings.TrimSpace(keyOut.Data.Token) == "" {
		return "", fmt.Errorf("WeKnora 未返回 API Key token")
	}
	return strings.TrimSpace(keyOut.Data.Token), nil
}

func weknoraHealthy(ctx context.Context, baseURL string) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, NormalizeWeKnoraBaseURL(baseURL)+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func ensureManagedWeKnoraContainers(ctx context.Context, network, baseURL string) error {
	if weknoraHealthy(ctx, baseURL) {
		return nil
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("当前运行环境未挂载 docker CLI，请使用 ./start.sh --with-weknora 启动内置实例或改用外部实例模式")
	}
	ver := firstNonEmpty(strings.TrimSpace(os.Getenv("WEKNORA_VERSION")), "latest")
	appPort := firstNonEmpty(strings.TrimSpace(os.Getenv("WEKNORA_APP_PORT")), "18080")
	uiPort := firstNonEmpty(strings.TrimSpace(os.Getenv("WEKNORA_UI_PORT")), "18081")
	uiBind := firstNonEmpty(strings.TrimSpace(os.Getenv("WEKNORA_BIND")), strings.TrimSpace(os.Getenv("LOOPFORGE_BIND")), "0.0.0.0")
	secret := randomHex(16)
	aesKey := randomHex(16) // 32 ASCII hex chars == 32 bytes required by SYSTEM_AES_KEY

	steps := [][]string{
		{"network", "create", network},
		{"run", "-d", "--name", "loopforge-weknora-postgres", "--network", network, "--network-alias", "weknora-postgres",
			"--restart", "unless-stopped",
			"-e", "POSTGRES_USER=weknora", "-e", "POSTGRES_PASSWORD=" + secret, "-e", "POSTGRES_DB=weknora",
			"-v", "loopforge-weknora-pg:/var/lib/postgresql/data",
			"paradedb/paradedb:v0.25.11-pg17"},
		{"run", "-d", "--name", "loopforge-weknora-redis", "--network", network, "--network-alias", "weknora-redis",
			"--restart", "unless-stopped",
			"-v", "loopforge-weknora-redis:/data",
			"redis:7.0-alpine", "redis-server", "--appendonly", "yes", "--requirepass", secret},
		{"run", "-d", "--name", "loopforge-weknora-docreader", "--network", network, "--network-alias", "weknora-docreader",
			"--restart", "unless-stopped",
			"-e", "TZ=Asia/Shanghai", "-e", "DOCREADER_IMAGE_OUTPUT_DIR=/tmp/docreader",
			"-v", "loopforge-weknora-docreader:/tmp/docreader",
			"wechatopenai/weknora-docreader:" + ver},
		{"run", "-d", "--name", "loopforge-weknora-app", "--network", network, "--network-alias", "weknora-app",
			"--restart", "unless-stopped",
			"-p", "127.0.0.1:" + appPort + ":8080",
			"--add-host", "host.docker.internal:host-gateway",
			"-e", "TZ=Asia/Shanghai", "-e", "GIN_MODE=release", "-e", "AUTO_MIGRATE=true",
			"-e", "DB_DRIVER=postgres", "-e", "DB_HOST=weknora-postgres", "-e", "DB_PORT=5432",
			"-e", "DB_USER=weknora", "-e", "DB_PASSWORD=" + secret, "-e", "DB_NAME=weknora",
			"-e", "RETRIEVE_DRIVER=postgres",
			"-e", "DOCREADER_ADDR=weknora-docreader:50051", "-e", "DOCREADER_TRANSPORT=grpc",
			"-e", "STORAGE_TYPE=local", "-e", "LOCAL_STORAGE_BASE_DIR=/data/files",
			"-e", "REDIS_ADDR=weknora-redis:6379", "-e", "REDIS_PASSWORD=" + secret,
			"-e", "SYSTEM_AES_KEY=" + aesKey, "-e", "JWT_SECRET=" + secret,
			"-e", "OLLAMA_OPTIONAL=true",
			"-v", "loopforge-weknora-files:/data/files",
			"-v", "loopforge-weknora-docreader:/tmp/docreader:ro",
			"wechatopenai/weknora-app:" + ver},
		{"run", "-d", "--name", "loopforge-weknora-frontend", "--network", network, "--network-alias", "weknora-frontend",
			"--restart", "unless-stopped",
			"-p", uiBind + ":" + uiPort + ":80",
			"-e", "APP_HOST=weknora-app", "-e", "APP_PORT=8080", "-e", "APP_SCHEME=http",
			"wechatopenai/weknora-ui:" + ver},
	}

	// Ensure containers exist or are started if previously created.
	for i, args := range steps {
		if i == 0 {
			_ = exec.CommandContext(ctx, "docker", args...).Run()
			continue
		}
		name := args[3]
		if err := exec.CommandContext(ctx, "docker", "inspect", name).Run(); err == nil {
			_ = exec.CommandContext(ctx, "docker", "start", name).Run()
			continue
		}
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("启动内置 WeKnora 容器 %s 失败：%s", name, clip(string(out), 200))
		}
	}

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if weknoraHealthy(ctx, baseURL) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("等待内置 WeKnora 服务就绪超时（%s）", baseURL)
}

// WeKnoraConsoleLogin is the sign-in the platform uses for the WeKnora
// console. Only the instance the platform bootstrapped has a password the
// platform knows; an externally linked WeKnora keeps its credentials upstream.
type WeKnoraConsoleLogin struct {
	Configured bool   `json:"configured"`
	Email      string `json:"email,omitempty"`
	Password   string `json:"password,omitempty"`
	Source     string `json:"source,omitempty"`
	UIURL      string `json:"uiUrl,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// ManagedWeKnoraConsole returns the console sign-in for the managed WeKnora
// instance, and nothing for an arbitrary external connection. connID scopes the
// lookup to one connection: an empty or "default" id means the top-level
// settings, so a connection pointed at another host can never borrow the
// managed instance's password.
func (s *Service) ManagedWeKnoraConsole(connID string) WeKnoraConsoleLogin {
	out := WeKnoraConsoleLogin{UIURL: strings.TrimSpace(os.Getenv("LOOPFORGE_WEKNORA_UI_URL"))}
	settings := s.Settings()
	if settings.EffectiveProvider() != domain.KnowledgeProviderWeKnora {
		return out
	}
	baseURL := strings.TrimSpace(os.Getenv("LOOPFORGE_WEKNORA_URL"))
	if baseURL == "" {
		// 没有内置实例配置时，平台不掌握任何控制台密码。
		return out
	}
	connID = strings.TrimSpace(connID)
	var account string
	matched := false
	if connID == "" || connID == "default" {
		matched = sameWeKnoraConsoleURL(settings.URL, baseURL)
	} else {
		for _, conn := range settings.Connections {
			if conn.ID != connID {
				continue
			}
			if sameWeKnoraConsoleURL(conn.URL, baseURL) {
				account, matched = strings.TrimSpace(conn.Account), true
			}
			break
		}
	}
	if !matched {
		return out
	}
	managedEmail := managedWeKnoraConsoleEmail()
	if account == "" {
		account = managedEmail
	}
	out.Email = account
	// 同一个实例可以被多个账号接入，平台只知道内置管理员账号的密码。
	// 其他账号的密码在链接时就已丢弃，不能拿管理员密码去配对。
	if !strings.EqualFold(account, managedEmail) {
		out.Reason = "account_not_managed"
		return out
	}
	if password := strings.TrimSpace(os.Getenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD")); password != "" {
		out.Password, out.Source = password, "environment"
	} else if password := strings.TrimSpace(s.store.ManagedWeKnoraAdminPassword()); password != "" {
		out.Password, out.Source = password, "generated"
	}
	out.Configured = out.Password != ""
	return out
}

// managedWeKnoraConsoleEmail 是内置实例初始化时注册的管理员账号。
func managedWeKnoraConsoleEmail() string {
	if email := strings.TrimSpace(os.Getenv("LOOPFORGE_WEKNORA_ADMIN_EMAIL")); email != "" {
		return email
	}
	return "admin@loopforge.local"
}

func sameWeKnoraConsoleURL(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(a), "/"), strings.TrimRight(strings.TrimSpace(b), "/"))
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

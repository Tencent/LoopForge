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
	"log/slog"
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
	Mode     string `json:"mode"` // "managed" | "external"
	Name     string `json:"name"`
	URL      string `json:"url"`
	Token    string `json:"token"`
	Email    string `json:"email"`
	Password string `json:"password"`
	// SaveConsolePassword keeps the password entered for an external connection
	// so the admin can reveal it again from the console sign-in drawer. Only
	// used with mode=external; managed instances already have a stored password.
	SaveConsolePassword bool `json:"saveConsolePassword,omitempty"`
	WriteEnabled        bool `json:"writeEnabled"`
	OnlyIfFresh         bool `json:"onlyIfFresh,omitempty"`
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
		savedConnID          string
		staleSecretConnID    string
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
				prevAccount := strings.TrimSpace(conns[i].Account)
				conns[i].Token = apiKey
				conns[i].Account = firstNonEmpty(account, conns[i].Account)
				conns[i].Provider = domain.KnowledgeProviderWeKnora
				conns[i].Enabled = true
				// 账号换了（含原连接没记录账号的情况），旧密码不能继续沿用。
				if account != "" && !strings.EqualFold(prevAccount, account) {
					staleSecretConnID = conns[i].ID
				}
				savedConnID = conns[i].ID
				updatedExisting = true
				break
			}
		}
		if !updatedExisting {
			savedConnID = "wk-" + randomHex(4)
			conns = append(conns, domain.KnowledgeConnection{
				ID:       savedConnID,
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
	s.invalidateMetadataCache()
	if migratedLegacyConnID != "" {
		s.migrateLegacySubmissionSources(migratedLegacyConnID)
	}
	// 账号被改写时，旧的密码密文必须先清掉，避免抽屉展示“新账号 + 旧密码”。
	if staleSecretConnID != "" {
		if err := s.store.DeleteKnowledgeConnectionConsoleSecret(staleSecretConnID); err != nil {
			slog.WarnContext(ctx, "stale console secret not deleted",
				"connectionId", staleSecretConnID, "error", err)
		}
	}
	// 建连时输入的密码默认立即丢弃；管理员显式勾选后，只在登录校验通过后
	// 加密保存，供控制台登录抽屉再次查看。保存失败不影响连接本身。
	if input.SaveConsolePassword && mode == "external" && strings.TrimSpace(input.Password) != "" {
		switch {
		case strings.TrimSpace(account) == "":
			slog.WarnContext(ctx, "knowledge console password not saved",
				"connectionId", savedConnID, "reason", "account_missing")
			msg += "；未填写账号，控制台密码未保存"
		default:
			if err := verifyWeKnoraLogin(ctx, targetURL, account, input.Password); err != nil {
				// 上游错误可能回显密码，这里只记录连接 ID。
				slog.WarnContext(ctx, "knowledge console password not verified",
					"connectionId", savedConnID)
				msg += "；账号密码未通过登录校验，控制台密码未保存"
			} else if err := s.saveConnectionConsoleSecret(savedConnID, account, input.Password); err != nil {
				slog.WarnContext(ctx, "knowledge console password not saved",
					"connectionId", savedConnID, "error", err)
				msg += "；控制台密码保存失败"
			}
		}
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
	s.invalidateMetadataCache()
	// 两行密文都要清掉；任一失败不影响另一行，返回第一个错误便于重试（两次
	// 删除都是幂等的）。
	var cleanupErr error
	if err := s.store.DeleteKnowledgeConnectionToken(connID); err != nil {
		cleanupErr = err
	}
	if err := s.store.DeleteKnowledgeConnectionConsoleSecret(connID); err != nil && cleanupErr == nil {
		cleanupErr = err
	}
	if cleanupErr != nil {
		return current, cleanupErr
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

type weKnoraLoginSession struct {
	Token    string
	TenantID uint64
}

// loginWeKnora performs a console/API login. It is shared by API-key bootstrap
// and by the admin "save console password" validation, so both paths accept
// exactly the same credentials.
func loginWeKnora(ctx context.Context, hc *http.Client, baseURL, email, password string) (weKnoraLoginSession, error) {
	loginBody, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/auth/login", bytes.NewReader(loginBody))
	if err != nil {
		return weKnoraLoginSession{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return weKnoraLoginSession{}, err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 上游错误体可能回显提交的密码，任何向外传播的文本都要先脱敏。
		return weKnoraLoginSession{}, fmt.Errorf("%s", redactSecrets(parseWeKnoraError(resp.StatusCode, raw).Error(), password))
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
		return weKnoraLoginSession{}, err
	}
	if !loginOut.Success || strings.TrimSpace(loginOut.Token) == "" {
		msg := redactSecrets(firstNonEmpty(loginOut.Message, "未返回 token"), password)
		return weKnoraLoginSession{}, fmt.Errorf("WeKnora 登录失败：%s", msg)
	}
	out := weKnoraLoginSession{Token: strings.TrimSpace(loginOut.Token)}
	if loginOut.ActiveTenant != nil && loginOut.ActiveTenant.ID > 0 {
		out.TenantID = loginOut.ActiveTenant.ID
	} else if loginOut.User != nil && loginOut.User.TenantID > 0 {
		out.TenantID = loginOut.User.TenantID
	}
	if out.TenantID == 0 {
		return weKnoraLoginSession{}, fmt.Errorf("WeKnora 登录响应缺少 tenant_id")
	}
	return out, nil
}

func verifyWeKnoraLogin(ctx context.Context, baseURL, email, password string) error {
	hc := &http.Client{Timeout: 20 * time.Second}
	_, err := loginWeKnora(ctx, hc, NormalizeWeKnoraBaseURL(baseURL), strings.TrimSpace(email), password)
	return err
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

	session, err := loginWeKnora(ctx, hc, baseURL, email, password)
	if err != nil {
		return "", err
	}

	keyBody, _ := json.Marshal(map[string]any{
		"name":        "loopforge-kb",
		"full_access": true,
	})
	keyReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/api/v1/tenants/%d/api-keys", baseURL, session.TenantID), bytes.NewReader(keyBody))
	if err != nil {
		return "", err
	}
	keyReq.Header.Set("Content-Type", "application/json")
	keyReq.Header.Set("Authorization", "Bearer "+session.Token)
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

// consoleLoginTarget 是控制台凭据所属的配置行：顶层设置（default）或某个连接。
type consoleLoginTarget struct {
	ID      string
	URL     string
	Account string
}

func consoleLoginTargetFor(settings domain.KnowledgeSettings, connID string) (consoleLoginTarget, bool) {
	connID = strings.TrimSpace(connID)
	if connID == "" || connID == "default" {
		if strings.TrimSpace(settings.URL) == "" {
			return consoleLoginTarget{}, false
		}
		return consoleLoginTarget{ID: "default", URL: settings.URL}, true
	}
	for _, conn := range settings.Connections {
		if conn.ID == connID {
			return consoleLoginTarget{ID: conn.ID, URL: conn.URL, Account: conn.Account}, true
		}
	}
	return consoleLoginTarget{}, false
}

// ManagedWeKnoraConsole returns the console sign-in for one connection, or for
// the top-level settings when connID is empty. A password the administrator
// saved for that connection wins; otherwise only the platform-managed instance
// and its built-in administrator account can fall back to the deployment
// password.
func (s *Service) ManagedWeKnoraConsole(connID string) WeKnoraConsoleLogin {
	out := WeKnoraConsoleLogin{UIURL: strings.TrimSpace(os.Getenv("LOOPFORGE_WEKNORA_UI_URL"))}
	settings := s.Settings()
	if settings.EffectiveProvider() != domain.KnowledgeProviderWeKnora {
		return out
	}
	target, ok := consoleLoginTargetFor(settings, connID)
	if !ok {
		return out
	}
	out.Email = strings.TrimSpace(target.Account)

	// 1) 管理员为该连接保存/补录的密码优先：它对同一实例上的其他账号与外部
	// 实例都有效，且不依赖内置实例的环境变量。
	if sealed := s.store.KnowledgeConnectionConsoleSecret(target.ID); sealed != "" {
		if plain, err := openConsoleSecret(target.ID, sealed); err == nil {
			var saved storedConsoleSecret
			if json.Unmarshal([]byte(plain), &saved) == nil && strings.TrimSpace(saved.Password) != "" {
				savedAccount := strings.TrimSpace(saved.Account)
				connAccount := strings.TrimSpace(target.Account)
				// 连接已换成别的账号：旧密文属于旧账号，既不能使用也不能继续保存，
				// 否则免密进入会用错身份登录。
				if savedAccount != "" && connAccount != "" && !strings.EqualFold(savedAccount, connAccount) {
					slog.Warn("knowledge console secret account mismatch", "connectionId", target.ID)
					if derr := s.store.DeleteKnowledgeConnectionConsoleSecret(target.ID); derr != nil {
						slog.Warn("knowledge console secret cleanup failed", "connectionId", target.ID, "error", derr)
					}
				} else {
					out.Email = firstNonEmpty(savedAccount, out.Email)
					out.Password, out.Source, out.Configured = saved.Password, "connection", true
					return out
				}
			}
		} else {
			slog.Warn("knowledge console secret decrypt failed", "connectionId", target.ID, "error", err)
		}
	}

	// 2) 只有内置实例 + 内置管理员账号才回退到平台初始化时掌握的密码。
	baseURL := strings.TrimSpace(os.Getenv("LOOPFORGE_WEKNORA_URL"))
	if baseURL == "" || !sameWeKnoraConsoleURL(target.URL, baseURL) {
		return out
	}
	managedEmail := managedWeKnoraConsoleEmail()
	if out.Email == "" {
		out.Email = managedEmail
	}
	// 同一个实例可以被多个账号接入：不能拿管理员密码去配对别的账号。
	if !strings.EqualFold(out.Email, managedEmail) {
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

// saveConnectionConsoleSecret seals account+password for one connection. It does
// not validate the credentials; SaveWeKnoraConsolePassword does that first.
func (s *Service) saveConnectionConsoleSecret(connID, account, password string) error {
	connID = strings.TrimSpace(connID)
	account = strings.TrimSpace(account)
	// 密码原文可能合法地以空格开头/结尾，只用 TrimSpace 判断是否为空。
	if connID == "" || account == "" || strings.TrimSpace(password) == "" {
		return errors.New("缺少连接、账号或密码")
	}
	raw, err := json.Marshal(storedConsoleSecret{Account: account, Password: password})
	if err != nil {
		return err
	}
	sealed, err := sealConsoleSecret(connID, string(raw))
	if err != nil {
		return err
	}
	return s.store.PutKnowledgeConnectionConsoleSecret(connID, sealed)
}

// SaveWeKnoraConsolePassword verifies a connection's console password by logging
// in to its WeKnora instance, then stores it sealed for the admin-only drawer.
func (s *Service) SaveWeKnoraConsolePassword(ctx context.Context, connID, password string) error {
	if strings.TrimSpace(password) == "" {
		return apperr.New(apperr.Invalid, "请填写该账号的控制台密码")
	}
	settings := s.Settings()
	if settings.EffectiveProvider() != domain.KnowledgeProviderWeKnora {
		return apperr.New(apperr.Invalid, "当前知识库不是 WeKnora，无法保存控制台密码")
	}
	target, ok := consoleLoginTargetFor(settings, connID)
	if !ok {
		return apperr.New(apperr.NotFound, "连接不存在")
	}
	// 只有内置实例的连接才能借用内置管理员账号；外部实例缺账号时不能瞎猜。
	account := strings.TrimSpace(target.Account)
	if account == "" && isManagedConsoleURL(target.URL) {
		account = managedWeKnoraConsoleEmail()
	}
	if account == "" {
		return apperr.New(apperr.Invalid, "该连接没有保存账号，请先补全账号后再保存密码")
	}
	baseURL, err := ValidateOutboundWeKnoraURL(target.URL)
	if err != nil {
		return apperr.New(apperr.Invalid, "连接地址不可用："+err.Error())
	}
	if err := verifyWeKnoraLogin(ctx, baseURL, account, password); err != nil {
		// 上游可能回显提交的密码，错误内容一律不进入响应或日志。
		slog.WarnContext(ctx, "knowledge console password verification failed", "connectionId", target.ID)
		return apperr.New(apperr.Upstream, "WeKnora 登录校验失败，请确认账号与密码")
	}
	// 网络校验期间连接可能被删除或改指向，落库前再确认一次。
	latest, ok := consoleLoginTargetFor(s.Settings(), target.ID)
	if !ok || !sameWeKnoraConsoleURL(latest.URL, target.URL) ||
		!strings.EqualFold(strings.TrimSpace(latest.Account), strings.TrimSpace(target.Account)) {
		return apperr.New(apperr.NotFound, "连接已变更，请刷新后重试")
	}
	if err := s.saveConnectionConsoleSecret(target.ID, account, password); err != nil {
		return apperr.New(apperr.Invalid, "控制台密码保存失败："+err.Error())
	}
	return nil
}

// isManagedConsoleURL reports whether a connection points at the platform-managed
// WeKnora instance, the only case where the built-in admin account may be assumed.
func isManagedConsoleURL(raw string) bool {
	baseURL := strings.TrimSpace(os.Getenv("LOOPFORGE_WEKNORA_URL"))
	return baseURL != "" && sameWeKnoraConsoleURL(raw, baseURL)
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

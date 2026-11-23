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
	"net/url"
	"strings"
	"sync"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

// 同源桥接页的一次性票据：LoopForge 签发票据，WeKnora 前端容器上的静态页
// 拿到票据后向 LoopForge 兑换一次登录态，再写进 :18081 自己的 localStorage。
// 票据只放在 URL fragment 里，不经过 Nginx 访问日志与 Referer。
const (
	weKnoraSSOTicketTTL     = 60 * time.Second
	weKnoraSSOTicketMaxSize = 64
	weKnoraSSORedeemPath    = "/api/knowledge/weknora/console-sso/redeem"
)

// WeKnoraConsoleSSO 是签发结果。Supported=false 时前端应回退到普通外链或
// 引导管理员保存该账号的密码，而不是当成错误。
type WeKnoraConsoleSSO struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	Ticket    string `json:"ticket,omitempty"`
	ExpiresIn int    `json:"expiresIn,omitempty"`
	Email     string `json:"email,omitempty"`
}

// weKnoraConsoleSession 是桥接页唯一依赖的字段集合，登录响应的其余内容
// （租户偏好、配额等）不透传给浏览器。
type weKnoraConsoleSession struct {
	Token        string          `json:"token"`
	RefreshToken string          `json:"refresh_token,omitempty"`
	User         json.RawMessage `json:"user"`
	Tenant       json.RawMessage `json:"tenant,omitempty"`
	ActiveTenant json.RawMessage `json:"active_tenant,omitempty"`
	Memberships  json.RawMessage `json:"memberships,omitempty"`
}

type weKnoraSSOTicket struct {
	payload       json.RawMessage
	consoleOrigin string
	expiresAt     time.Time
}

type weKnoraSSOTicketStore struct {
	mu      sync.Mutex
	tickets map[string]weKnoraSSOTicket
}

var weKnoraSSOTickets = &weKnoraSSOTicketStore{tickets: map[string]weKnoraSSOTicket{}}

func (st *weKnoraSSOTicketStore) issue(payload json.RawMessage, consoleOrigin string) (string, error) {
	id, err := newSSOTicketID()
	if err != nil {
		return "", err
	}
	now := time.Now()
	st.mu.Lock()
	defer st.mu.Unlock()
	for key, ticket := range st.tickets {
		if now.After(ticket.expiresAt) {
			delete(st.tickets, key)
		}
	}
	// 票据最多存活 60 秒，正常情况下不会堆积；上限只防异常调用刷内存。
	if len(st.tickets) >= weKnoraSSOTicketMaxSize {
		var oldestKey string
		var oldest time.Time
		for key, ticket := range st.tickets {
			if oldestKey == "" || ticket.expiresAt.Before(oldest) {
				oldestKey, oldest = key, ticket.expiresAt
			}
		}
		delete(st.tickets, oldestKey)
	}
	st.tickets[id] = weKnoraSSOTicket{payload: payload, consoleOrigin: consoleOrigin, expiresAt: now.Add(weKnoraSSOTicketTTL)}
	return id, nil
}

// redeem 原子取出票据：无论后续校验是否通过都立即作废，避免被重放。
func (st *weKnoraSSOTicketStore) redeem(id, consoleOrigin string) (json.RawMessage, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	ticket, ok := st.tickets[id]
	if !ok {
		return nil, false
	}
	delete(st.tickets, id)
	if time.Now().After(ticket.expiresAt) {
		return nil, false
	}
	if !strings.EqualFold(ticket.consoleOrigin, consoleOrigin) {
		return nil, false
	}
	return ticket.payload, true
}

func newSSOTicketID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// normalizeConsoleOrigin 只接受 scheme://host[:port]：签发时记下的是 WeKnora
// 控制台（:18081）的来源，兑换时必须由浏览器从同一个来源发起，防止票据被
// 别的站点使用。
func normalizeConsoleOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
		strings.TrimSpace(u.Host) == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("控制台来源无效")
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

// IssueWeKnoraConsoleSSO 为管理员签发一张一次性登录票据。只有平台内置的
// WeKnora 实例才有同源桥接页；外部实例与尚未保存密码的账号都返回
// Supported=false 让前端回退。
func (s *Service) IssueWeKnoraConsoleSSO(ctx context.Context, connID, consoleOrigin string) (WeKnoraConsoleSSO, error) {
	origin, err := normalizeConsoleOrigin(consoleOrigin)
	if err != nil {
		return WeKnoraConsoleSSO{}, apperr.New(apperr.Invalid, err.Error())
	}
	settings := s.Settings()
	if settings.EffectiveProvider() != domain.KnowledgeProviderWeKnora {
		return WeKnoraConsoleSSO{Supported: false, Reason: "provider_not_weknora"}, nil
	}
	target, ok := consoleLoginTargetFor(settings, connID)
	if !ok {
		return WeKnoraConsoleSSO{}, apperr.New(apperr.NotFound, "连接不存在")
	}
	// 桥接页挂在内置实例的前端容器上；外部实例没有可用的同源页面。
	if !isManagedConsoleURL(target.URL) {
		return WeKnoraConsoleSSO{Supported: false, Reason: "external_instance"}, nil
	}
	login := s.ManagedWeKnoraConsole(connID)
	if !login.Configured {
		reason := login.Reason
		if reason == "" {
			reason = "password_not_configured"
		}
		return WeKnoraConsoleSSO{Supported: false, Reason: reason, Email: login.Email}, nil
	}
	baseURL, err := ValidateOutboundWeKnoraURL(target.URL)
	if err != nil {
		return WeKnoraConsoleSSO{}, apperr.New(apperr.Invalid, "连接地址不可用："+err.Error())
	}
	payload, err := fetchWeKnoraConsoleSession(ctx, baseURL, login.Email, login.Password)
	if err != nil {
		// 上游错误可能回显密码，fetchWeKnoraConsoleSession 已脱敏；这里只记连接。
		slog.WarnContext(ctx, "knowledge console sso login failed", "connectionId", target.ID, "error", err)
		return WeKnoraConsoleSSO{}, apperr.New(apperr.Upstream, "WeKnora 登录失败，请检查该账号的登录密码")
	}
	ticket, err := weKnoraSSOTickets.issue(payload, origin)
	if err != nil {
		return WeKnoraConsoleSSO{}, apperr.New(apperr.Upstream, "生成一次性登录票据失败")
	}
	return WeKnoraConsoleSSO{
		Supported: true,
		Ticket:    ticket,
		ExpiresIn: int(weKnoraSSOTicketTTL / time.Second),
		Email:     login.Email,
	}, nil
}

// RedeemWeKnoraConsoleSSO 用票据换回登录态。consoleOrigin 取请求的 Origin 头，
// 必须是签发时记录的那个来源。
func RedeemWeKnoraConsoleSSO(ticket, consoleOrigin string) (json.RawMessage, error) {
	origin, err := normalizeConsoleOrigin(consoleOrigin)
	if err != nil {
		return nil, apperr.New(apperr.Forbidden, "登录请求来源无效")
	}
	payload, ok := weKnoraSSOTickets.redeem(strings.TrimSpace(ticket), origin)
	if !ok {
		return nil, apperr.New(apperr.Forbidden, "登录票据已失效，请回到平台重新打开控制台")
	}
	return payload, nil
}

// fetchWeKnoraConsoleSession 登录 WeKnora 并只保留桥接页需要的字段。响应可能
// 直接挂在顶层，也可能包在 data 里，两种形态都要兼容。
func fetchWeKnoraConsoleSession(ctx context.Context, baseURL, email, password string) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, NormalizeWeKnoraBaseURL(baseURL)+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	hc := &http.Client{Timeout: 20 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s", redactSecrets(parseWeKnoraError(resp.StatusCode, raw).Error(), password))
	}

	type loginEnvelope struct {
		Success      bool            `json:"success"`
		Data         json.RawMessage `json:"data"`
		Token        string          `json:"token"`
		RefreshToken string          `json:"refresh_token"`
		User         json.RawMessage `json:"user"`
		Tenant       json.RawMessage `json:"tenant"`
		ActiveTenant json.RawMessage `json:"active_tenant"`
		Memberships  json.RawMessage `json:"memberships"`
	}
	var env loginEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	if env.Token == "" && len(env.Data) > 0 {
		var inner loginEnvelope
		if err := json.Unmarshal(env.Data, &inner); err == nil {
			env = inner
		}
	}
	if strings.TrimSpace(env.Token) == "" || len(env.User) == 0 || string(env.User) == "null" {
		return nil, errors.New("WeKnora 登录响应缺少 token 或 user")
	}
	out, err := json.Marshal(weKnoraConsoleSession{
		Token:        strings.TrimSpace(env.Token),
		RefreshToken: strings.TrimSpace(env.RefreshToken),
		User:         env.User,
		Tenant:       env.Tenant,
		ActiveTenant: env.ActiveTenant,
		Memberships:  env.Memberships,
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

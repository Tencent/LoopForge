package notification

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"
)

type Provider interface {
	Sessions(context.Context, string, string) ([]domain.BotChat, error)
	Send(context.Context, string, string, string, string) error
}
type ProviderError struct {
	Code    int
	Message string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("企业微信错误 %d：%s", e.Code, e.Message)
}
func failure(code int) *ProviderError {
	message := "请求失败，请检查机器人授权或稍后重试"
	switch code {
	case 850003:
		message = "消息使用权限已过期，请机器人创建者重新授权"
	case 853004:
		message = "机器人凭据或访问令牌失效，请重新验证"
	}
	return &ProviderError{Code: code, Message: message}
}

// CLIProvider keeps service discovery and message encoding inside the official CLI.
// Only the signed bootstrap is implemented here to support platform-side credential setup without a TTY.
type CLIProvider struct {
	HTTP    *http.Client
	Binary  string
	authURL string
}

func NewCLIProvider() *CLIProvider {
	return &CLIProvider{HTTP: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Binary: "wecom-cli", authURL: "https://qyapi.weixin.qq.com/cgi-bin/aibot/cli/get_cli_config"}
}
func (p *CLIProvider) token(ctx context.Context, botID, secret string) (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	nonce := "cli_" + hex.EncodeToString(random)
	timestamp := time.Now().Unix()
	digest := sha256.Sum256([]byte(secret + botID + strconv.FormatInt(timestamp, 10) + nonce))
	body, _ := json.Marshal(map[string]any{"bot_id": botID, "time": timestamp, "nonce": nonce, "signature": hex.EncodeToString(digest[:]), "bind_source": 1})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.authURL, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("企业微信鉴权请求创建失败")
	}
	req.Header.Set("Content-Type", "application/json")
	rsp, err := p.HTTP.Do(req)
	if err != nil {
		return "", errors.New("企业微信鉴权网络请求失败")
	}
	defer rsp.Body.Close()
	if rsp.StatusCode != http.StatusOK {
		return "", errors.New("企业微信鉴权服务暂不可用")
	}
	var result struct {
		Code  int    `json:"errcode"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(rsp.Body, 1<<20)).Decode(&result); err != nil {
		return "", errors.New("企业微信鉴权响应格式异常")
	}
	if result.Code != 0 {
		return "", failure(result.Code)
	}
	if result.Token == "" {
		return "", errors.New("企业微信鉴权未返回令牌")
	}
	return result.Token, nil
}
func (p *CLIProvider) call(ctx context.Context, botID, secret string, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "agentflow-wecom-")
	if err != nil {
		return nil, errors.New("通知临时目录不可用")
	}
	defer os.RemoveAll(dir)
	for attempt := 0; attempt < 2; attempt++ {
		token, err := p.token(ctx, botID, secret)
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, p.Binary, args...)
		for _, name := range []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
			if value, ok := os.LookupEnv(name); ok {
				cmd.Env = append(cmd.Env, name+"="+value)
			}
		}
		cmd.Env = append(cmd.Env, "WECOM_CLI_ACCESS_TOKEN="+token, "WECOM_CLI_CONFIG_DIR="+dir)
		var out boundedBuffer
		cmd.Stdout = &out
		cmd.Stderr = io.Discard
		err = cmd.Run()
		if ctx.Err() != nil {
			return nil, errors.New("企业微信通知请求超时")
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errors.New("通知服务未安装 wecom-cli，请联系管理员")
		}
		var result struct {
			Code  int `json:"errcode"`
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(out.Bytes(), &result) != nil {
			return nil, errors.New("企业微信 CLI 响应格式异常")
		}
		if result.Error != nil {
			result.Code = result.Error.Code
		}
		if result.Code == 853004 && attempt == 0 {
			continue
		}
		if result.Code != 0 {
			return nil, failure(result.Code)
		}
		if err != nil {
			return nil, errors.New("企业微信 CLI 调用失败")
		}
		return append(json.RawMessage(nil), out.Bytes()...), nil
	}
	return nil, failure(853004)
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 2<<20 {
		return 0, errors.New("notification response too large")
	}
	return b.Buffer.Write(p)
}
func (p *CLIProvider) Sessions(ctx context.Context, botID, secret string) ([]domain.BotChat, error) {
	raw, err := p.call(ctx, botID, secret, "message", "aibot", "sessions", "list")
	if err != nil {
		return nil, err
	}
	var rsp struct {
		Sessions []struct {
			ID   string `json:"chat_id"`
			Name string `json:"chat_name"`
			Type string `json:"chat_type"`
			Last string `json:"last_msg_time"`
		} `json:"sessions"`
	}
	if err = json.Unmarshal(raw, &rsp); err != nil {
		return nil, errors.New("企业微信会话列表格式异常")
	}
	chats := make([]domain.BotChat, 0, len(rsp.Sessions))
	for _, s := range rsp.Sessions {
		if s.ID != "" && (s.Type == "single" || s.Type == "group") {
			chats = append(chats, domain.BotChat{ID: s.ID, Name: s.Name, Type: s.Type, LastMessageTime: s.Last})
		}
	}
	return chats, nil
}
func (p *CLIProvider) Send(ctx context.Context, botID, secret, target, content string) error {
	if len([]byte(content)) == 0 || len([]byte(content)) > 20480 {
		return errors.New("通知正文必须为 1 至 20480 字节")
	}
	body, _ := json.Marshal(map[string]any{"chat_id": target, "msg_type": "markdown", "markdown": map[string]string{"content": content}})
	raw, err := p.call(ctx, botID, secret, "message", "aibot", "send", "--json", string(body))
	if err != nil {
		return err
	}
	var result struct {
		Success bool `json:"success"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.Success {
		return errors.New("企业微信未确认发送成功")
	}
	return nil
}

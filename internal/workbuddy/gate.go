package workbuddy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"agentflow/internal/gatemcp"
)

const gateURLEnv = "AGENTFLOW_WORKBUDDY_GATE_URL"
const gateControlTokenEnv = "AGENTFLOW_GATE_MCP_CONTROL_TOKEN"

type GateClient struct {
	endpoint string
	token    string
}

func NewGateClient() (*GateClient, error) {
	raw := strings.TrimSpace(os.Getenv(gateURLEnv))
	token := strings.TrimSpace(os.Getenv(gateControlTokenEnv))
	if raw == "" || token == "" {
		return nil, fmt.Errorf("WorkBuddy 节点需要配置 Gate MCP（%s，或 %s 和 %s）", gatePublicKeyEnv, gateURLEnv, gateControlTokenEnv)
	}
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("%s 地址无效", gateURLEnv)
	}
	if endpoint.Scheme != "https" && (endpoint.Scheme != "http" || !net.ParseIP(endpoint.Hostname()).IsLoopback()) {
		return nil, fmt.Errorf("%s 需要 HTTPS 地址或本机 HTTP 地址", gateURLEnv)
	}
	return &GateClient{endpoint: endpoint.JoinPath(gatemcp.AttemptPath).String(), token: token}, nil
}

func (g *GateClient) Open(ctx context.Context) (string, error) {
	status, raw, err := g.request(ctx, http.MethodPost, "")
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("Gate MCP 无法登记本次执行（HTTP %d）", status)
	}
	var response struct {
		Ticket string `json:"ticket"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Ticket == "" || len(response.Ticket) > 128 || strings.Trim(response.Ticket, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") != "" {
		return "", errors.New("Gate MCP 返回了无效的提交票据")
	}
	return response.Ticket, nil
}

func (g *GateClient) Result(ctx context.Context, ticket string) ([]byte, error) {
	status, raw, err := g.request(ctx, http.MethodGet, ticket)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		return raw, nil
	case http.StatusNoContent:
		return nil, errors.New("本次执行未收到 submit_gate_result，请检查 WorkBuddy Agent 版本是否已绑定 LoopForge Gate 连接器")
	default:
		return nil, fmt.Errorf("无法读取本次 Gate MCP 终态（HTTP %d）", status)
	}
}

func (g *GateClient) Discard(ctx context.Context, ticket string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _, _ = g.request(ctx, http.MethodDelete, ticket)
}

func (g *GateClient) request(ctx context.Context, method, ticket string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, g.endpoint, nil)
	if err != nil {
		return 0, nil, errors.New("无法构造 Gate MCP 请求")
	}
	request.Header.Set("Authorization", "Bearer "+g.token)
	if ticket != "" {
		request.Header.Set(gatemcp.TicketHeader, ticket)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return 0, nil, errors.New("Gate MCP 连接失败")
	}
	defer response.Body.Close()
	const responseLimit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil || len(raw) > responseLimit {
		return 0, nil, errors.New("Gate MCP 响应无效")
	}
	return response.StatusCode, raw, nil
}

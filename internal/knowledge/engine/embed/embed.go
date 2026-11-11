// Package embed 是向量检索的 embedding 抽象。三种来源：
//   - 不启用（Disabled）：平台不配置时完全不发起请求，行为等同纯关键词检索。
//   - 本地 sidecar（Sidecar，BGE-M3 CPU）：自定义只读协议
//     {"model","input":[...],"task":"query|document"} → {"embeddings":[[...]]}。
//   - OpenAI 兼容远程接口（OpenAI）：POST /v1/embeddings，带
//     Authorization: Bearer 鉴权头，响应 {"data":[{"embedding":[...]}]}。
//
// 相比移植源，这里放宽了 sidecar 特有的健康字段校验：远程接口没有
// /healthz，也不返回 revision / artifact_sha256 / max_length / ready 这些
// 字段，所以 Probe 对远程只做一次真实推理，对 sidecar 的 /healthz 也只要求
// HTTP 200、不解析附属字段；向量本身只校验维度、有限与非零，不强制 L2 归一。
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Kind 选择 embedding 来源。
type Kind string

const (
	Disabled Kind = "disabled"
	Sidecar  Kind = "sidecar"
	OpenAI   Kind = "openai"
)

// Provider 生成文本向量。isQuery=true 表示查询向量（sidecar 用它选 task）。
type Provider interface {
	Name() string
	Available() bool
	Dim() int
	Embed(ctx context.Context, text string, isQuery bool) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string, isQuery bool) ([][]float32, error)
	Probe(ctx context.Context) error
}

// Config 是 embedding 的来源配置，由平台环境变量装配。
type Config struct {
	Kind      Kind
	URL       string
	APIKey    string
	Model     string
	Dim       int
	MaxLength int
	Timeout   time.Duration
	Client    *http.Client
}

// NewProvider 按 Kind 构造 Provider。Disabled 返回永远不可用的实现。
func NewProvider(cfg Config) Provider {
	if cfg.Kind == Disabled || strings.TrimSpace(cfg.URL) == "" || cfg.Dim <= 0 {
		return &disabledProvider{}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		// 模型冷启动（ONNX 预热）或远程排队可能明显慢于一次普通请求。
		timeout = 3 * time.Minute
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &HTTPProvider{
		kind: cfg.Kind, url: strings.TrimSpace(cfg.URL), apiKey: cfg.APIKey,
		model: cfg.Model, dim: cfg.Dim, maxLength: cfg.MaxLength, client: client,
	}
}

type disabledProvider struct{}

func (d *disabledProvider) Name() string      { return "disabled" }
func (d *disabledProvider) Available() bool   { return false }
func (d *disabledProvider) Dim() int          { return 0 }
func (d *disabledProvider) Probe(context.Context) error {
	return errors.New("embedding 未启用")
}
func (d *disabledProvider) Embed(context.Context, string, bool) ([]float32, error) {
	return nil, errors.New("embedding 未启用")
}
func (d *disabledProvider) EmbedBatch(context.Context, []string, bool) ([][]float32, error) {
	return nil, errors.New("embedding 未启用")
}

// HTTPProvider 用单一 HTTP 端点同时对接 sidecar 与 OpenAI 兼容接口。
type HTTPProvider struct {
	kind      Kind
	url       string
	apiKey    string
	model     string
	dim       int
	maxLength int
	client    *http.Client
}

func (p *HTTPProvider) Name() string {
	switch p.kind {
	case OpenAI:
		return "openai/" + p.model
	default:
		return "sidecar/" + p.model
	}
}

func (p *HTTPProvider) Available() bool { return strings.TrimSpace(p.url) != "" && p.dim > 0 }
func (p *HTTPProvider) Dim() int        { return p.dim }

func (p *HTTPProvider) Embed(ctx context.Context, text string, isQuery bool) ([]float32, error) {
	vectors, err := p.EmbedBatch(ctx, []string{text}, isQuery)
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, errors.New("embedding 返回向量数量错误")
	}
	return vectors[0], nil
}

// EmbedBatch 一次请求多个输入。sidecar 发 task 字段；OpenAI 兼容接口发
// Authorization: Bearer 鉴权头，不带 task。响应同时兼容
// {"embeddings":[[...]]} 与 {"data":[{"embedding":[...]}]}。
func (p *HTTPProvider) EmbedBatch(ctx context.Context, texts []string, isQuery bool) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	if !p.Available() {
		return nil, errors.New("embedding 未配置 URL 或维度")
	}
	body := map[string]any{"model": p.model, "input": texts}
	if p.kind == Sidecar {
		task := "document"
		if isQuery {
			task = "query"
		}
		body["task"] = task
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(p.apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding 服务不可用: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding 服务 HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Embeddings [][]float32 `json:"embeddings"`
		Data       []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("embedding 响应解析失败: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("embedding 服务错误: %s", parsed.Error.Message)
	}
	vectors := parsed.Embeddings
	if len(vectors) == 0 && len(parsed.Data) > 0 {
		vectors = make([][]float32, 0, len(parsed.Data))
		for _, item := range parsed.Data {
			vectors = append(vectors, item.Embedding)
		}
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("embedding 返回 %d 个向量，期望 %d", len(vectors), len(texts))
	}
	for i, v := range vectors {
		if len(v) == 0 {
			return nil, fmt.Errorf("embedding 第 %d 个向量为空", i)
		}
		if p.dim > 0 && len(v) != p.dim {
			return nil, fmt.Errorf("embedding 维度 %d 与配置 %d 不一致", len(v), p.dim)
		}
		normSquared := 0.0
		for _, x := range v {
			f := float64(x)
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, fmt.Errorf("embedding 第 %d 个向量包含非有限值", i)
			}
			normSquared += f * f
		}
		if normSquared <= 1e-24 {
			return nil, fmt.Errorf("embedding 第 %d 个向量为零向量", i)
		}
	}
	return vectors, nil
}

// Probe 做一次真实推理，确保模型/分词器可用，而不只是端口在听。
//
// 远程（OpenAI 兼容）没有 /healthz，直接推理；本地 sidecar 的 /healthz 只
// 要求 HTTP 200，刻意不解析 ready / revision / artifact_sha256 / max_length
// 这些 sidecar 特有字段——接入远程接口时必须放宽这些校验。
func (p *HTTPProvider) Probe(ctx context.Context) error {
	if !p.Available() {
		return errors.New("embedding 未配置")
	}
	if p.kind == Sidecar {
		base := strings.TrimSuffix(strings.TrimSuffix(p.url, "/"), "/embed")
		base = strings.TrimSuffix(base, "/v1/embeddings")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		if err == nil {
			if resp, err := p.client.Do(req); err == nil {
				io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
				if resp.StatusCode >= 300 {
					return fmt.Errorf("embedding readiness HTTP %d", resp.StatusCode)
				}
			}
			// /healthz 不可达不直接判死：以真实推理为准。
		}
	}
	_, err := p.Embed(ctx, "knowledge-base readiness probe", true)
	return err
}

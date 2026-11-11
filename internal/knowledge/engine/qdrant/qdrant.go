// Package qdrant 是 Qdrant 的最小 REST 客户端：集合管理、按来源/文档删除、
// 批量写入、向量检索，以及换模型时删集合重建。任何网络/服务错误都由上层
// 视为「向量不可用」并降级为关键词检索。
package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Point struct {
	ID      uint64         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload"`
}

type ScoredPoint struct {
	ID      uint64         `json:"id"`
	Score   float64        `json:"score"`
	Payload map[string]any `json:"payload"`
}

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

const maxPointsPerUpsert = 256

func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:  apiKey,
		// 一次全量重建会推送成千上万个 1024 维点，CPU 版 Qdrant 比普通查询慢。
		HTTP: &http.Client{Timeout: 10 * time.Minute},
	}
}

func (c *Client) Enabled() bool { return c.BaseURL != "" }

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("api-key", c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("qdrant %s %s → HTTP %d: %s", method, path, resp.StatusCode, string(data[:min(300, len(data))]))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/healthz", nil, nil)
}

// CollectionInfo 是集合是否存在与其向量维度的探测结果。
type CollectionInfo struct {
	Exists bool
	Dim    int
}

// CollectionInfo 读集合配置，返回是否存在及现有维度。
func (c *Client) CollectionInfo(ctx context.Context, name string) (CollectionInfo, error) {
	var probe struct {
		Result struct {
			Config struct {
				Params struct {
					Vectors struct {
						Size int `json:"size"`
					} `json:"vectors"`
				} `json:"params"`
			} `json:"config"`
		} `json:"result"`
	}
	if err := c.do(ctx, http.MethodGet, "/collections/"+name, nil, &probe); err != nil {
		return CollectionInfo{}, nil // 不存在或不可达：交给上层决定降级
	}
	dim := probe.Result.Config.Params.Vectors.Size
	return CollectionInfo{Exists: true, Dim: dim}, nil
}

// EnsureCollection 创建集合（已存在且维度一致则直接返回；维度不一致报错，
// 提示上层需要换 collection 并全量重建）。
func (c *Client) EnsureCollection(ctx context.Context, name string, dim int) error {
	info, err := c.CollectionInfo(ctx, name)
	if err != nil {
		return err
	}
	if info.Exists {
		if info.Dim != 0 && info.Dim != dim {
			return fmt.Errorf("集合 %s 现有维度 %d 与期望 %d 不一致（需换 collection 并重建）", name, info.Dim, dim)
		}
		return nil
	}
	body := map[string]any{"vectors": map[string]any{"size": dim, "distance": "Cosine"}}
	return c.do(ctx, http.MethodPut, "/collections/"+name+"?timeout=30", body, nil)
}

// DeleteCollection 删除整个集合，用于换模型后的全量重建。
func (c *Client) DeleteCollection(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/collections/"+name+"?timeout=30", nil, nil)
}

// Upsert 批量写入点，按上限拆请求，避免单次超过 Qdrant 默认 32 MiB 限制。
func (c *Client) Upsert(ctx context.Context, name string, points []Point) error {
	for start := 0; start < len(points); start += maxPointsPerUpsert {
		end := start + maxPointsPerUpsert
		if end > len(points) {
			end = len(points)
		}
		if err := c.do(ctx, http.MethodPut, "/collections/"+name+"/points?wait=true",
			map[string]any{"points": points[start:end]}, nil); err != nil {
			return err
		}
	}
	return nil
}

// DeleteByFilter 按 payload 里的 source_id（可选加 path）删除点。
// 增量更新时先删旧向量再写新向量，避免块重排后旧点残留。
func (c *Client) DeleteByFilter(ctx context.Context, name, sourceID, path string) error {
	must := []map[string]any{
		{"key": "source_id", "match": map[string]any{"value": sourceID}},
	}
	if path != "" {
		must = append(must, map[string]any{"key": "path", "match": map[string]any{"value": path}})
	}
	return c.do(ctx, http.MethodPost, "/collections/"+name+"/points/delete?wait=true",
		map[string]any{"filter": map[string]any{"must": must}}, nil)
}

// Search 向量检索；sources 非空时按来源过滤。
func (c *Client) Search(ctx context.Context, name string, vector []float32, sources []string, limit int) ([]ScoredPoint, error) {
	body := map[string]any{
		"vector": vector, "limit": limit, "with_payload": true,
	}
	if len(sources) > 0 {
		should := make([]any, 0, len(sources))
		for _, s := range sources {
			should = append(should, map[string]any{"key": "source_id", "match": map[string]any{"value": s}})
		}
		body["filter"] = map[string]any{"should": should}
	}
	var out struct {
		Result []ScoredPoint `json:"result"`
	}
	if err := c.do(ctx, http.MethodPost, "/collections/"+name+"/points/search", body, &out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

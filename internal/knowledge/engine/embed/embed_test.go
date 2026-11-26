package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func normalized(dim int) []float32 {
	v := make([]float32, dim)
	v[0] = 1
	return v
}

func TestDisabledProviderIsUnavailable(t *testing.T) {
	p := NewProvider(Config{Kind: Disabled, Dim: 1024})
	if p.Available() {
		t.Fatal("Disabled 来源不应可用")
	}
	if _, err := p.Embed(context.Background(), "x", true); err == nil {
		t.Fatal("Disabled 来源调用应报错")
	}
	// 没给 URL / 维度也视为不启用。
	if NewProvider(Config{Kind: Sidecar}).Available() {
		t.Fatal("缺 URL 不应可用")
	}
	if NewProvider(Config{Kind: OpenAI, URL: "http://x", Dim: 0}).Available() {
		t.Fatal("维度为 0 不应可用")
	}
}

func TestSidecarProtocol(t *testing.T) {
	var gotPath, gotTask, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
			Task  string   `json:"task"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotTask = body.Task
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embeddings": [][]float32{normalized(4), normalized(4)},
			"model":      body.Model,
			"dim":        4,
		})
	}))
	defer srv.Close()

	p := NewProvider(Config{Kind: Sidecar, URL: srv.URL + "/embed", Model: "bge-m3", Dim: 4})
	vecs, err := p.EmbedBatch(context.Background(), []string{"查询文本", "文档正文"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 4 {
		t.Fatalf("sidecar 响应解析错误: %+v", vecs)
	}
	if gotTask != "query" {
		t.Fatalf("查询向量应带 task=query: %q", gotTask)
	}
	if gotAuth != "" {
		t.Fatalf("sidecar 未配 APIKey 时不应带鉴权头: %q", gotAuth)
	}
	_ = gotPath
}

func TestOpenAIProtocolSendsAuthHeader(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"index": 0, "embedding": normalized(4)},
				{"index": 1, "embedding": normalized(4)},
			},
			"model": "text-embedding-3-small",
		})
	}))
	defer srv.Close()

	p := NewProvider(Config{
		Kind: OpenAI, URL: srv.URL + "/v1/embeddings", APIKey: "sk-secret",
		Model: "text-embedding-3-small", Dim: 4,
	})
	vecs, err := p.EmbedBatch(context.Background(), []string{"a", "b"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 {
		t.Fatalf("OpenAI 响应 data[].embedding 解析错误: %+v", vecs)
	}
	if gotAuth != "Bearer sk-secret" {
		t.Fatalf("远程接口必须带 Authorization Bearer 鉴权头: %q", gotAuth)
	}
	if _, ok := gotBody["task"]; ok {
		t.Fatal("OpenAI 兼容接口不应发送 sidecar 特有的 task 字段")
	}
	if gotBody["model"] != "text-embedding-3-small" {
		t.Fatalf("model 字段错误: %v", gotBody["model"])
	}
}

func TestEmbeddingValidatesDimAndFiniteness(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embeddings": [][]float32{{1, 2, 3}}, // 维度错误
		})
	}))
	defer srv.Close()
	p := NewProvider(Config{Kind: Sidecar, URL: srv.URL + "/embed", Dim: 4})
	if _, err := p.Embed(context.Background(), "x", true); err == nil {
		t.Fatal("维度不一致应报错")
	}
}

// 放宽 sidecar 特有健康字段校验：远程没有 /healthz，Probe 只做一次真实
// 推理；sidecar 的 /healthz 也只要求 HTTP 200，不解析 ready/revision/
// artifact_sha256/max_length 等字段。
func TestProbeRelaxesHealthFieldValidation(t *testing.T) {
	t.Run("openai no healthz", func(t *testing.T) {
		var hitsHealthz bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/healthz") {
				hitsHealthz = true
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"embedding": normalized(4)}},
			})
		}))
		defer srv.Close()
		p := NewProvider(Config{Kind: OpenAI, URL: srv.URL + "/v1/embeddings", APIKey: "k", Model: "m", Dim: 4})
		if err := p.Probe(context.Background()); err != nil {
			t.Fatalf("远程 Probe 应只做真实推理、不依赖 /healthz: %v", err)
		}
		if hitsHealthz {
			t.Fatal("远程接口不应请求 sidecar 特有的 /healthz")
		}
	})

	t.Run("sidecar minimal healthz", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/healthz") {
				// 只给 ready，不返回 revision/artifact_sha256/max_length/dim。
				_ = json.NewEncoder(w).Encode(map[string]any{"ready": true})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"embeddings": [][]float32{normalized(4)},
			})
		}))
		defer srv.Close()
		p := NewProvider(Config{Kind: Sidecar, URL: srv.URL + "/embed", Model: "m", Dim: 4})
		if err := p.Probe(context.Background()); err != nil {
			t.Fatalf("sidecar 健康字段应放宽，仅凭 HTTP 200 + 真实推理即可: %v", err)
		}
	})
}

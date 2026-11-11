package engine

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"agentflow/internal/knowledge/engine/embed"
)

// VectorConfig 是向量检索的部署级配置，来自 AGENTFLOW_KB_* 环境变量。
// 它由进程启动时装配一次；某个知识库是否启用向量、以及它的索引契约快照
// 存在 knowledge_bases 行上（见 domain.KnowledgeBase）。没有配置 Qdrant 或
// embedding 时 Enabled() 为 false，引擎完全不调用向量通道。
type VectorConfig struct {
	QdrantURL       string
	QdrantAPIKey    string
	Collection      string
	EmbeddingKind   embed.Kind
	EmbeddingURL    string
	EmbeddingAPIKey string
	Model           string
	Dim             int
	MaxLength       int
}

// VectorConfigFromEnv 读环境变量。三种来源通过
// AGENTFLOW_KB_EMBEDDING_PROVIDER 选择：sidecar（本地 BGE-M3 CPU）、
// openai（OpenAI 兼容远程接口，带鉴权头）、未配置/其它=不启用。
func VectorConfigFromEnv() VectorConfig {
	kind := embed.Kind(strings.ToLower(strings.TrimSpace(os.Getenv("AGENTFLOW_KB_EMBEDDING_PROVIDER"))))
	url := strings.TrimSpace(os.Getenv("AGENTFLOW_KB_EMBEDDING_URL"))
	switch kind {
	case embed.Sidecar, embed.OpenAI:
		// 显式指定
	case "":
		// 兼容：给了 embedding URL 就默认按 sidecar 处理。
		if url != "" {
			kind = embed.Sidecar
		} else {
			kind = embed.Disabled
		}
	default:
		kind = embed.Disabled
	}
	dim, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("AGENTFLOW_KB_EMBEDDING_DIM")))
	if dim <= 0 {
		dim = 0
	}
	maxLength, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("AGENTFLOW_KB_EMBEDDING_MAX_LENGTH")))
	model := strings.TrimSpace(os.Getenv("AGENTFLOW_KB_EMBEDDING_MODEL"))

	qdrantURL := strings.TrimSpace(os.Getenv("AGENTFLOW_KB_QDRANT_URL"))
	collection := strings.TrimSpace(os.Getenv("AGENTFLOW_KB_QDRANT_COLLECTION"))
	if collection == "" && model != "" && dim > 0 {
		collection = CollectionName(model, dim)
	}

	return VectorConfig{
		QdrantURL:       qdrantURL,
		QdrantAPIKey:    os.Getenv("AGENTFLOW_KB_QDRANT_API_KEY"),
		Collection:      collection,
		EmbeddingKind:   kind,
		EmbeddingURL:    url,
		EmbeddingAPIKey: os.Getenv("AGENTFLOW_KB_EMBEDDING_API_KEY"),
		Model:           model,
		Dim:             dim,
		MaxLength:       maxLength,
	}
}

// Enabled 表示「部署层面」是否具备向量检索能力。某个具体知识库是否启用，
// 还要看该行 VectorEnabled 与 collection 是否与本配置一致。
func (c VectorConfig) Enabled() bool {
	return strings.TrimSpace(c.QdrantURL) != "" &&
		strings.TrimSpace(c.EmbeddingURL) != "" &&
		c.EmbeddingKind != embed.Disabled &&
		c.Dim > 0 && strings.TrimSpace(c.Collection) != ""
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// CollectionName 从模型与维度派生 collection 名。模型或维度一变，名字必然
// 变，从机制上保证「换模型换 collection 并全量重建」，不会把不同模型的
// 向量混进同一集合。知识库行上存的就是这个名字的快照。
func CollectionName(model string, dim int) string {
	m := strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(strings.TrimSpace(model)), "_"), "_")
	if m == "" {
		m = "model"
	}
	return fmt.Sprintf("kb_%s_%d", m, dim)
}

// 进程内全局向量运行时。默认是一个禁用配置，未调用 ConfigureVector 时所有
// 引擎都走纯关键词检索，行为与阶段一完全一致。
var (
	vectorMu     sync.RWMutex
	globalVector = &VectorRuntime{cfg: VectorConfig{}}
)

// ConfigureVector 在进程启动时装配全局向量运行时。content/control 通常是
// *repo.Store。禁用配置也安全：运行时 Active() 返回 false。
func ConfigureVector(cfg VectorConfig, content VectorContentStore, control VectorControlStore) {
	rt := newVectorRuntime(cfg, content, control)
	vectorMu.Lock()
	globalVector = rt
	vectorMu.Unlock()
}

// ConfigureVectorFromEnv 读环境变量并装配；在 repo.Open 之后、构造任何引擎
// 之前调用一次。所有角色（含纯 api 实例）都要调用，检索才会带上向量通道。
func ConfigureVectorFromEnv(content VectorContentStore, control VectorControlStore) {
	ConfigureVector(VectorConfigFromEnv(), content, control)
}

func vectorRuntime() *VectorRuntime {
	vectorMu.RLock()
	defer vectorMu.RUnlock()
	return globalVector
}

// CurrentVectorConfig 暴露当前部署级向量配置，供管理接口回显与启用校验。
func CurrentVectorConfig() VectorConfig { return vectorRuntime().cfg }

// VectorStatus 给管理接口/自检用：部署是否具备能力、模型/维度/collection。
type VectorStatus struct {
	Available  bool   `json:"available"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	Dim        int    `json:"dim,omitempty"`
	Collection string `json:"collection,omitempty"`
}

func CurrentVectorStatus() VectorStatus {
	c := vectorRuntime().cfg
	return VectorStatus{
		Available: c.Enabled(), Provider: string(c.EmbeddingKind),
		Model: c.Model, Dim: c.Dim, Collection: c.Collection,
	}
}

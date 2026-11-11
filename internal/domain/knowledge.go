package domain

import (
	"time"
)

// KnowledgeMCPServerID is the reserved id of the read-only knowledge base MCP
// that the engine materializes for roles with AgentDefinition.KnowledgeBase.
// Like PlatformMCPServerID it is never a row in the mcp_servers table.
const KnowledgeMCPServerID = "agentflow-knowledge"

// KnowledgeUsageRuleID is the built-in rule injected alongside the knowledge
// MCP so every opted-in role searches before answering and cites sources.
const KnowledgeUsageRuleID = "agentflow-knowledge-usage"

// KnowledgeArtifactBasename is the conventional deliverable name a run writes
// when it has knowledge worth distilling back into the knowledge base.
const KnowledgeArtifactBasename = "knowledge.md"

const (
	KnowledgeSubmissionPending   = "pending"
	KnowledgeSubmissionPublished = "published"
	KnowledgeSubmissionRejected  = "rejected"
	KnowledgeSubmissionRetracted = "retracted"
)

// 自动沉淀的发布模式，配置在知识库上。默认人工审核；auto 只在门禁全过时
// 自动走已有的写回路径。
const (
	KnowledgeSedimentReview = "review"
	KnowledgeSedimentAuto   = "auto"
)

// 触发自动沉淀的闭环事件。LoopForge 运行、合并请求、需求和人工关卡的实际
// 事件各对应一个取值；多个事件同时成立时全部记录。
const (
	KnowledgeSignalRunSucceeded   = "run_succeeded"
	KnowledgeSignalMRMerged       = "mr_merged"
	KnowledgeSignalIssueCompleted = "issue_completed"
	KnowledgeSignalGateApproved   = "gate_approved"
)

// KnowledgeGateRule 是敏感内容过滤的可配置规则。Pattern 是 Go 正则；
// 内置规则在没配置时生效，配置后以知识库上的规则集为准。
type KnowledgeGateRule struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
	Enabled bool   `json:"enabled"`
}

// KnowledgeGateCheck 是一项门禁结果，逐项保存在审核条目上供审核页展示。
type KnowledgeGateCheck struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// KnowledgeTombstone 记录一次撤回。同项目内相同内容指纹再次沉淀时被门禁
// 拦截；墓碑只存指纹和出处元数据，不保存正文。
type KnowledgeTombstone struct {
	ProjectID    string    `json:"projectId"`
	Fingerprint  string    `json:"fingerprint"`
	BaseID       string    `json:"baseId,omitempty"`
	SourceID     string    `json:"sourceId,omitempty"`
	Path         string    `json:"path,omitempty"`
	SubmissionID string    `json:"submissionId,omitempty"`
	Title        string    `json:"title,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	CreatedBy    string    `json:"createdBy,omitempty"`
	CreatedAt    time.Time `json:"createdAt,omitempty"`
}

// Git 写回的生命周期。审核条目发布成功即进入 published；写回记录另行
// 跟踪分支与合并请求，合并后由同步器把新文档纳入索引。
const (
	KnowledgeWritebackPending      = "pending"
	KnowledgeWritebackBranchPushed = "branch_pushed"
	KnowledgeWritebackMROpen       = "mr_open"
	KnowledgeWritebackMerged       = "merged"
	KnowledgeWritebackClosed       = "closed"
	KnowledgeWritebackFailed       = "failed"
)

// KnowledgeSubmission is one distilled document waiting for human review
// before it is written back to an upstream knowledge source.
type KnowledgeSubmission struct {
	ID         string `json:"id"`
	ProjectID  string `json:"projectId,omitempty"`
	RunID      string `json:"runId,omitempty"`
	NodeID     string `json:"nodeId,omitempty"`
	ArtifactID string `json:"artifactId,omitempty"`
	IssueID    string `json:"issueId,omitempty"`
	WorkflowID string `json:"workflowId,omitempty"`
	Title      string `json:"title"`
	Source     string `json:"source"`
	Path       string `json:"path"`
	Content    string `json:"content,omitempty"`
	Status     string `json:"status"`
	Commit     string `json:"commit,omitempty"`
	Error      string `json:"error,omitempty"`
	// Git 来源写回的管理视图。读取时由 knowledge_writebacks 覆盖，避免
	// 审核条目里的历史快照与合并状态不一致。
	Branch     string `json:"branch,omitempty"`
	MRURL      string `json:"mrUrl,omitempty"`
	MRProvider string `json:"mrProvider,omitempty"`
	MRStatus   string `json:"mrStatus,omitempty"`
	WriteNote  string `json:"writeNote,omitempty"`
	WriteError string `json:"writeError,omitempty"`
	// 自动沉淀的门禁快照。Fingerprint 是正文（不含 frontmatter）归一化后的
	// SHA-256；Signals 记录入队时的闭环事件；GateChecks 逐项保留结果。
	Fingerprint     string               `json:"fingerprint,omitempty"`
	SedimentSignals []string             `json:"sedimentSignals,omitempty"`
	GateChecks      []KnowledgeGateCheck `json:"gateChecks,omitempty"`
	RetractedAt     *time.Time           `json:"retractedAt,omitempty"`
	ReviewNote      string               `json:"reviewNote,omitempty"`
	CreatedBy       string               `json:"createdBy,omitempty"`
	ReviewedBy      string               `json:"reviewedBy,omitempty"`
	CreatedAt       time.Time            `json:"createdAt"`
	UpdatedAt       time.Time            `json:"updatedAt"`
	PublishedAt     time.Time            `json:"publishedAt,omitempty"`
}

// KnowledgeWriteback 是一次 Git 来源写回。分支和提交是推送结果，合并请求
// 链接与状态由托管平台返回并在后台刷新；Note/Error 只保存分类后的中文提示。
// 同一审核条目只有一条写回记录，重试沿用同一分支。
type KnowledgeWriteback struct {
	ID           string     `json:"id"`
	SubmissionID string     `json:"submissionId,omitempty"`
	SourceID     string     `json:"sourceId"`
	ProjectID    string     `json:"projectId,omitempty"`
	Path         string     `json:"path"`
	Branch       string     `json:"branch,omitempty"`
	Commit       string     `json:"commit,omitempty"`
	MRURL        string     `json:"mrUrl,omitempty"`
	MRProvider   string     `json:"mrProvider,omitempty"`
	MRIID        int        `json:"mrIid,omitempty"`
	MRState      string     `json:"mrState,omitempty"`
	Status       string     `json:"status"`
	Note         string     `json:"note,omitempty"`
	Error        string     `json:"error,omitempty"`
	Attempts     int        `json:"attempts,omitempty"`
	CheckedAt    *time.Time `json:"checkedAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt,omitempty"`
	UpdatedAt    time.Time  `json:"updatedAt,omitempty"`
}

// KnowledgeCandidate is a run artifact that looks like a distilled knowledge
// document and can be turned into a submission.
type KnowledgeCandidate struct {
	Artifact     Artifact `json:"artifact"`
	ProjectID    string   `json:"projectId,omitempty"`
	IssueID      string   `json:"issueId,omitempty"`
	WorkflowID   string   `json:"workflowId,omitempty"`
	RunStatus    string   `json:"runStatus,omitempty"`
	SubmissionID string   `json:"submissionId,omitempty"`
}

// 内置知识引擎的领域实体。控制面在 KnowledgeRepo，内容面在 KnowledgeContentRepo；
// 内容行只用 SourceID 字符串指向控制面，不建跨组外键。

// KnowledgeBase 是一个知识库。切片只写 Layer="project" 的项目层库；
// ProjectID 为空留给以后的公共/平台库，不参与 (project_id, layer) 唯一约束。
//
// 向量检索按知识库开启：VectorEnabled 打开且平台配置了 Qdrant/embedding 时，
// 该库下来源的索引块会进向量库，检索走混合通道。EmbeddingModel/Dim/
// QdrantCollection 是「索引契约」，固定在该行上：换模型必须换 collection 并
// 全量重建，不能让不同模型的向量混进同一 collection。向量只从正文重建，
// 这些字段是校验用的快照，不是事实源。
type KnowledgeBase struct {
	ID          string `json:"id"`
	ProjectID   string `json:"projectId,omitempty"`
	Name        string `json:"name"`
	Layer       string `json:"layer"`
	OwnerUserID int64  `json:"ownerUserId,omitempty"`
	// 向量检索索引契约（见 028）。向量按知识库开启，换模型换 collection 重建。
	VectorEnabled    bool   `json:"vectorEnabled,omitempty"`
	EmbeddingModel   string `json:"embeddingModel,omitempty"`
	EmbeddingDim     int    `json:"embeddingDim,omitempty"`
	QdrantCollection string `json:"qdrantCollection,omitempty"`
	// SedimentPublishMode 为空按人工审核处理；auto 表示门禁全过后自动写回。
	SedimentPublishMode string `json:"sedimentPublishMode,omitempty"`
	// SedimentRules 是敏感内容过滤规则集。nil 表示使用内置默认规则；非 nil
	//（包括空数组）表示该知识库显式配置的规则集。
	SedimentRules []KnowledgeGateRule `json:"sedimentRules,omitempty"`
	CreatedAt     time.Time           `json:"createdAt,omitempty"`
	UpdatedAt     time.Time           `json:"updatedAt,omitempty"`
}

// KnowledgeSource 是一个知识来源。Kind 为 hosted 或 git。托管库用
// HeadRevision 记当前修订号，Git 来源用 HeadCommit 记 commit。
type KnowledgeSource struct {
	CredentialCiphertext string     `json:"-"`
	CredentialConfigured bool       `json:"credentialConfigured"`
	ID                   string     `json:"id"`
	BaseID               string     `json:"baseId"`
	Kind                 string     `json:"kind"`
	Name                 string     `json:"name"`
	GitURL               string     `json:"gitUrl,omitempty"`
	Branch               string     `json:"branch,omitempty"`
	PathPrefix           string     `json:"pathPrefix,omitempty"`
	HeadCommit           string     `json:"headCommit,omitempty"`
	HeadRevision         int64      `json:"headRevision,omitempty"`
	Status               string     `json:"status,omitempty"`
	IndexStatus          string     `json:"indexStatus,omitempty"`
	DocCount             int        `json:"docCount,omitempty"`
	LastError            string     `json:"lastError,omitempty"`
	LastSyncedAt         *time.Time `json:"lastSyncedAt,omitempty"`
	LeaseOwner           string     `json:"leaseOwner,omitempty"`
	LeaseUntil           *time.Time `json:"leaseUntil,omitempty"`
}

// KnowledgeBinding 把项目绑定到知识库；切片里 Permission 只写 submit。
type KnowledgeBinding struct {
	ProjectID  string `json:"projectId"`
	BaseID     string `json:"baseId"`
	Permission string `json:"permission"`
}

// KnowledgeSyncJob 是一次同步的记录，状态与错误可在知识中心看见。
type KnowledgeSyncJob struct {
	ID           int64      `json:"id"`
	SourceID     string     `json:"sourceId"`
	Kind         string     `json:"kind"`
	Status       string     `json:"status"`
	FromCommit   string     `json:"fromCommit,omitempty"`
	ToCommit     string     `json:"toCommit,omitempty"`
	ChangedCount int        `json:"changedCount,omitempty"`
	DeletedCount int        `json:"deletedCount,omitempty"`
	Log          string     `json:"log,omitempty"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

// KnowledgeActivityLimit 是每个项目保留的最近调用条数，也是知识中心一次
// 读取的上限。控制面裁剪与读取共用这个值，避免两处漂移。
const KnowledgeActivityLimit = 200

// KnowledgeActivity 是一次 Agent 知识库工具调用。它只存控制面，不存正文；
// 记录在工具调用返回前尽力写入，写入失败不影响调用本身。ProjectID 是读取
// 时的隔离边界，RunID/SessionID 用来把记录对回 SOP 运行或 Main Bot 会话。
type KnowledgeActivity struct {
	At        time.Time `json:"at"`
	Tool      string    `json:"tool"`
	ProjectID string    `json:"projectId,omitempty"`
	AgentID   string    `json:"agentId,omitempty"`
	UserID    int64     `json:"userId,omitempty"`
	RunID     string    `json:"runId,omitempty"`
	SessionID string    `json:"sessionId,omitempty"`
	Query     string    `json:"query,omitempty"`
	Hits      int       `json:"hits"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	LatencyMS int64     `json:"latencyMs"`
}

// KnowledgeCitationLimit 是每个项目在统计窗口内保留的引用上限。
// KnowledgeCitationWindow 是引用计入「引用次数」和「从未命中」的时间窗：
// 更早的记录不参与统计，写入时也会删掉。
const (
	KnowledgeCitationLimit  = 2000
	KnowledgeCitationWindow = 90 * 24 * time.Hour
)

// KnowledgeCitation 是 Agent 一次检索或读取引用到的块或行范围。
// 它只记引用关系，不存正文。ProjectID 是隔离边界。
type KnowledgeCitation struct {
	ID        int64     `json:"id,omitempty"`
	At        time.Time `json:"at"`
	ProjectID string    `json:"projectId,omitempty"`
	AgentID   string    `json:"agentId,omitempty"`
	UserID    int64     `json:"userId,omitempty"`
	RunID     string    `json:"runId,omitempty"`
	SessionID string    `json:"sessionId,omitempty"`
	Tool      string    `json:"tool"`
	SourceID  string    `json:"sourceId"`
	Path      string    `json:"path"`
	Commit    string    `json:"commit,omitempty"`
	StartLine int       `json:"startLine,omitempty"`
	EndLine   int       `json:"endLine,omitempty"`
	ChunkID   int64     `json:"chunkId,omitempty"`
	BaseID    string    `json:"baseId,omitempty"`
	Layer     string    `json:"layer,omitempty"`
}

// KnowledgeCitationCount 是某篇文档在统计窗口内被引用的次数。
type KnowledgeCitationCount struct {
	SourceID string `json:"sourceId"`
	Path     string `json:"path"`
	Count    int    `json:"count"`
}

// KnowledgeDocument 是含 frontmatter 的完整 Markdown 正文。出处版本对托管库
// 是修订号的十进制字符串，对 Git 来源是 commit；两者都放在 Commit 字段。
type KnowledgeDocument struct {
	SourceID  string    `json:"sourceId"`
	Path      string    `json:"path"`
	Title     string    `json:"title,omitempty"`
	Body      string    `json:"body"`
	LineCount int       `json:"lineCount,omitempty"`
	SHA256    string    `json:"sha256,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	Revision  int64     `json:"revision,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// KnowledgeRevision 只给托管库追加，Git 同步不写修订历史。
type KnowledgeRevision struct {
	ID        string    `json:"id"`
	SourceID  string    `json:"sourceId"`
	Path      string    `json:"path"`
	Revision  int64     `json:"revision"`
	Body      string    `json:"body"`
	Author    string    `json:"author,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
}

// KnowledgeChunk 是检索用的索引块，可以从正文重建。
type KnowledgeChunk struct {
	ID        int64  `json:"id"`
	SourceID  string `json:"sourceId"`
	Path      string `json:"path"`
	Idx       int    `json:"idx"`
	Heading   string `json:"heading,omitempty"`
	Title     string `json:"title,omitempty"`
	StartLine int    `json:"startLine,omitempty"`
	EndLine   int    `json:"endLine,omitempty"`
	Commit    string `json:"commit,omitempty"`
	Revision  int64  `json:"revision,omitempty"`
	Text      string `json:"text"`
}

// KnowledgeChunkMatch 是关键词适配器的一次命中：索引块加相关度分数。
// 分数由适配器给出，调用方据此排序，不改索引块本身。
type KnowledgeChunkMatch struct {
	Chunk KnowledgeChunk `json:"chunk"`
	Score float64        `json:"score"`
}

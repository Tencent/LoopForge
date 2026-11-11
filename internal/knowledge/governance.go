package knowledge

// 长期治理：文档生命周期对检索的后处理、反馈、知识缺口、健康检查与处置
// 入口。平台只让问题被看见，不自动改知识内容；下线和更新都进入审核队列。

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/knowledge/governance"
)

const (
	// GovernanceStaleDays 是健康检查判断过期的默认阈值。
	GovernanceStaleDays = governance.DefaultStaleDays
	// deprecatedScoreFactor 是废弃文档的检索降权系数。只降权不删除，仍可
	// 通过明确路径读原文；归档文档则直接从检索结果中移除。
	deprecatedScoreFactor = 0.25
	governanceLoopEvery   = 24 * time.Hour
)

// DocumentMetaStore 是检索后回查文档生命周期的能力。*repo.Store 通过内嵌
// 的 KnowledgeContentRepo 满足；测试替身不实现时生命周期后处理按无状态
// 处理，不改变原有检索契约。
type DocumentMetaStore interface {
	KnowledgeDocumentMetas(sourceIDs []string) []domain.KnowledgeDocumentMeta
}

// GapStore 是知识缺口持久化能力。
type GapStore interface {
	PutKnowledgeGap(domain.KnowledgeGap) error
	KnowledgeGaps(projectID string, limit int) []domain.KnowledgeGap
}

// FeedbackStore 是反馈持久化能力。
type FeedbackStore interface {
	PutKnowledgeFeedback(domain.KnowledgeFeedback) error
	KnowledgeFeedbacks(projectID string, limit int) []domain.KnowledgeFeedback
}

// GovernanceStore 是健康检查结果持久化能力。
type GovernanceStore interface {
	ReplaceKnowledgeGovernanceReport(baseID string, scan domain.KnowledgeGovernanceScan, issues []domain.KnowledgeGovernanceIssue) error
	KnowledgeGovernanceIssues(baseIDs []string) []domain.KnowledgeGovernanceIssue
	KnowledgeGovernanceScans(baseIDs []string) []domain.KnowledgeGovernanceScan
}

func lifecycleKey(source, path string) string { return source + "\x00" + path }

// applyLifecycle 在检索器返回后按文档状态后处理：归档移除、废弃降权，再按
// 分数稳定排序。检索融合与向量代码不感知生命周期。
func (s *Service) applyLifecycle(result *SearchResult) {
	if s == nil || result == nil || len(result.Results) == 0 || s.store == nil {
		return
	}
	store, ok := s.store.(DocumentMetaStore)
	if !ok {
		return
	}
	sourceIDs := make([]string, 0, len(result.Results))
	seen := map[string]bool{}
	for _, hit := range result.Results {
		if hit.Source != "" && !seen[hit.Source] {
			seen[hit.Source] = true
			sourceIDs = append(sourceIDs, hit.Source)
		}
	}
	if len(sourceIDs) == 0 {
		return
	}
	byKey := map[string]domain.KnowledgeDocumentMeta{}
	for _, meta := range store.KnowledgeDocumentMetas(sourceIDs) {
		byKey[lifecycleKey(meta.SourceID, meta.Path)] = meta
	}
	kept := make([]SearchHit, 0, len(result.Results))
	for _, hit := range result.Results {
		if meta, ok := byKey[lifecycleKey(hit.Source, hit.Path)]; ok {
			hit.DocStatus = meta.Status
		}
		switch strings.ToLower(strings.TrimSpace(hit.DocStatus)) {
		case domain.KnowledgeDocArchived:
			continue
		case domain.KnowledgeDocDeprecated:
			hit.Score *= deprecatedScoreFactor
		}
		kept = append(kept, hit)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Score > kept[j].Score })
	result.Results = kept
}

// normalizeGapQuery 合并大小写和空白差异；不做语义改写，保留用户原始问法。
func normalizeGapQuery(query string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(query)), " "))
}

// RecordGap 持久化一次搜不到结果的查询。空项目或空查询不记录；同一项目内
// 按归一化查询合并计数。写入失败只影响面板，不回滚检索。
func (s *Service) RecordGap(projectID, query string) error {
	if s == nil || s.store == nil {
		return nil
	}
	projectID = strings.TrimSpace(projectID)
	normalized := normalizeGapQuery(query)
	if projectID == "" || normalized == "" {
		return nil
	}
	store, ok := s.store.(GapStore)
	if !ok {
		return nil
	}
	now := s.clock()
	return store.PutKnowledgeGap(domain.KnowledgeGap{
		ProjectID: projectID, Query: strings.TrimSpace(query), Normalized: normalized,
		Count: 1, FirstSeen: now, LastSeen: now,
	})
}

// FeedbackInput 是知识中心、Main Bot 回答和 Agent 标记问题共用的反馈入参。
type FeedbackInput struct {
	ProjectID string `json:"projectId"`
	Source    string `json:"source"`
	Path      string `json:"path"`
	Title     string `json:"title"`
	Query     string `json:"query"`
	Kind      string `json:"kind"`
	Note      string `json:"note"`
	Surface   string `json:"surface"`
	AgentID   string `json:"agentId"`
	RunID     string `json:"runId"`
	SessionID string `json:"sessionId"`
	MessageID string `json:"messageId"`
}

func validFeedbackKind(kind string) bool {
	switch kind {
	case domain.KnowledgeFeedbackHelpful, domain.KnowledgeFeedbackNotHelpful, domain.KnowledgeFeedbackIssue:
		return true
	default:
		return false
	}
}

// RecordFeedback 校验项目范围和文档存在性后写入一条反馈。source/path 可空：
// Main Bot 回答旁的反馈只针对回答本身；带文档的反馈用于知识中心。
func (s *Service) RecordFeedback(input FeedbackInput, actor string, userID int64) (domain.KnowledgeFeedback, error) {
	if s == nil || s.store == nil {
		return domain.KnowledgeFeedback{}, apperr.New(apperr.Unavailable, "知识库存储不可用")
	}
	projectID := strings.TrimSpace(input.ProjectID)
	if projectID == "" {
		return domain.KnowledgeFeedback{}, apperr.New(apperr.Invalid, "项目标识不能为空")
	}
	kind := strings.ToLower(strings.TrimSpace(input.Kind))
	if !validFeedbackKind(kind) {
		return domain.KnowledgeFeedback{}, apperr.New(apperr.Invalid, "反馈类型必须是 helpful、not_helpful 或 issue")
	}
	note := strings.TrimSpace(input.Note)
	if kind == domain.KnowledgeFeedbackIssue && note == "" {
		return domain.KnowledgeFeedback{}, apperr.New(apperr.Invalid, "标记问题需要填写问题说明")
	}
	if len([]rune(note)) > 2000 {
		return domain.KnowledgeFeedback{}, apperr.New(apperr.Invalid, "问题说明不能超过 2000 个字")
	}
	source := strings.TrimSpace(input.Source)
	docPath := strings.TrimSpace(input.Path)
	if (source == "") != (docPath == "") {
		return domain.KnowledgeFeedback{}, apperr.New(apperr.Invalid, "来源和文档路径必须同时提供或同时省略")
	}
	item := domain.KnowledgeFeedback{
		ID: "kbf-" + s.itemID(), ProjectID: projectID, SourceID: source, Path: docPath,
		Title: strings.TrimSpace(input.Title), Query: strings.TrimSpace(input.Query),
		Kind: kind, Note: note, Surface: strings.TrimSpace(input.Surface),
		Actor: strings.TrimSpace(actor), UserID: userID,
		AgentID: strings.TrimSpace(input.AgentID), RunID: strings.TrimSpace(input.RunID),
		SessionID: strings.TrimSpace(input.SessionID), MessageID: strings.TrimSpace(input.MessageID),
		CreatedAt: s.clock(),
	}
	if item.Surface == "" {
		item.Surface = "knowledge_center"
	}
	if source != "" {
		ms, err := s.managed()
		if err != nil {
			return domain.KnowledgeFeedback{}, err
		}
		if !sourceInProject(ms, projectID, source) {
			return domain.KnowledgeFeedback{}, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
		}
		if src, ok := ms.KnowledgeSource(source); ok {
			item.BaseID = src.BaseID
		}
		if lookup, ok := s.store.(interface {
			KnowledgeDocument(string, string) (domain.KnowledgeDocument, bool)
		}); ok {
			doc, found := lookup.KnowledgeDocument(source, docPath)
			if !found && kind == domain.KnowledgeFeedbackIssue {
				return domain.KnowledgeFeedback{}, apperr.New(apperr.NotFound, "要标记问题的知识文档不存在")
			}
			if found && item.Title == "" {
				item.Title = doc.Title
			}
		}
	}
	store, ok := s.store.(FeedbackStore)
	if !ok {
		return domain.KnowledgeFeedback{}, apperr.New(apperr.Unavailable, "知识库反馈存储不可用")
	}
	if err := store.PutKnowledgeFeedback(item); err != nil {
		return domain.KnowledgeFeedback{}, apperr.New(apperr.Upstream, "记录反馈失败："+err.Error())
	}
	return item, nil
}

// ReportIssue 是 Agent 的“标记文档问题”工具入口：只写反馈，不改正文。
func (s *Service) ReportIssue(_ context.Context, caller Caller, source, path, note string) (domain.KnowledgeFeedback, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return domain.KnowledgeFeedback{}, apperr.New(apperr.Invalid, "请说明文档存在什么问题")
	}
	return s.RecordFeedback(FeedbackInput{
		ProjectID: caller.ProjectID, Source: source, Path: path, Kind: domain.KnowledgeFeedbackIssue,
		Note: note, Surface: "agent", AgentID: caller.AgentID, RunID: caller.RunID, SessionID: caller.SessionID,
	}, "agent", caller.UserID)
}

// Feedback 读取项目最近的反馈。空项目不返回其他项目的数据。
func (s *Service) Feedback(projectID string, limit int) []domain.KnowledgeFeedback {
	out := []domain.KnowledgeFeedback{}
	if s == nil || s.store == nil {
		return out
	}
	store, ok := s.store.(FeedbackStore)
	if !ok || strings.TrimSpace(projectID) == "" {
		return out
	}
	return store.KnowledgeFeedbacks(projectID, limit)
}

// GovernanceBaseView 是治理面板里单个知识库的摘要。
type GovernanceBaseView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Layer      string     `json:"layer"`
	DocCount   int        `json:"docCount"`
	IssueCount int        `json:"issueCount"`
	ScannedAt  *time.Time `json:"scannedAt,omitempty"`
}

// GovernancePlaceholders 明确标记依赖引用表的两项尚未接入，避免面板用 0
// 冒充“从未命中”或“引用 0 次”。
type GovernancePlaceholders struct {
	CitationCounts bool `json:"citationCounts"`
	NeverHit       bool `json:"neverHit"`
}

// GovernanceOverview 是治理面板的一次读取结果。
type GovernanceOverview struct {
	StaleDays        int                               `json:"staleDays"`
	LastScanAt       *time.Time                        `json:"lastScanAt,omitempty"`
	Bases            []GovernanceBaseView              `json:"bases"`
	Scans            []domain.KnowledgeGovernanceScan  `json:"scans"`
	Issues           []domain.KnowledgeGovernanceIssue `json:"issues"`
	Counts           map[string]int                    `json:"counts"`
	Feedback         []domain.KnowledgeFeedback        `json:"feedback"`
	FeedbackCounts   map[string]int                    `json:"feedbackCounts"`
	Gaps             []domain.KnowledgeGap             `json:"gaps"`
	GapsTotal        int                               `json:"gapsTotal"`
	CitedDocuments   []CitedDocument                   `json:"citedDocuments"`
	UncitedDocuments []UncitedDocument                 `json:"uncitedDocuments"`
	Placeholders     GovernancePlaceholders            `json:"placeholders"`
	GeneratedAt      time.Time                         `json:"generatedAt"`
}

func newGovernanceCounts() map[string]int {
	return map[string]int{
		domain.KnowledgeGovernanceStale: 0, domain.KnowledgeGovernanceDuplicate: 0,
		domain.KnowledgeGovernanceDeadLink: 0, domain.KnowledgeGovernanceMissingOwner: 0,
		"total": 0,
	}
}

// governanceBases 返回一个治理范围：项目范围取项目库加已加载公共库；空项目
// 是公共库管理范围，只返回公共库。
func governanceBases(ms ManagedStore, projectID string) []domain.KnowledgeBase {
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		return visibleBases(ms, projectID)
	}
	out := []domain.KnowledgeBase{}
	for _, base := range ms.KnowledgeBases("") {
		if base.Layer == "shared" {
			out = append(out, base)
		}
	}
	return out
}

// GovernanceOverview 读取当前项目的健康检查、反馈和知识缺口。
func (s *Service) GovernanceOverview(projectID string) (GovernanceOverview, error) {
	generatedAt := time.Now().UTC()
	if s != nil {
		generatedAt = s.clock()
	}
	out := GovernanceOverview{
		StaleDays: GovernanceStaleDays, Bases: []GovernanceBaseView{}, Scans: []domain.KnowledgeGovernanceScan{},
		Issues: []domain.KnowledgeGovernanceIssue{}, Counts: newGovernanceCounts(),
		Feedback: []domain.KnowledgeFeedback{}, FeedbackCounts: map[string]int{
			domain.KnowledgeFeedbackHelpful: 0, domain.KnowledgeFeedbackNotHelpful: 0, domain.KnowledgeFeedbackIssue: 0,
		},
		Gaps: []domain.KnowledgeGap{}, Placeholders: GovernancePlaceholders{}, GeneratedAt: generatedAt,
	}
	if s == nil || s.store == nil {
		return out, apperr.New(apperr.Unavailable, "知识库治理存储不可用")
	}
	ms, err := s.managed()
	if err != nil {
		return out, err
	}
	gs, ok := s.store.(GovernanceStore)
	if !ok {
		return out, apperr.New(apperr.Unavailable, "知识库治理存储不可用")
	}
	bases := governanceBases(ms, projectID)
	baseIDs := make([]string, 0, len(bases))
	for _, base := range bases {
		baseIDs = append(baseIDs, base.ID)
	}
	scans := gs.KnowledgeGovernanceScans(baseIDs)
	issues := gs.KnowledgeGovernanceIssues(baseIDs)
	scanByBase := map[string]domain.KnowledgeGovernanceScan{}
	for _, scan := range scans {
		scanByBase[scan.BaseID] = scan
		if out.LastScanAt == nil || scan.ScannedAt.After(*out.LastScanAt) {
			at := scan.ScannedAt
			out.LastScanAt = &at
		}
	}
	issueCount := map[string]int{}
	for _, issue := range issues {
		issueCount[issue.BaseID]++
		out.Counts[issue.Type]++
		out.Counts["total"]++
	}
	for _, base := range bases {
		view := GovernanceBaseView{ID: base.ID, Name: base.Name, Layer: base.Layer, IssueCount: issueCount[base.ID]}
		if scan, ok := scanByBase[base.ID]; ok {
			view.DocCount = scan.DocCount
			at := scan.ScannedAt
			view.ScannedAt = &at
		}
		out.Bases = append(out.Bases, view)
	}
	out.Scans = scans
	out.Issues = issues
	if strings.TrimSpace(projectID) != "" {
		out.Feedback = s.Feedback(projectID, 200)
		for _, item := range out.Feedback {
			out.FeedbackCounts[item.Kind]++
		}
		if gaps, ok := s.store.(GapStore); ok {
			out.Gaps = gaps.KnowledgeGaps(projectID, 100)
			out.GapsTotal = len(out.Gaps)
		}
		usage, ok := s.CitationUsage(projectID)
		if ok {
			out.CitedDocuments = usage.Cited
			out.UncitedDocuments = usage.Uncited
		} else {
			out.Placeholders.CitationCounts = true
			out.Placeholders.NeverHit = true
		}
	} else {
		out.Placeholders.CitationCounts = true
		out.Placeholders.NeverHit = true
	}
	return out, nil
}

// ScanGovernance 对当前范围的知识库立即跑一次健康检查并持久化，返回刷新后
// 的面板数据。项目范围只扫描项目库和已加载公共库；空项目扫描全部公共库。
func (s *Service) ScanGovernance(ctx context.Context, projectID string) (GovernanceOverview, error) {
	if s == nil || s.store == nil {
		return GovernanceOverview{}, apperr.New(apperr.Unavailable, "知识库治理存储不可用")
	}
	ms, err := s.managed()
	if err != nil {
		return GovernanceOverview{}, err
	}
	if _, ok := s.store.(GovernanceStore); !ok {
		return GovernanceOverview{}, apperr.New(apperr.Unavailable, "知识库治理存储不可用")
	}
	if err := s.scanBases(ctx, ms, governanceBases(ms, projectID)); err != nil {
		return GovernanceOverview{}, err
	}
	return s.GovernanceOverview(projectID)
}

// ScanAllGovernance 是后台循环使用的全量扫描；不改变任何面板范围语义。
func (s *Service) ScanAllGovernance(ctx context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}
	ms, err := s.managed()
	if err != nil {
		return err
	}
	if _, ok := s.store.(GovernanceStore); !ok {
		return nil
	}
	return s.scanBases(ctx, ms, ms.KnowledgeBases(""))
}

func (s *Service) scanBases(ctx context.Context, ms ManagedStore, bases []domain.KnowledgeBase) error {
	gs := s.store.(GovernanceStore)
	now := s.clock()
	for _, base := range bases {
		if err := ctx.Err(); err != nil {
			return err
		}
		docs := []governance.Document{}
		for _, src := range ms.KnowledgeSources(base.ID) {
			for _, doc := range ms.KnowledgeDocuments(src.ID) {
				docs = append(docs, governance.Document{
					SourceID: doc.SourceID, Path: doc.Path, Title: doc.Title, Body: doc.Body,
					Owner: doc.Owner, Updated: doc.FrontmatterUpdated,
				})
			}
		}
		report := governance.Analyze(docs, now, GovernanceStaleDays)
		issues := make([]domain.KnowledgeGovernanceIssue, 0, len(report.Issues))
		for _, issue := range report.Issues {
			issues = append(issues, domain.KnowledgeGovernanceIssue{
				BaseID: base.ID, SourceID: issue.SourceID, Path: issue.Path, Title: issue.Title,
				Type: issue.Type, Line: issue.Line, Summary: issue.Summary, DetectedAt: now,
			})
		}
		scan := domain.KnowledgeGovernanceScan{BaseID: base.ID, ScannedAt: now, DocCount: report.Documents, IssueCount: len(issues)}
		if err := gs.ReplaceKnowledgeGovernanceReport(base.ID, scan, issues); err != nil {
			return apperr.New(apperr.Upstream, "保存治理检查结果失败："+err.Error())
		}
	}
	return nil
}

// StartGovernanceLoop 是 worker 角色的健康检查循环：启动后立即跑一次，之后
// 每 24 小时扫描全部知识库。错误只记日志，下一周期继续。
func (s *Service) StartGovernanceLoop(ctx context.Context) {
	go func() {
		if err := s.ScanAllGovernance(ctx); err != nil && ctx.Err() == nil {
			log.Printf("knowledge governance: %v", err)
		}
		ticker := time.NewTicker(governanceLoopEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.ScanAllGovernance(ctx); err != nil && ctx.Err() == nil {
					log.Printf("knowledge governance: %v", err)
				}
			}
		}
	}()
}

// GovernanceDispositionInput 是治理面板发起的处置。action 只能是 update 或
// retract；处置本身只创建审核条目，不改正文，也不直接下线。
type GovernanceDispositionInput struct {
	ProjectID string `json:"projectId"`
	Source    string `json:"source"`
	Path      string `json:"path"`
	Action    string `json:"action"`
	Note      string `json:"note"`
}

// DisposeGovernanceDocument 为问题文档创建一条待审核处置。update 会带入
// 当前正文供审核人编辑；retract 只允许托管来源，审核通过后删除正文与索引
// 并写墓碑。同一文档同一动作已有待审核条目时复用它，避免重复点击堆队列。
func (s *Service) DisposeGovernanceDocument(input GovernanceDispositionInput, actor string) (domain.KnowledgeSubmission, error) {
	projectID := strings.TrimSpace(input.ProjectID)
	source := strings.TrimSpace(input.Source)
	docPath := strings.TrimSpace(input.Path)
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action == "" {
		action = domain.KnowledgeSubmissionActionUpdate
	}
	if action != domain.KnowledgeSubmissionActionUpdate && action != domain.KnowledgeSubmissionActionRetract {
		return domain.KnowledgeSubmission{}, apperr.New(apperr.Invalid, "处置动作只能是 update 或 retract")
	}
	if source == "" || docPath == "" {
		return domain.KnowledgeSubmission{}, apperr.New(apperr.Invalid, "来源和文档路径必填")
	}
	ms, err := s.managed()
	if err != nil {
		return domain.KnowledgeSubmission{}, err
	}
	if !sourceWritable(ms, projectID, source) {
		return domain.KnowledgeSubmission{}, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	src, ok := ms.KnowledgeSource(source)
	if !ok {
		return domain.KnowledgeSubmission{}, apperr.New(apperr.NotFound, "知识库来源不存在")
	}
	if action == domain.KnowledgeSubmissionActionRetract && src.Kind != "hosted" {
		return domain.KnowledgeSubmission{}, apperr.New(apperr.Invalid, "Git 来源请通过仓库删除合并请求下线")
	}
	lookup, ok := s.store.(interface {
		KnowledgeDocument(string, string) (domain.KnowledgeDocument, bool)
	})
	if !ok {
		return domain.KnowledgeSubmission{}, apperr.New(apperr.Unavailable, "知识库内容存储不可用")
	}
	doc, found := lookup.KnowledgeDocument(source, docPath)
	if !found {
		return domain.KnowledgeSubmission{}, apperr.New(apperr.NotFound, "知识文档不存在")
	}
	if err := s.ensureDispositionStore(); err != nil {
		return domain.KnowledgeSubmission{}, err
	}
	for _, existing := range s.store.KnowledgeSubmissions(projectID, domain.KnowledgeSubmissionPending, 500) {
		existingAction := strings.TrimSpace(existing.Action)
		if existingAction == "" {
			existingAction = domain.KnowledgeSubmissionActionUpdate
		}
		if existing.ProjectID == projectID && existing.Source == source && existing.Path == docPath && existingAction == action {
			return existing, nil
		}
	}
	title := strings.TrimSpace(doc.Title)
	if title == "" {
		title = docPath
	}
	note := strings.TrimSpace(input.Note)
	if note == "" {
		if action == domain.KnowledgeSubmissionActionRetract {
			note = "治理处置：申请下线"
		} else {
			note = "治理处置：发起更新"
		}
	}
	item := domain.KnowledgeSubmission{
		ID: "kbs-" + s.itemID(), ProjectID: projectID, Title: title,
		Source: source, Path: docPath, Content: doc.Body,
		Status: domain.KnowledgeSubmissionPending, Action: action,
		ReviewNote: note, CreatedBy: strings.TrimSpace(actor),
		CreatedAt: s.clock(), UpdatedAt: s.clock(),
	}
	return s.save(item)
}

// ensureDispositionStore 确认 store 具备审核队列与墓碑能力，避免创建出无法
// 审核或无法写墓碑的条目。
func (s *Service) ensureDispositionStore() error {
	if _, ok := s.store.(interface {
		PutKnowledgeSubmission(domain.KnowledgeSubmission) error
		KnowledgeSubmissions(string, string, int) []domain.KnowledgeSubmission
	}); !ok {
		return apperr.New(apperr.Unavailable, "审核队列存储不可用")
	}
	if _, ok := s.store.(SedimentStore); !ok {
		return apperr.New(apperr.Unavailable, "知识库墓碑存储不可用")
	}
	return nil
}

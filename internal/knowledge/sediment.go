package knowledge

// 自动沉淀：把闭环事件触发的 knowledge.md 候选过门禁后放进审核队列。门禁
// 结果逐项保存在提交项上，发布模式按知识库配置；撤回写墓碑、删正文与索引，
// 同一项目里的相同内容不能再沉淀。

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

// SedimentStore 是自动沉淀额外需要的控制面能力。*repo.Store 通过内嵌的
// KnowledgeRepo 满足它；已有 Store 接口保持不动，后台循环与测试各取所需。
type SedimentStore interface {
	Run(string) (domain.Run, bool)
	ManagedStore
	Issue(id string) (domain.Issue, bool)
	KnowledgeTombstone(projectID, fingerprint string) (domain.KnowledgeTombstone, bool)
	PutKnowledgeTombstone(domain.KnowledgeTombstone) error
}

// DocumentRetractor 是引擎对已发布托管文档的撤回能力。Service 通过类型断言
// 使用它，保持 Upstream 五个只读方法不变。
type DocumentRetractor interface {
	RetractDocument(ctx context.Context, source, path string) error
}

const (
	sedimentScanLimit = 100
	sedimentMinRunes  = 20
)

// SedimentReport 是一次自动沉淀扫描的结果。
type SedimentReport struct {
	Queued        int `json:"queued"`
	AutoPublished int `json:"autoPublished"`
	Skipped       int `json:"skipped"`
}

// DefaultSensitiveRules 是不带内部名词的默认敏感内容规则集。知识库没有
// 显式配置规则时使用这一组；配置后以知识库上的规则为准。
func DefaultSensitiveRules() []domain.KnowledgeGateRule {
	return []domain.KnowledgeGateRule{
		{Name: "认证头或令牌", Pattern: `(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`, Enabled: true},
		{Name: "口令或密钥赋值", Pattern: `(?i)\b(?:password|passwd|secret|token|cookie|authorization)\s*[:=]\s*\S{4,}`, Enabled: true},
		{Name: "私钥", Pattern: `-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`, Enabled: true},
		{Name: "JWT", Pattern: `\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`, Enabled: true},
		{Name: "邮箱地址", Pattern: `(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`, Enabled: true},
		{Name: "手机号", Pattern: `(?:^|\D)1[3-9]\d{9}(?:\D|$)`, Enabled: true},
		{Name: "IPv4 地址", Pattern: `\b(?:25[0-5]|2[0-4]\d|1?\d?\d)(?:\.(?:25[0-5]|2[0-4]\d|1?\d?\d)){3}\b`, Enabled: true},
		{Name: "提示词注入", Pattern: `(?i)ignore (?:all |any )?(?:previous|prior) instructions?|忽略.{0,12}(?:系统|之前|以上).{0,8}(?:指令|要求)|</?(?:system|developer|assistant)(?:\s|>)`, Enabled: true},
	}
}

// EffectiveSensitiveRules 返回知识库实际生效的规则集。空配置表示使用默认
// 规则；显式配置（包括空数组）表示完全替换默认集。
func EffectiveSensitiveRules(base domain.KnowledgeBase) []domain.KnowledgeGateRule {
	if base.SedimentRules == nil {
		return DefaultSensitiveRules()
	}
	return base.SedimentRules
}

// KnowledgeFingerprint 对正文做归一化后取 SHA-256：统一换行、去掉首尾空白
// 并把连续空白压成一个空格。标题变化不影响指纹，同一正文重复沉淀会被拦。
func KnowledgeFingerprint(body string) string {
	text := strings.ReplaceAll(body, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.Join(strings.Fields(text), " ")
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// NewID 返回自动沉淀使用的短随机标识，供后台循环等没有 httpapi randomID
// 的调用方构造 Service。
func NewID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// SedimentOnce 扫描最近的 knowledge.md 运行产物：命中闭环事件且出自开启
// 知识库的角色时，过门禁后入审核队列；知识库配置为自动发布且门禁全过时
// 直接走已有写回路径。同一个产物只会入队一次。
func (s *Service) SedimentOnce(ctx context.Context) (SedimentReport, error) {
	var report SedimentReport
	s = s.Snapshot()
	if !s.Builtin() {
		return report, nil
	}
	store, ok := s.store.(SedimentStore)
	if !ok {
		return report, apperr.New(apperr.Unavailable, "知识库沉淀存储不可用")
	}
	candidates := s.store.KnowledgeArtifacts("", sedimentScanLimit)
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.Artifact.ID)
	}
	queued := s.store.KnowledgeSubmissionIDsByArtifact(ids)
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		if queued[candidate.Artifact.ID] != "" {
			continue
		}
		queuedNow, publishedNow, err := s.sedimentCandidate(ctx, store, candidate)
		if err != nil {
			log.Printf("knowledge sediment: artifact=%s: %v", candidate.Artifact.ID, err)
			report.Skipped++
			continue
		}
		if !queuedNow {
			continue
		}
		report.Queued++
		if publishedNow {
			report.AutoPublished++
		}
	}
	return report, nil
}

// StartSedimentLoop 是 worker 角色的自动沉淀循环。扫描失败只记日志，下个
// 周期继续；进程退出由 ctx 取消。
func (s *Service) StartSedimentLoop(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			if _, err := s.SedimentOnce(ctx); err != nil && ctx.Err() == nil {
				log.Printf("knowledge sediment: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *Service) sedimentCandidate(ctx context.Context, store SedimentStore, candidate domain.KnowledgeCandidate) (bool, bool, error) {
	run, ok := store.Run(candidate.Artifact.RunID)
	if !ok {
		return false, false, nil
	}
	if !roleHasKnowledgeBase(run, candidate.Artifact.NodeID, s.store.Agents(), s.store.Workflows()) {
		return false, false, nil
	}
	signals := closureSignals(run, s.issue(candidate.IssueID))
	if len(signals) == 0 {
		return false, false, nil
	}
	artifact, ok := s.store.Artifact(candidate.Artifact.RunID, candidate.Artifact.ID)
	if !ok {
		return false, false, nil
	}
	projectID := strings.TrimSpace(run.ExecutionContext.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(candidate.ProjectID)
	}
	if projectID == "" {
		return false, false, nil
	}
	parsed := ParseDistilled(artifact.Content)
	body := strings.TrimSpace(parsed.Body)
	if body == "" {
		return false, false, nil
	}
	fingerprint := KnowledgeFingerprint(body)
	base, source, path, err := s.sedimentTarget(store, projectID, parsed, fingerprint)
	if err != nil {
		return false, false, err
	}
	title := strings.TrimSpace(firstNonEmpty(parsed.Title, "未命名知识"))
	gates := s.evaluateSedimentGates(store, projectID, base, "", fingerprint, title, body, signals)
	now := s.clock()
	item := domain.KnowledgeSubmission{
		ID: autoSubmissionID(artifact.ID), ProjectID: projectID, RunID: run.ID, NodeID: artifact.NodeID,
		ArtifactID: artifact.ID, IssueID: run.ExecutionContext.IssueID, WorkflowID: run.WorkflowID,
		Title: title, Source: source, Path: path, Content: body,
		Status: domain.KnowledgeSubmissionPending, CreatedBy: "系统（自动沉淀）", CreatedAt: now,
		Fingerprint: fingerprint, SedimentSignals: signals, GateChecks: gates,
	}
	if _, err := s.save(item); err != nil {
		if lookup, ok := s.store.(interface {
			KnowledgeSubmissionByFingerprint(string, string) (domain.KnowledgeSubmission, bool)
		}); ok {
			if _, exists := lookup.KnowledgeSubmissionByFingerprint(projectID, fingerprint); exists {
				return false, false, nil
			}
		}
		return false, false, err
	}
	if gatesPassed(gates) && sedimentAutoPublish(base) {
		if _, err := s.Publish(ctx, item.ID, "系统（门禁自动发布）"); err == nil {
			return true, true, nil
		}
	}
	return true, false, nil
}

func (s *Service) issue(id string) domain.Issue {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.Issue{}
	}
	lookup, ok := s.store.(interface {
		Issue(string) (domain.Issue, bool)
	})
	if !ok {
		return domain.Issue{}
	}
	issue, _ := lookup.Issue(id)
	return issue
}

// roleHasKnowledgeBase 判断知识产物是否来自开启知识库的角色。Agent 节点按
// 该节点角色判断；脚本/命令节点没有角色，按同一次运行里是否至少有一个
// 开启知识库的角色判断，避免把 Agent 汇总产物误判为非知识运行。
func roleHasKnowledgeBase(run domain.Run, nodeID string, agents []domain.AgentDefinition, workflows []domain.WorkflowDefinition) bool {
	byID := map[string]domain.AgentDefinition{}
	for _, agent := range agents {
		byID[agent.ID] = agent
	}
	nodes := run.WorkflowSnapshot.Nodes
	if len(nodes) == 0 {
		for _, wf := range workflows {
			if wf.ID == run.WorkflowID {
				nodes = wf.Nodes
				break
			}
		}
	}
	if len(nodes) == 0 {
		return false
	}
	for _, node := range nodes {
		if node.ID != nodeID {
			continue
		}
		if agentID := strings.TrimSpace(node.AgentID); agentID != "" {
			return byID[agentID].KnowledgeBase
		}
		break
	}
	for _, node := range nodes {
		if agentID := strings.TrimSpace(node.AgentID); agentID != "" && byID[agentID].KnowledgeBase {
			return true
		}
	}
	return false
}

func closureSignals(run domain.Run, issue domain.Issue) []string {
	out := []string{}
	add := func(signal string) {
		for _, existing := range out {
			if existing == signal {
				return
			}
		}
		out = append(out, signal)
	}
	if strings.TrimSpace(run.Status) == "succeeded" {
		add(domain.KnowledgeSignalRunSucceeded)
	}
	for _, event := range run.Events {
		if strings.TrimSpace(event.Type) == "gate.approved" {
			add(domain.KnowledgeSignalGateApproved)
			break
		}
	}
	if issue.Completed() {
		add(domain.KnowledgeSignalIssueCompleted)
	}
	for _, mr := range issue.DeliveryMRs {
		if strings.EqualFold(strings.TrimSpace(mr.State), "merged") {
			add(domain.KnowledgeSignalMRMerged)
			break
		}
	}
	return out
}

// sedimentTarget 优先采用 knowledge.md frontmatter 里指定的项目内来源和
// 路径；否则落到项目托管库，按内容指纹生成稳定路径。
func (s *Service) sedimentTarget(store SedimentStore, projectID string, parsed Distilled, fingerprint string) (domain.KnowledgeBase, string, string, error) {
	source := strings.TrimSpace(parsed.Source)
	if source != "" && sourceWritable(store, projectID, source) {
		src, ok := store.KnowledgeSource(source)
		if ok {
			if base, ok := store.KnowledgeBase(src.BaseID); ok {
				path, ok := NormalizeDocPath(parsed.Path)
				if !ok || path == "" || !pathWithinSourcePrefix(src, path) {
					path = defaultSedimentPathForSource(src, fingerprint)
				}
				return base, source, path, nil
			}
		}
	}
	base, err := s.EnsureHostedBase(projectID)
	if err != nil {
		return domain.KnowledgeBase{}, "", "", err
	}
	defaultSource := hostedSourceID(projectID)
	src, ok := store.KnowledgeSource(defaultSource)
	if !ok {
		for _, source := range store.KnowledgeSources(base.ID) {
			if source.Kind == "hosted" {
				src, ok = source, true
				break
			}
		}
	}
	if !ok {
		return domain.KnowledgeBase{}, "", "", apperr.New(apperr.Unavailable, "项目还没有可写入的托管知识库")
	}
	path, ok := NormalizeDocPath(parsed.Path)
	if !ok || path == "" || !pathWithinSourcePrefix(src, path) {
		path = defaultSedimentPathForSource(src, fingerprint)
	}
	return base, src.ID, path, nil
}

func pathWithinSourcePrefix(src domain.KnowledgeSource, path string) bool {
	prefix, ok := NormalizePathPrefix(src.PathPrefix)
	if !ok {
		return false
	}
	return prefix == "" || strings.HasPrefix(path, prefix+"/")
}

func defaultSedimentPath(fingerprint string) string {
	short := strings.TrimSpace(fingerprint)
	if len(short) > 12 {
		short = short[:12]
	}
	return "knowledge/" + short + ".md"
}

func defaultSedimentPathForSource(src domain.KnowledgeSource, fingerprint string) string {
	path := defaultSedimentPath(fingerprint)
	prefix, ok := NormalizePathPrefix(src.PathPrefix)
	if !ok || prefix == "" {
		return path
	}
	return prefix + "/" + path
}

// autoSubmissionID is deterministic per artifact so two instances scanning the
// same run cannot create two queue entries; the row upsert collapses them.
func autoSubmissionID(artifactID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(artifactID)))
	return "kbs-auto-" + hex.EncodeToString(sum[:16])
}

// evaluateSedimentGates 按证据、敏感内容、指纹去重、墓碑的顺序逐项记录。
// selfID 在发布前重新检查时排除当前条目本身；自动入队时为空。
func (s *Service) evaluateSedimentGates(store SedimentStore, projectID string, base domain.KnowledgeBase, selfID, fingerprint, title, body string, signals []string) []domain.KnowledgeGateCheck {
	checks := make([]domain.KnowledgeGateCheck, 0, 4)
	checks = append(checks, s.evidenceGate(signals, title, body))
	checks = append(checks, sensitiveGate(base, title, body))
	checks = append(checks, s.fingerprintGate(projectID, selfID, fingerprint))
	checks = append(checks, s.tombstoneGate(store, projectID, fingerprint))
	return checks
}

func (s *Service) evidenceGate(signals []string, title, body string) domain.KnowledgeGateCheck {
	check := domain.KnowledgeGateCheck{Key: "evidence", Name: "证据检查"}
	if len(signals) == 0 {
		check.Detail = "没有闭环事件作为证据"
		return check
	}
	if strings.TrimSpace(title) == "" || utf8.RuneCountInString(strings.TrimSpace(body)) < sedimentMinRunes {
		check.Detail = fmt.Sprintf("内容不足 %d 字，无法作为可复用知识", sedimentMinRunes)
		return check
	}
	check.OK = true
	check.Detail = "闭环事件：" + signalLabels(signals)
	return check
}

func sensitiveGate(base domain.KnowledgeBase, title, body string) domain.KnowledgeGateCheck {
	check := domain.KnowledgeGateCheck{Key: "sensitive", Name: "敏感内容过滤"}
	text := strings.TrimSpace(title) + "\n" + body
	matched := []string{}
	for _, rule := range EffectiveSensitiveRules(base) {
		if !rule.Enabled || strings.TrimSpace(rule.Pattern) == "" {
			continue
		}
		re, err := regexp.Compile(rule.Pattern)
		if err != nil || !re.MatchString(text) {
			continue
		}
		matched = append(matched, strings.TrimSpace(rule.Name))
	}
	if len(matched) > 0 {
		check.Detail = "命中规则：" + strings.Join(matched, "、")
		return check
	}
	check.OK = true
	check.Detail = "未命中敏感规则"
	return check
}

func (s *Service) fingerprintGate(projectID, selfID, fingerprint string) domain.KnowledgeGateCheck {
	check := domain.KnowledgeGateCheck{Key: "fingerprint", Name: "指纹去重"}
	if fingerprint == "" {
		check.Detail = "内容指纹为空"
		return check
	}
	for _, item := range s.store.KnowledgeSubmissions(projectID, "", 500) {
		if item.ID == selfID || item.Fingerprint != fingerprint {
			continue
		}
		if item.Status == domain.KnowledgeSubmissionPending || item.Status == domain.KnowledgeSubmissionPublished {
			check.Detail = fmt.Sprintf("与提交 %s 内容重复", item.ID)
			return check
		}
	}
	check.OK = true
	check.Detail = "未发现相同内容"
	return check
}

func (s *Service) tombstoneGate(store SedimentStore, projectID, fingerprint string) domain.KnowledgeGateCheck {
	check := domain.KnowledgeGateCheck{Key: "tombstone", Name: "墓碑检查"}
	if fingerprint == "" {
		check.Detail = "内容指纹为空"
		return check
	}
	if tombstone, ok := store.KnowledgeTombstone(projectID, fingerprint); ok {
		when := ""
		if !tombstone.CreatedAt.IsZero() {
			when = "（" + tombstone.CreatedAt.UTC().Format("2006-01-02") + "）"
		}
		check.Detail = "该内容此前已撤回" + when
		return check
	}
	check.OK = true
	check.Detail = "未命中墓碑"
	return check
}

func signalLabels(signals []string) string {
	labels := map[string]string{
		domain.KnowledgeSignalRunSucceeded:   "LOOP 运行成功",
		domain.KnowledgeSignalMRMerged:       "合并请求已合并",
		domain.KnowledgeSignalIssueCompleted: "需求已完成",
		domain.KnowledgeSignalGateApproved:   "人工关卡通过",
	}
	out := make([]string, 0, len(signals))
	for _, signal := range signals {
		if label, ok := labels[signal]; ok {
			out = append(out, label)
		}
	}
	return strings.Join(out, "、")
}

func gatesPassed(checks []domain.KnowledgeGateCheck) bool {
	if len(checks) == 0 {
		return false
	}
	for _, check := range checks {
		if !check.OK {
			return false
		}
	}
	return true
}

func sedimentAutoPublish(base domain.KnowledgeBase) bool {
	return strings.EqualFold(strings.TrimSpace(base.SedimentPublishMode), domain.KnowledgeSedimentAuto)
}

// SedimentConfig 是知识中心读取的沉淀配置。规则集总是返回实际生效的一组。
type SedimentConfig struct {
	BaseID string                     `json:"baseId"`
	Mode   string                     `json:"mode"`
	Rules  []domain.KnowledgeGateRule `json:"rules"`
}

// SedimentConfig 读取知识库的沉淀发布模式与生效规则。
func (s *Service) SedimentConfig(projectID, baseID string) (SedimentConfig, error) {
	ms, err := s.managed()
	if err != nil {
		return SedimentConfig{}, err
	}
	base, ok := ms.KnowledgeBase(strings.TrimSpace(baseID))
	if !ok || !ownsBase(base, projectID) {
		return SedimentConfig{}, apperr.New(apperr.NotFound, "知识库不存在")
	}
	mode := strings.TrimSpace(base.SedimentPublishMode)
	if mode == "" {
		mode = domain.KnowledgeSedimentReview
	}
	return SedimentConfig{BaseID: base.ID, Mode: mode, Rules: EffectiveSensitiveRules(base)}, nil
}

// SetSedimentConfig 保存发布模式与敏感规则集。规则非 nil 时替换默认集；
// 传 nil 恢复内置默认规则。
func (s *Service) SetSedimentConfig(projectID, baseID, mode string, rules []domain.KnowledgeGateRule) (domain.KnowledgeBase, error) {
	ms, err := s.managed()
	if err != nil {
		return domain.KnowledgeBase{}, err
	}
	base, ok := ms.KnowledgeBase(strings.TrimSpace(baseID))
	if !ok || !ownsBase(base, projectID) {
		return domain.KnowledgeBase{}, apperr.New(apperr.NotFound, "知识库不存在")
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = domain.KnowledgeSedimentReview
	}
	if mode != domain.KnowledgeSedimentReview && mode != domain.KnowledgeSedimentAuto {
		return domain.KnowledgeBase{}, apperr.New(apperr.Invalid, "发布模式只能是人工审核或自动发布")
	}
	clean, err := validateGateRules(rules)
	if err != nil {
		return domain.KnowledgeBase{}, apperr.New(apperr.Invalid, err.Error())
	}
	base.SedimentPublishMode = mode
	base.SedimentRules = clean
	if err := ms.PutKnowledgeBase(base); err != nil {
		return domain.KnowledgeBase{}, err
	}
	saved, _ := ms.KnowledgeBase(base.ID)
	return saved, nil
}

func validateGateRules(rules []domain.KnowledgeGateRule) ([]domain.KnowledgeGateRule, error) {
	if rules == nil {
		return nil, nil
	}
	if len(rules) > 32 {
		return nil, fmt.Errorf("敏感规则最多 32 条")
	}
	out := make([]domain.KnowledgeGateRule, 0, len(rules))
	for _, rule := range rules {
		name := strings.TrimSpace(rule.Name)
		pattern := strings.TrimSpace(rule.Pattern)
		if name == "" || utf8.RuneCountInString(name) > 60 {
			return nil, fmt.Errorf("规则名称须为 1–60 个字")
		}
		if pattern == "" || utf8.RuneCountInString(pattern) > 300 {
			return nil, fmt.Errorf("规则「%s」的正则须为 1–300 个字符", name)
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("规则「%s」的正则无法编译", name)
		}
		out = append(out, domain.KnowledgeGateRule{Name: name, Pattern: pattern, Enabled: rule.Enabled})
	}
	return out, nil
}

// Retract 撤回一篇已写回的知识：先删正文与索引，再写墓碑并更新提交项。
// Git 来源的原文在仓库里，直接删本地索引不会删除事实源，因此要求通过
// 删除合并请求撤回，避免平台显示与仓库内容不一致。
func (s *Service) Retract(ctx context.Context, id, reasonCategory, reasonDetail, actor string) (domain.KnowledgeSubmission, error) {
	item, err := s.Submission(id)
	if err != nil {
		return item, err
	}
	if item.Status == domain.KnowledgeSubmissionRetracted {
		return item, nil
	}
	if item.Status != domain.KnowledgeSubmissionPublished && item.Status != domain.KnowledgeSubmissionPublishing {
		return item, apperr.New(apperr.Conflict, "只有已写回的条目可以撤回")
	}
	reason := strings.TrimSpace(reasonCategory)
	if detail := strings.TrimSpace(reasonDetail); detail != "" {
		reason = strings.TrimSpace(reason + "：" + detail)
	}
	return s.retractDocument(ctx, item, reason, actor)
}

// publishRetraction 执行治理面板发起、审核通过的 pending 下线条目。它不跑
// 沉淀门禁：内容本身可能正是因为有敏感信息或质量问题才需要下线。
func (s *Service) publishRetraction(ctx context.Context, item domain.KnowledgeSubmission, actor string) (domain.KnowledgeSubmission, error) {
	reason := strings.TrimSpace(item.ReviewNote)
	if reason == "" {
		reason = "治理处置：申请下线"
	}
	return s.retractDocument(ctx, item, reason, actor)
}

// retractDocument 是已发布撤回和审核通过下线的共同执行路径。
func (s *Service) retractDocument(ctx context.Context, item domain.KnowledgeSubmission, reason, actor string) (domain.KnowledgeSubmission, error) {
	source := strings.TrimSpace(item.Source)
	ms, err := s.managed()
	if err != nil {
		return item, err
	}
	if !sourceWritable(ms, item.ProjectID, source) {
		return item, apperr.New(apperr.Forbidden, "知识库不属于当前项目")
	}
	if source == "" {
		return item, apperr.New(apperr.Invalid, "提交项没有写回来源")
	}
	src, ok := s.sourceByID(source)
	if !ok {
		return item, apperr.New(apperr.Invalid, "提交项的写回来源不存在")
	}
	if src.Kind == "git" {
		return item, apperr.New(apperr.Invalid, "Git 来源的知识请通过删除合并请求撤回，平台不会直接改写仓库")
	}
	retractor, ok := s.upstream.(DocumentRetractor)
	if !ok {
		return item, apperr.New(apperr.Unavailable, "内置知识引擎不支持撤回")
	}
	fingerprint := strings.TrimSpace(item.Fingerprint)
	if fingerprint == "" {
		fingerprint = KnowledgeFingerprint(item.Content)
	}
	now := s.clock()
	store, ok := s.store.(SedimentStore)
	if !ok {
		return item, apperr.New(apperr.Unavailable, "知识库沉淀存储不可用")
	}
	if err := store.PutKnowledgeTombstone(domain.KnowledgeTombstone{
		ProjectID: item.ProjectID, Fingerprint: fingerprint, BaseID: src.BaseID,
		SourceID: source, Path: item.Path, SubmissionID: item.ID,
		Title: item.Title, Reason: strings.TrimSpace(reason), CreatedBy: actor, CreatedAt: now,
	}); err != nil {
		return item, apperr.New(apperr.Upstream, "写入墓碑失败："+err.Error())
	}
	if err := retractor.RetractDocument(ctx, source, item.Path); err != nil {
		return item, apperr.New(apperr.Upstream, "撤回失败："+err.Error())
	}
	item.Status = domain.KnowledgeSubmissionRetracted
	item.RetractedAt = &now
	if strings.TrimSpace(reason) != "" {
		item.ReviewNote = strings.TrimSpace(reason)
	}
	item.ReviewedBy = actor
	return s.save(item)
}

// checkPublishGates 在真正写回前重算门禁：墓碑与敏感规则对所有条目生效；
// 自动沉淀条目还重算证据与指纹去重，人工提交不因缺少运行事件被卡住。
func (s *Service) checkPublishGates(item domain.KnowledgeSubmission) (domain.KnowledgeSubmission, error) {
	fingerprint := KnowledgeFingerprint(item.Content)
	item.Fingerprint = fingerprint
	base := s.baseForSource(item.Source)
	auto := len(item.SedimentSignals) > 0
	evidence := s.evidenceGate(item.SedimentSignals, item.Title, item.Content)
	fingerprintCheck := s.fingerprintGate(item.ProjectID, item.ID, fingerprint)
	if !auto {
		// 人工提交没有运行事件；证据与去重不阻断写回，但保留说明让审核页知道
		// 这一项不适用。
		evidence = domain.KnowledgeGateCheck{Key: "evidence", Name: "证据检查", OK: true, Detail: "人工提交，证据由审核人确认"}
		fingerprintCheck = domain.KnowledgeGateCheck{Key: "fingerprint", Name: "指纹去重", OK: true, Detail: "人工提交，不按指纹拦截重复"}
	}
	checks := []domain.KnowledgeGateCheck{
		evidence,
		sensitiveGate(base, item.Title, item.Content),
		fingerprintCheck,
		s.tombstoneGateFor(s.store, item.ProjectID, fingerprint),
	}
	item.GateChecks = checks
	if check := checks[3]; !check.OK {
		return item, apperr.New(apperr.Conflict, check.Detail)
	}
	if check := checks[1]; !check.OK {
		return item, apperr.New(apperr.Invalid, check.Detail)
	}
	if auto {
		if check := checks[0]; !check.OK {
			return item, apperr.New(apperr.Invalid, check.Detail)
		}
		if check := checks[2]; !check.OK {
			return item, apperr.New(apperr.Conflict, check.Detail)
		}
	}
	return item, nil
}

func (s *Service) baseForSource(source string) domain.KnowledgeBase {
	if src, ok := s.sourceByID(source); ok {
		if ms, err := s.managed(); err == nil {
			if base, ok := ms.KnowledgeBase(src.BaseID); ok {
				return base
			}
		}
	}
	return domain.KnowledgeBase{}
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) itemID() string {
	if s.newID == nil {
		return NewID()
	}
	return s.newID()
}

// tombstoneGateFor 在存储不支持墓碑（例如极简的测试替身）时按未命中处理。
func (s *Service) tombstoneGateFor(store Store, projectID, fingerprint string) domain.KnowledgeGateCheck {
	sediment, ok := store.(SedimentStore)
	if !ok {
		return domain.KnowledgeGateCheck{Key: "tombstone", Name: "墓碑检查", OK: true, Detail: "当前存储未启用墓碑"}
	}
	return s.tombstoneGate(sediment, projectID, fingerprint)
}

package evolution

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

const SignalSOPPreference = "sop_preference"

var sopPreferenceOrder = []string{
	domain.EvoPrefTokens,
	domain.EvoPrefTools,
	domain.EvoPrefDuration,
	domain.EvoPrefClean,
}

// SOPPreferenceLabels maps preference keys into human-readable Chinese labels.
func SOPPreferenceLabels(prefs []string) []string {
	labels := map[string]string{
		domain.EvoPrefTokens:   "减少 Token 消耗",
		domain.EvoPrefTools:    "减少工具调用次数",
		domain.EvoPrefDuration: "缩短执行耗时",
		domain.EvoPrefClean:    "提高一次成功率",
	}
	norm := NormalizeSOPPreferences(prefs)
	out := make([]string, 0, len(norm))
	for _, k := range norm {
		if l, ok := labels[k]; ok {
			out = append(out, l)
		}
	}
	return out
}

// BuildSOPPreferenceFinding constructs a concrete, preference-guided finding from
// a completed SOP batch so the improver can optimize Token/Tools/Duration/Clean rate
// even when most runs succeeded without hard errors.
func BuildSOPPreferenceFinding(
	projectID string,
	wf domain.WorkflowDefinition,
	assetKind, assetID, assetName, nodeID string,
	policy domain.EvoSOPPolicy,
	batchRuns []domain.Run,
	metrics domain.EvoSOPMetrics,
) domain.EvoFinding {
	prefs := NormalizeSOPPreferences(policy.Preferences)
	prefText := strings.Join(SOPPreferenceLabels(prefs), "、")
	scored := append([]domain.Run(nil), batchRuns...)
	sort.SliceStable(scored, func(i, j int) bool {
		// Prioritize highest-cost or non-clean runs according to primary preference.
		switch prefs[0] {
		case domain.EvoPrefTools:
			if RunToolCalls(scored[i]) != RunToolCalls(scored[j]) {
				return RunToolCalls(scored[i]) > RunToolCalls(scored[j])
			}
		case domain.EvoPrefDuration:
			if RunDurationMs(scored[i]) != RunDurationMs(scored[j]) {
				return RunDurationMs(scored[i]) > RunDurationMs(scored[j])
			}
		case domain.EvoPrefClean:
			if RunScore(scored[i]) != RunScore(scored[j]) {
				return RunScore(scored[i]) < RunScore(scored[j])
			}
		}
		return RunTokens(scored[i]) > RunTokens(scored[j])
	})

	signals := make([]domain.EvoSignal, 0, min(len(scored), 6))
	sampleIDs := make([]string, 0, min(len(scored), 6))
	for _, r := range scored {
		if len(sampleIDs) >= 6 {
			break
		}
		sampleIDs = append(sampleIDs, r.ID)
		detail := fmt.Sprintf("偏好目标：%s。本运行消耗 %d Token、%d 次工具调用、耗时 %.1fs（状态 %s）。请在保证最终产物与验收标准完全一致的前提下，精简多余步骤、重复核对与冗余输出。",
			prefText, RunTokens(r), RunToolCalls(r), float64(RunDurationMs(r))/1000.0, r.Status)
		signals = append(signals, domain.EvoSignal{
			ProjectID: projectID,
			RunID:     r.ID,
			NodeID:    nodeID,
			Kind:      SignalSOPPreference,
			Detail:    detail,
			CreatedAt: r.CreatedAt,
		})
	}
	summary := fmt.Sprintf("SOP「%s」近 %d 次运行统计：平均 Token %d、平均工具调用 %.1f 次、平均耗时 %.1fs、一次成功率 %.0f%%。请严格围绕用户优化偏好【%s】精简该角色/规则正文，去除重复检索、重复核对和冗余输出，同时不得破坏正确性与验收门禁。",
		firstText(wf.Name, wf.ID), metrics.Runs, metrics.AvgTokens, metrics.AvgTools, metrics.AvgDuration, metrics.CleanRate*100, prefText)
	now := time.Now().UTC()
	return domain.EvoFinding{
		ProjectID:    projectID,
		AssetKind:    assetKind,
		AssetID:      assetID,
		AssetName:    firstText(assetName, assetID),
		Title:        fmt.Sprintf("按偏好优化（%s）", prefText),
		Summary:      summary,
		Signals:      signals,
		SampleRunIDs: sampleIDs,
		WorkflowIDs:  []string{wf.ID},
		Status:       domain.EvoFindingOpen,
		FirstSeenAt:  now,
		LastSeenAt:   now,
	}
}

// RunTokens returns the best available total tokens for a run.
func RunTokens(run domain.Run) int64 {
	if t := usageTotal(run.Usage); t > 0 {
		return t
	}
	var sum int64
	for _, nr := range run.Nodes {
		if nr != nil {
			sum += NodeTokens(*nr)
		}
	}
	return sum
}

// NodeTokens returns the best available total tokens for a node run.
func NodeTokens(nr domain.NodeRun) int64 {
	return usageTotal(nr.Usage)
}

func usageTotal(u domain.TokenUsage) int64 {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens + u.ReasoningTokens
}

// RunToolCalls returns the total tool calls of a run.
func RunToolCalls(run domain.Run) int {
	if run.ToolCalls > 0 {
		return run.ToolCalls
	}
	sum := 0
	for _, nr := range run.Nodes {
		if nr != nil {
			sum += nr.ToolCalls
		}
	}
	return sum
}

// RunDurationMs returns execution duration (or wall duration) in milliseconds.
func RunDurationMs(run domain.Run) int64 {
	if run.Timing != nil {
		if run.Timing.ExecutionMs > 0 {
			return run.Timing.ExecutionMs
		}
		if run.Timing.WallMs > 0 {
			return run.Timing.WallMs
		}
	}
	if !run.CreatedAt.IsZero() && !run.UpdatedAt.IsZero() && run.UpdatedAt.After(run.CreatedAt) {
		return run.UpdatedAt.Sub(run.CreatedAt).Milliseconds()
	}
	return 0
}

// NormalizeSOPPreferences validates and deduplicates preference keys.
func NormalizeSOPPreferences(prefs []string) []string {
	allowed := map[string]bool{}
	for _, k := range sopPreferenceOrder {
		allowed[k] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(prefs))
	for _, raw := range prefs {
		k := strings.TrimSpace(strings.ToLower(raw))
		if !allowed[k] || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	if len(out) == 0 {
		return []string{domain.EvoPrefTokens}
	}
	return out
}

// NormalizeSOPBatchSize clamps the per-SOP batch trigger size into [1, 100] with default 10.
func NormalizeSOPBatchSize(n int) int {
	if n <= 0 {
		return 10
	}
	if n > 100 {
		return 100
	}
	return n
}

// FormatSOPVersion formats a workflow version integer into vN (minimum v1).
func FormatSOPVersion(v int) string {
	if v <= 0 {
		v = 1
	}
	return fmt.Sprintf("v%d", v)
}

// ComputeRunHeuristicScore produces a transparent 0-100 score for a terminal run
// when no RunEvalRecord exists:
//   - Outcome (45): clean=45, reworked=28, failed=8
//   - Step efficiency (20): minus 5 per rework/retry
//   - Tool reliability (15): minus 4 per failed tool call
//   - Tool economy (10): <=12->10, <=24->8, <=40->5, else 2
//   - Token economy (10): unknown->7, <=50k->10, <=120k->8, <=250k->5, else 2
func ComputeRunHeuristicScore(run domain.Run) int {
	rs := RunScore(run)
	score := 8
	switch rs {
	case 2:
		score = 45
	case 1:
		score = 28
	}

	repeats := 0
	for _, nr := range run.Nodes {
		if nr != nil {
			repeats += nr.ReworkCount + max(0, nr.Attempt-1)
		}
	}
	score += max(0, 20-repeats*5)

	toolFails := 0
	for _, ev := range run.Telemetry {
		if ev.Category == "tool" && (ev.Error != nil || ev.Phase == "error" || ev.Level == "error") {
			toolFails++
		}
	}
	score += max(0, 15-toolFails*4)

	tools := RunToolCalls(run)
	switch {
	case tools <= 12:
		score += 10
	case tools <= 24:
		score += 8
	case tools <= 40:
		score += 5
	default:
		score += 2
	}

	tokens := RunTokens(run)
	switch {
	case tokens <= 0:
		score += 7
	case tokens <= 50_000:
		score += 10
	case tokens <= 120_000:
		score += 8
	case tokens <= 250_000:
		score += 5
	default:
		score += 2
	}

	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

// SummarizeSOPRuns computes core execution metrics and weighted SOP score over terminal runs.
func SummarizeSOPRuns(runs []domain.Run, evals map[string]domain.RunEvalRecord) domain.EvoSOPMetrics {
	var m domain.EvoSOPMetrics
	var totalDurMs int64
	var durRuns int
	var evalSum, evalCount int
	var heurSum int

	for _, run := range runs {
		rs := RunScore(run)
		if rs < 0 {
			continue
		}
		m.Runs++
		if rs == 2 {
			m.CleanCount++
		}
		m.TotalTokens += RunTokens(run)
		m.TotalTools += RunToolCalls(run)
		if d := RunDurationMs(run); d > 0 {
			totalDurMs += d
			durRuns++
		}
		if evals != nil {
			if rec, ok := evals[run.ID]; ok && rec.Status == domain.RunEvalSucceeded && !rec.SOPDetection {
				evalSum += rec.Score
				evalCount++
			}
		}
		heurSum += ComputeRunHeuristicScore(run)
	}
	if m.Runs == 0 {
		return m
	}

	m.AvgTokens = m.TotalTokens / int64(m.Runs)
	m.AvgTools = round1(float64(m.TotalTools) / float64(m.Runs))
	if durRuns > 0 {
		m.AvgDuration = round1(float64(totalDurMs) / float64(durRuns) / 1000.0)
	}
	m.CleanRate = round4(float64(m.CleanCount) / float64(m.Runs))

	if evalCount > 0 {
		s := int(math.Round(float64(evalSum) / float64(evalCount)))
		m.Score = &s
		m.ScoredRuns = evalCount
		m.ScoreSource = "eval"
	} else {
		s := int(math.Round(float64(heurSum) / float64(m.Runs)))
		m.Score = &s
		m.ScoredRuns = m.Runs
		m.ScoreSource = "heuristic"
	}
	return m
}

// BuildSOPVersions groups runs by WorkflowSnapshot.Version (ascending) and summarizes
// up to batchSize latest runs per version for the SOP version trend chart & table.
func BuildSOPVersions(wf domain.WorkflowDefinition, runs []domain.Run, evals map[string]domain.RunEvalRecord, batchSize int) []domain.EvoSOPVersionPoint {
	batchSize = NormalizeSOPBatchSize(batchSize)
	currentVer := wf.Version
	if currentVer <= 0 {
		currentVer = 1
	}

	byVer := map[int][]domain.Run{}
	for _, run := range runs {
		if RunScore(run) < 0 {
			continue
		}
		ver := run.WorkflowSnapshot.Version
		if ver <= 0 {
			ver = 1
		}
		byVer[ver] = append(byVer[ver], run)
	}
	if _, ok := byVer[currentVer]; !ok {
		byVer[currentVer] = nil
	}

	vers := make([]int, 0, len(byVer))
	for v := range byVer {
		vers = append(vers, v)
	}
	sort.Ints(vers)
	if len(vers) > 12 {
		vers = vers[len(vers)-12:]
	}

	points := make([]domain.EvoSOPVersionPoint, 0, len(vers))
	for _, v := range vers {
		group := append([]domain.Run(nil), byVer[v]...)
		sort.SliceStable(group, func(i, j int) bool {
			return group[i].CreatedAt.After(group[j].CreatedAt)
		})
		if len(group) > batchSize {
			group = group[:batchSize]
		}
		points = append(points, domain.EvoSOPVersionPoint{
			Version: FormatSOPVersion(v),
			Current: v == currentVer,
			Metrics: SummarizeSOPRuns(group, evals),
		})
	}
	return points
}

// DetermineSOPStage maps an SOP's related proposals to one of the 3 workbench stages:
// gray (canary active), optimize (drafting/validating/pending), or monitor.
func DetermineSOPStage(proposals []domain.EvoProposal) (string, *domain.EvoProposal, *domain.EvoProposal) {
	if len(proposals) == 0 {
		return domain.EvoSOPStageMonitor, nil, nil
	}
	sorted := append([]domain.EvoProposal(nil), proposals...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].UpdatedAt.Equal(sorted[j].UpdatedAt) {
			return sorted[i].UpdatedAt.After(sorted[j].UpdatedAt)
		}
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})

	for i := range sorted {
		if sorted[i].Status == domain.EvoProposalCanary {
			p := sorted[i]
			return domain.EvoSOPStageGray, &p, &p
		}
	}
	for i := range sorted {
		switch sorted[i].Status {
		case domain.EvoProposalDrafting, domain.EvoProposalValidating, domain.EvoProposalPending:
			p := sorted[i]
			return domain.EvoSOPStageOptimize, &p, &p
		}
	}
	last := sorted[0]
	return domain.EvoSOPStageMonitor, nil, &last
}

// BuildAssetDiffFile builds a compact unified diff for a single asset edit.
func BuildAssetDiffFile(kind, id, name, oldText, newText string) *domain.EvoSOPChangeFile {
	if strings.TrimSpace(oldText) == "" && strings.TrimSpace(newText) == "" {
		return nil
	}
	prefix := "assets/"
	switch kind {
	case domain.EvoAssetAgent:
		prefix = "roles/"
	case domain.EvoAssetRule:
		prefix = "rules/"
	}
	fileKey := strings.TrimSpace(id)
	if fileKey == "" {
		fileKey = strings.TrimSpace(name)
	}
	lines := compactUnifiedDiff(oldText, newText, 2, 120)
	if len(lines) == 0 {
		return nil
	}
	return &domain.EvoSOPChangeFile{
		Path:      prefix + fileKey + ".md",
		AssetKind: kind,
		AssetID:   id,
		AssetName: name,
		Lines:     lines,
	}
}

// BuildSOPChangeFiles extracts compact unified diff hunks for a proposal's modified asset.
func BuildSOPChangeFiles(p *domain.EvoProposal) []domain.EvoSOPChangeFile {
	if p == nil {
		return nil
	}
	file := BuildAssetDiffFile(p.AssetKind, p.AssetID, p.AssetName, p.BaseContent, p.CandidateContent)
	if file == nil {
		return nil
	}
	return []domain.EvoSOPChangeFile{*file}
}

// SplitSOPVersionsWithExternalEdits reflects external (chatbot / manual) role or rule
// edits on the SOP version trend timeline so runs before vs after the edit are directly
// comparable even when wf.Version itself did not increment.
func SplitSOPVersionsWithExternalEdits(
	wf domain.WorkflowDefinition,
	points []domain.EvoSOPVersionPoint,
	runs []domain.Run,
	evals map[string]domain.RunEvalRecord,
	edits []domain.EvoSOPExternalEdit,
	batchSize int,
) []domain.EvoSOPVersionPoint {
	if len(points) == 0 || len(edits) == 0 {
		return points
	}
	batchSize = NormalizeSOPBatchSize(batchSize)
	latestEdit := edits[0] // edits are sorted newest-first
	if latestEdit.CreatedAt.IsZero() {
		return points
	}
	currentVer := wf.Version
	if currentVer <= 0 {
		currentVer = 1
	}
	curIdx := -1
	for i := range points {
		if points[i].Current {
			curIdx = i
			break
		}
	}
	if curIdx < 0 {
		return points
	}

	var curRuns []domain.Run
	for _, r := range runs {
		if RunScore(r) < 0 {
			continue
		}
		v := r.WorkflowSnapshot.Version
		if v <= 0 {
			v = 1
		}
		if v == currentVer {
			curRuns = append(curRuns, r)
		}
	}

	var before, after []domain.Run
	for _, r := range curRuns {
		if !r.CreatedAt.Before(latestEdit.CreatedAt) {
			after = append(after, r)
		} else {
			before = append(before, r)
		}
	}
	sourceLabel := "手动修改"
	if latestEdit.Source == domain.EvoVersionChatbot {
		sourceLabel = "Chatbot 修改"
	}
	note := fmt.Sprintf("%s「%s」", sourceLabel, firstText(latestEdit.AssetName, latestEdit.AssetID))

	out := append([]domain.EvoSOPVersionPoint(nil), points...)
	if len(before) == 0 {
		// All runs on current version are after the edit (or no runs yet): annotate current point directly.
		out[curIdx].Source = latestEdit.Source
		out[curIdx].ChangeNote = note
		return out
	}

	sort.SliceStable(before, func(i, j int) bool { return before[i].CreatedAt.After(before[j].CreatedAt) })
	if len(before) > batchSize {
		before = before[:batchSize]
	}
	sort.SliceStable(after, func(i, j int) bool { return after[i].CreatedAt.After(after[j].CreatedAt) })
	if len(after) > batchSize {
		after = after[:batchSize]
	}

	out[curIdx].Current = false
	out[curIdx].Metrics = SummarizeSOPRuns(before, evals)
	tag := "手动"
	if latestEdit.Source == domain.EvoVersionChatbot {
		tag = "Chatbot"
	}
	revPoint := domain.EvoSOPVersionPoint{
		Version:    fmt.Sprintf("%s · %s", FormatSOPVersion(currentVer), tag),
		Current:    true,
		Source:     latestEdit.Source,
		ChangeNote: note,
		Metrics:    SummarizeSOPRuns(after, evals),
	}
	// Insert right after curIdx.
	res := make([]domain.EvoSOPVersionPoint, 0, len(out)+1)
	res = append(res, out[:curIdx+1]...)
	res = append(res, revPoint)
	res = append(res, out[curIdx+1:]...)
	return res
}

func compactUnifiedDiff(oldText, newText string, contextLines, maxLines int) [][2]string {
	a := splitNonEmptyEnd(oldText)
	b := splitNonEmptyEnd(newText)
	var raw [][2]string
	if len(a)*len(b) > 250000 {
		aSet := map[string]bool{}
		for _, l := range a {
			aSet[l] = true
		}
		bSet := map[string]bool{}
		for _, l := range b {
			bSet[l] = true
		}
		for _, l := range a {
			if !bSet[l] {
				raw = append(raw, [2]string{"del", l})
			}
		}
		for _, l := range b {
			if !aSet[l] {
				raw = append(raw, [2]string{"add", l})
			}
		}
	} else {
		n, m := len(a), len(b)
		dp := make([][]int, n+1)
		for i := range dp {
			dp[i] = make([]int, m+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if a[i] == b[j] {
					dp[i][j] = dp[i+1][j+1] + 1
				} else if dp[i+1][j] >= dp[i][j+1] {
					dp[i][j] = dp[i+1][j]
				} else {
					dp[i][j] = dp[i][j+1]
				}
			}
		}
		i, j := 0, 0
		for i < n && j < m {
			if a[i] == b[j] {
				raw = append(raw, [2]string{"ctx", a[i]})
				i++
				j++
			} else if dp[i+1][j] >= dp[i][j+1] {
				raw = append(raw, [2]string{"del", a[i]})
				i++
			} else {
				raw = append(raw, [2]string{"add", b[j]})
				j++
			}
		}
		for i < n {
			raw = append(raw, [2]string{"del", a[i]})
			i++
		}
		for j < m {
			raw = append(raw, [2]string{"add", b[j]})
			j++
		}
	}

	hasChange := false
	keep := make([]bool, len(raw))
	for idx, item := range raw {
		if item[0] != "ctx" {
			hasChange = true
			lo := max(0, idx-contextLines)
			hi := min(len(raw)-1, idx+contextLines)
			for k := lo; k <= hi; k++ {
				keep[k] = true
			}
		}
	}
	if !hasChange {
		return nil
	}
	out := make([][2]string, 0, min(len(raw), maxLines))
	for idx, item := range raw {
		if !keep[idx] {
			continue
		}
		out = append(out, item)
		if len(out) >= maxLines {
			break
		}
	}
	return out
}

func splitNonEmptyEnd(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(s, "\n")
}

// BuildSOPProblems converts SOP findings and run signals into user-facing problem items,
// prioritized by the SOP's selected optimization preferences.
func BuildSOPProblems(findings []domain.EvoFinding, signals []domain.EvoSignal, preferences []string, active *domain.EvoProposal) []domain.EvoSOPProblemItem {
	prefRank := map[string]int{}
	for i, p := range NormalizeSOPPreferences(preferences) {
		prefRank[p] = i + 1
	}

	fixHint := ""
	if active != nil && strings.TrimSpace(active.Title) != "" {
		fixHint = active.Title
		if active.Review != nil && len(active.Review.Changes) > 0 && strings.TrimSpace(active.Review.Changes[0].Title) != "" {
			fixHint = active.Review.Changes[0].Title
		}
	}

	type acc struct {
		item domain.EvoSOPProblemItem
		runs map[string]bool
	}
	byKey := map[string]*acc{}

	addItem := func(kind, title, detail, runID string, hits, runs int) {
		prefKey := sopSignalPrefKey(kind)
		k := kind + "|" + title
		a := byKey[k]
		if a == nil {
			a = &acc{
				item: domain.EvoSOPProblemItem{
					Key:    prefKey,
					Title:  domain.TrimEvoText(title, 120),
					Detail: domain.TrimEvoText(detail, 240),
					Fix:    fixHint,
				},
				runs: map[string]bool{},
			}
			byKey[k] = a
		}
		a.item.Hits += max(1, hits)
		if runs > a.item.Runs {
			a.item.Runs = runs
		}
		if runID != "" {
			a.runs[runID] = true
		}
	}

	for _, f := range findings {
		if len(f.Clusters) > 0 {
			for _, c := range f.Clusters {
				title := strings.TrimSpace(c.Label)
				if title == "" {
					title = f.Title
				}
				detail := fmt.Sprintf("涉及资产「%s」，近批次出现 %d 次（%d 次运行）", firstText(f.AssetName, f.AssetID), c.Count, c.Runs)
				addItem(c.Kind, title, detail, "", c.Count, c.Runs)
			}
			continue
		}
		if len(f.Signals) > 0 {
			for _, s := range f.Signals {
				addItem(s.Kind, firstText(s.Detail, f.Title), f.Summary, s.RunID, 1, 1)
			}
		}
	}

	if len(byKey) == 0 {
		for _, s := range signals {
			addItem(s.Kind, sopSignalLabel(s.Kind), s.Detail, s.RunID, 1, 1)
		}
	}

	items := make([]domain.EvoSOPProblemItem, 0, len(byKey))
	for _, a := range byKey {
		if len(a.runs) > a.item.Runs {
			a.item.Runs = len(a.runs)
		}
		items = append(items, a.item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := prefRank[items[i].Key], prefRank[items[j].Key]
		if (ri > 0) != (rj > 0) {
			return ri > 0
		}
		if ri != rj {
			return ri < rj
		}
		if items[i].Hits != items[j].Hits {
			return items[i].Hits > items[j].Hits
		}
		return items[i].Title < items[j].Title
	})
	if len(items) > 12 {
		items = items[:12]
	}
	return items
}

func sopSignalPrefKey(kind string) string {
	switch kind {
	case SignalSOPDetection:
		return domain.EvoPrefTools
	case SignalNodeFailed, SignalRework, SignalEvalLow, SignalUserFeedback:
		return domain.EvoPrefClean
	default:
		return domain.EvoPrefTokens
	}
}

func sopSignalLabel(kind string) string {
	switch kind {
	case SignalSOPDetection:
		return "SOP 轨迹效率异常"
	case SignalRework:
		return "节点打回返工"
	case SignalNodeFailed:
		return "节点执行失败"
	case SignalEvalLow:
		return "评测得分偏低"
	case SignalUserFeedback:
		return "人工反馈问题"
	default:
		return "执行效率异常"
	}
}

// LastRunSummaryNote produces the single-line analysis note shown below an SOP name.
func LastRunSummaryNote(run *domain.Run, signals []domain.EvoSignal, sopDetection *domain.RunEvalRecord) string {
	if run == nil {
		return "暂无运行记录"
	}
	if sopDetection != nil && sopDetection.Status == domain.RunEvalSucceeded && !sopDetection.Passed {
		if s := strings.TrimSpace(sopDetection.Result.Summary); s != "" {
			return "最近 1 次 · " + domain.TrimEvoText(s, 48)
		}
		return "最近 1 次 · SOP 检测发现效率问题"
	}
	seen := map[string]bool{}
	var labels []string
	for _, s := range signals {
		if s.RunID != run.ID {
			continue
		}
		label := sopSignalLabel(s.Kind)
		if !seen[label] {
			seen[label] = true
			labels = append(labels, label)
		}
	}
	if len(labels) > 0 {
		if len(labels) > 2 {
			labels = labels[:2]
		}
		return "最近 1 次 · " + strings.Join(labels, "、")
	}
	switch RunScore(*run) {
	case 2:
		return "最近 1 次 · 一次成功，未发现异常"
	case 1:
		return "最近 1 次 · 返工后成功"
	case 0:
		return "最近 1 次 · 运行失败"
	default:
		return "最近 1 次 · 进行中"
	}
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

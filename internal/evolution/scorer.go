package evolution

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

const (
	// MinBaselineCleanRuns is the minimum number of clean terminal runs needed
	// before an SOP's Layer 1 relative baseline is considered frozen/ready.
	MinBaselineCleanRuns = 3
	// MaxBaselineAnchorRuns caps the oldest clean-run window used to anchor a
	// frozen baseline so later optimized versions never drift the anchor.
	MaxBaselineAnchorRuns = 10

	// Neutral baseline dimension points for a clean run matching the SOP median:
	// control(25) + latency(12) + tools(12) + tokens(8) + recovery(10) + delivery(10) = 77.
	baselineNeutralLatency  = 12
	baselineNeutralTools    = 12
	baselineNeutralTokens   = 8
	baselineAnchorScore     = 77
	maxScoreControl         = 25
	maxScoreLatency         = 20
	maxScoreTools           = 20
	maxScoreTokens          = 15
	maxScoreRecovery        = 10
	maxScoreDelivery        = 10
	maxFailedProcessScore   = 20
	maxReworkedProcessScore = 72
)

// ComputeSOPScoreBaseline computes a deterministic per-SOP anchor baseline from
// the oldest clean terminal runs (so later optimized versions are always measured
// against the original anchor instead of a moving window).
func ComputeSOPScoreBaseline(runs []domain.Run, batchSize int) *domain.EvoSOPScoreBaseline {
	clean := make([]domain.Run, 0, len(runs))
	for _, r := range runs {
		if r.ExecutionContext.IsEvolutionReplay() {
			continue
		}
		if RunScore(r) == 2 {
			clean = append(clean, r)
		}
	}
	// Oldest first so v1/initial runs form the stable anchor.
	sort.SliceStable(clean, func(i, j int) bool {
		if !clean[i].CreatedAt.Equal(clean[j].CreatedAt) {
			return clean[i].CreatedAt.Before(clean[j].CreatedAt)
		}
		return clean[i].ID < clean[j].ID
	})

	window := NormalizeSOPBatchSize(batchSize)
	if window > MaxBaselineAnchorRuns {
		window = MaxBaselineAnchorRuns
	}
	if window < MinBaselineCleanRuns {
		window = MinBaselineCleanRuns
	}
	if len(clean) > window {
		clean = clean[:window]
	}

	b := &domain.EvoSOPScoreBaseline{
		ScorerVersion: domain.EvoScorerVersion,
		SampleRuns:    len(clean),
		BaselineReady: len(clean) >= MinBaselineCleanRuns,
		BaselineScore: baselineAnchorScore,
	}
	if len(clean) == 0 {
		return b
	}

	var tokens []float64
	var tools []float64
	var durs []float64
	var frozenAt time.Time
	for _, r := range clean {
		if t := RunTokens(r); t > 0 {
			tokens = append(tokens, float64(t))
		}
		if tc := RunToolCalls(r); tc > 0 {
			tools = append(tools, float64(tc))
		}
		if d := RunDurationMs(r); d > 0 {
			durs = append(durs, float64(d)/1000.0)
		}
		if r.CreatedAt.After(frozenAt) {
			frozenAt = r.CreatedAt
		}
	}
	if len(tokens) > 0 {
		b.MedianTokens = int64(math.Round(medianFloat64(tokens)))
	}
	if len(tools) > 0 {
		b.MedianTools = round1(medianFloat64(tools))
	}
	if len(durs) > 0 {
		b.MedianDurationSec = round1(medianFloat64(durs))
	}
	if b.BaselineReady {
		if frozenAt.IsZero() {
			frozenAt = time.Now().UTC()
		}
		b.FrozenAt = frozenAt
	}
	return b
}

// EffectiveSOPScoreBaseline returns the SOP's stored frozen baseline when valid
// for the current ScorerVersion, otherwise computes a stable read-only anchor
// from the SOP's oldest clean runs.
func EffectiveSOPScoreBaseline(policy domain.EvoSOPPolicy, runs []domain.Run) *domain.EvoSOPScoreBaseline {
	if policy.ScoreBaseline != nil && policy.ScoreBaseline.BaselineReady &&
		policy.ScoreBaseline.ScorerVersion == domain.EvoScorerVersion &&
		(policy.ScoreBaseline.MedianTokens > 0 || policy.ScoreBaseline.MedianTools > 0 || policy.ScoreBaseline.MedianDurationSec > 0) {
		cp := *policy.ScoreBaseline
		if cp.BaselineScore <= 0 {
			cp.BaselineScore = baselineAnchorScore
		}
		return &cp
	}
	return ComputeSOPScoreBaseline(runs, policy.BatchSize)
}

// ScoreRunProcess computes the deterministic Layer 1 pure-rule 0..100 process
// score across all 6 dimensions with event-level deduction/gain traceability.
// Same (run, baseline) always yields the exact same score and deductions.
func ScoreRunProcess(run domain.Run, baseline *domain.EvoSOPScoreBaseline) domain.EvoRunProcessScore {
	rs := RunScore(run)
	ready := baseline != nil && baseline.BaselineReady
	out := domain.EvoRunProcessScore{
		ScorerVersion: domain.EvoScorerVersion,
		BaselineReady: ready,
		Dimensions:    make(map[string]int, 6),
	}

	var deductions []domain.EvoScoreDeduction
	addDed := func(dim, label string, delta int, reason, nodeID, evRef string) {
		if delta == 0 && reason == "" {
			return
		}
		deductions = append(deductions, domain.EvoScoreDeduction{
			Dimension: dim,
			Label:     label,
			Delta:     delta,
			Reason:    reason,
			RunID:     run.ID,
			NodeID:    nodeID,
			EventRef:  evRef,
		})
	}

	// 1. Control flow (0..25): terminal status, rework count, retries.
	controlPts := maxScoreControl
	if rs <= 0 {
		controlPts = 4
		addDed("control", "运行失败", -(maxScoreControl - controlPts), fmt.Sprintf("运行终态为 %s，扣 %d 分", firstNonEmptyString(run.Status, "failed"), maxScoreControl-controlPts), "", "run.status")
	} else {
		nodeIDs := sortedRunNodeIDs(run)
		for _, id := range nodeIDs {
			nr := run.Nodes[id]
			if nr == nil {
				continue
			}
			if nr.ReworkCount > 0 {
				pen := min(16, nr.ReworkCount*8)
				controlPts -= pen
				addDed("control", "返工", -pen, fmt.Sprintf("节点「%s」返工 %d 次，扣 %d 分", nodeDisplayName(run, id), nr.ReworkCount, pen), id, fmt.Sprintf("node:%s:rework=%d", id, nr.ReworkCount))
			}
			if retries := nr.Attempt - 1 - nr.ReworkCount; retries > 0 {
				pen := min(10, retries*5)
				controlPts -= pen
				addDed("control", "重试", -pen, fmt.Sprintf("节点「%s」重试 %d 次，扣 %d 分", nodeDisplayName(run, id), retries, pen), id, fmt.Sprintf("node:%s:attempt=%d", id, nr.Attempt))
			}
		}
		if controlPts < 0 {
			controlPts = 0
		}
	}

	// 2. Latency (0..20): relative to frozen SOP median duration.
	latencyPts := 0
	if rs > 0 {
		durSec := float64(RunDurationMs(run)) / 1000.0
		if ready && baseline.MedianDurationSec > 0 && durSec > 0 {
			pts, delta := relativeDimScore(durSec, baseline.MedianDurationSec, baselineNeutralLatency, maxScoreLatency, 0.45, 1.0)
			latencyPts = pts
			if delta != 0 {
				pct := int(math.Round(math.Abs(durSec-baseline.MedianDurationSec) / baseline.MedianDurationSec * 100))
				if delta > 0 {
					addDed("latency", "提速", delta, fmt.Sprintf("耗时 %.1fs（较基线 %.1fs 降 %d%%），加 %d 分", durSec, baseline.MedianDurationSec, pct, delta), "", "timing.executionMs")
				} else {
					addDed("latency", "变慢", delta, fmt.Sprintf("耗时 %.1fs（较基线 %.1fs 增 %d%%），扣 %d 分", durSec, baseline.MedianDurationSec, pct, -delta), "", "timing.executionMs")
				}
			}
		} else {
			latencyPts = baselineNeutralLatency
		}
	}

	// 3. Tools (0..20): relative tool call count + telemetry duplicate/failure deductions.
	toolsPts := 0
	trace := BuildToolTraceSummary(run.Telemetry, "")
	if rs > 0 {
		tools := float64(RunToolCalls(run))
		if ready && baseline.MedianTools > 0 && tools > 0 {
			pts, delta := relativeDimScore(tools, baseline.MedianTools, baselineNeutralTools, maxScoreTools, 0.45, 1.0)
			toolsPts = pts
			if delta != 0 {
				pct := int(math.Round(math.Abs(tools-baseline.MedianTools) / baseline.MedianTools * 100))
				if delta > 0 {
					addDed("tools", "少工具", delta, fmt.Sprintf("工具调用 %.0f 次（较基线 %.1f 次降 %d%%），加 %d 分", tools, baseline.MedianTools, pct, delta), "", "run.toolCalls")
				} else {
					addDed("tools", "多工具", delta, fmt.Sprintf("工具调用 %.0f 次（较基线 %.1f 次增 %d%%），扣 %d 分", tools, baseline.MedianTools, pct, -delta), "", "run.toolCalls")
				}
			}
		} else {
			toolsPts = baselineNeutralTools
		}
		if trace != nil {
			if trace.DuplicateCalls > 0 {
				pen := min(8, trace.DuplicateCalls*2)
				toolsPts -= pen
				reason := fmt.Sprintf("重复工具调用 %d 次，扣 %d 分", trace.DuplicateCalls, pen)
				evRef := "tool.duplicate"
				if len(trace.Duplicates) > 0 {
					reason = fmt.Sprintf("重复工具调用（%s），扣 %d 分", trace.Duplicates[0], pen)
					evRef = trace.Duplicates[0]
				}
				addDed("tools", "重复调用", -pen, reason, "", evRef)
			}
			if trace.FailedCalls > 0 {
				pen := min(9, trace.FailedCalls*3)
				toolsPts -= pen
				reason := fmt.Sprintf("工具调用失败 %d 次，扣 %d 分", trace.FailedCalls, pen)
				evRef := "tool.failed"
				if len(trace.Failures) > 0 {
					reason = fmt.Sprintf("工具失败（%s），扣 %d 分", trace.Failures[0], pen)
					evRef = trace.Failures[0]
				}
				addDed("tools", "工具报错", -pen, reason, "", evRef)
			}
		}
		if toolsPts < 0 {
			toolsPts = 0
		}
	}

	// 4. Tokens (0..15): relative to frozen SOP median tokens.
	tokensPts := 0
	if rs > 0 {
		tok := float64(RunTokens(run))
		if ready && baseline.MedianTokens > 0 && tok > 0 {
			pts, delta := relativeDimScore(tok, float64(baseline.MedianTokens), baselineNeutralTokens, maxScoreTokens, 0.40, 1.0)
			tokensPts = pts
			if delta != 0 {
				pct := int(math.Round(math.Abs(tok-float64(baseline.MedianTokens)) / float64(baseline.MedianTokens) * 100))
				if delta > 0 {
					addDed("tokens", "省 Token", delta, fmt.Sprintf("Token %s（较基线 %s 降 %d%%），加 %d 分", formatCompactTokens(int64(tok)), formatCompactTokens(baseline.MedianTokens), pct, delta), "", "run.usage")
				} else {
					addDed("tokens", "耗 Token", delta, fmt.Sprintf("Token %s（较基线 %s 增 %d%%），扣 %d 分", formatCompactTokens(int64(tok)), formatCompactTokens(baseline.MedianTokens), pct, -delta), "", "run.usage")
				}
			}
		} else {
			tokensPts = baselineNeutralTokens
		}
	}

	// 5. Error recovery (0..10): repeated identical error signatures & gate failures.
	recoveryPts := maxScoreRecovery
	if rs <= 0 {
		recoveryPts = 2
	} else {
		errCounts := map[string]int{}
		for _, ev := range run.Telemetry {
			if ev.Error != nil && strings.TrimSpace(ev.Error.Message) != "" {
				sig := normalizeErrorSig(ev.Error.Message)
				errCounts[sig]++
			}
		}
		for _, id := range sortedRunNodeIDs(run) {
			nr := run.Nodes[id]
			if nr == nil {
				continue
			}
			if strings.TrimSpace(nr.Error) != "" {
				sig := normalizeErrorSig(nr.Error)
				errCounts[sig]++
			}
			if nr.GateResult != nil && (nr.GateResult.Decision == "fail" || nr.GateResult.Decision == "rework") {
				pen := 3
				recoveryPts -= pen
				addDed("recovery", "门禁未过", -pen, fmt.Sprintf("节点「%s」门禁结论为 %s，扣 %d 分", nodeDisplayName(run, id), nr.GateResult.Decision, pen), id, fmt.Sprintf("node:%s:gate", id))
			}
		}
		repeatErrs := make([]string, 0)
		for sig, c := range errCounts {
			if c >= 2 {
				repeatErrs = append(repeatErrs, sig)
			}
		}
		sort.Strings(repeatErrs)
		for _, sig := range repeatErrs {
			c := errCounts[sig]
			pen := min(6, (c-1)*3)
			recoveryPts -= pen
			addDed("recovery", "重复错误", -pen, fmt.Sprintf("相同错误重复 %d 次（%s），扣 %d 分", c, domain.TrimEvoText(sig, 48), pen), "", "telemetry.error")
		}
		if recoveryPts < 0 {
			recoveryPts = 0
		}
	}

	// 6. Delivery completeness (0..10): terminal status + declared artifacts present.
	deliveryPts := maxScoreDelivery
	if rs <= 0 {
		deliveryPts = 0
	} else {
		missing := missingDeclaredArtifacts(run)
		if len(missing) > 0 {
			pen := min(maxScoreDelivery, len(missing)*5)
			deliveryPts -= pen
			addDed("delivery", "缺产物", -pen, fmt.Sprintf("缺少声明产物 %s，扣 %d 分", strings.Join(missing, "、"), pen), "", "run.artifacts")
		}
	}

	out.Dimensions["control"] = controlPts
	out.Dimensions["latency"] = latencyPts
	out.Dimensions["tools"] = toolsPts
	out.Dimensions["tokens"] = tokensPts
	out.Dimensions["recovery"] = recoveryPts
	out.Dimensions["delivery"] = deliveryPts

	total := controlPts + latencyPts + toolsPts + tokensPts + recoveryPts + deliveryPts
	switch rs {
	case 0:
		if total > maxFailedProcessScore {
			total = maxFailedProcessScore
		}
	case 1:
		if total > maxReworkedProcessScore {
			total = maxReworkedProcessScore
		}
	}
	if total < 0 {
		total = 0
	}
	if total > 100 {
		total = 100
	}
	out.Score = total

	// Sort deductions: negative penalties first (most impactful first), then gains.
	sort.SliceStable(deductions, func(i, j int) bool {
		if (deductions[i].Delta < 0) != (deductions[j].Delta < 0) {
			return deductions[i].Delta < 0
		}
		ai, aj := absInt(deductions[i].Delta), absInt(deductions[j].Delta)
		if ai != aj {
			return ai > aj
		}
		return deductions[i].Dimension < deductions[j].Dimension
	})
	if len(deductions) > 12 {
		deductions = deductions[:12]
	}
	out.Deductions = deductions
	return out
}

// SummarizeSOPRunsWithScorer computes SOP execution metrics, Layer 1 static
// process scores against the SOP's frozen baseline (never overwritten by AI
// RunEval), and Layer 2 assertion pass rates when assertions are configured.
func SummarizeSOPRunsWithScorer(runs []domain.Run, baseline *domain.EvoSOPScoreBaseline, assertions []domain.EvoSOPAssertion) domain.EvoSOPMetrics {
	var m domain.EvoSOPMetrics
	var totalDurMs int64
	var durRuns int
	var scoreSum int
	var latestTrace []domain.EvoScoreDeduction

	activeAssertions := ActiveSOPAssertions(assertions)

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

		ps := ScoreRunProcess(run, baseline)
		scoreSum += ps.Score
		if len(latestTrace) == 0 && len(ps.Deductions) > 0 {
			latestTrace = ps.Deductions
		}

		if len(activeAssertions) > 0 {
			rep := EvaluateRunAssertions(run, activeAssertions)
			if rep.Configured {
				m.AssertionChecked++
				if rep.AllPassed {
					m.AssertionPassed++
				}
			}
		}
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

	avgScore := int(math.Round(float64(scoreSum) / float64(m.Runs)))
	m.Score = &avgScore
	m.ScoredRuns = m.Runs
	m.ScoreSource = "process_v1"
	m.ScorerVersion = domain.EvoScorerVersion
	if baseline != nil {
		m.BaselineReady = baseline.BaselineReady
		if baseline.BaselineReady && baseline.BaselineScore > 0 {
			bs := baseline.BaselineScore
			m.BaselineScore = &bs
		}
	}
	if m.AssertionChecked > 0 {
		rate := round4(float64(m.AssertionPassed) / float64(m.AssertionChecked))
		m.AssertionPassRate = &rate
	}
	if len(latestTrace) > 6 {
		latestTrace = latestTrace[:6]
	}
	m.Deductions = latestTrace
	return m
}

// BuildSOPVersionsWithScorer groups runs by workflow version and summarizes each
// version using the deterministic Layer 1 scorer and frozen SOP baseline.
func BuildSOPVersionsWithScorer(wf domain.WorkflowDefinition, runs []domain.Run, baseline *domain.EvoSOPScoreBaseline, assertions []domain.EvoSOPAssertion, batchSize int) []domain.EvoSOPVersionPoint {
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
			Metrics: SummarizeSOPRunsWithScorer(group, baseline, assertions),
		})
	}
	return points
}

// relativeDimScore maps a lower-is-better metric (duration/tools/tokens) relative
// to its SOP baseline median into [0, maxPts], returning (score, deltaFromNeutral).
func relativeDimScore(val, base float64, neutralPts, maxPts int, fullGainDrop, fullLossRise float64) (int, int) {
	if val <= 0 || base <= 0 {
		return neutralPts, 0
	}
	ratio := val / base
	var raw float64
	if ratio <= 1.0 {
		drop := 1.0 - ratio
		frac := math.Min(1.0, drop/fullGainDrop)
		raw = float64(neutralPts) + frac*float64(maxPts-neutralPts)
	} else {
		rise := ratio - 1.0
		frac := math.Min(1.0, rise/fullLossRise)
		raw = float64(neutralPts) * (1.0 - frac)
	}
	pts := int(math.Round(raw))
	if pts < 0 {
		pts = 0
	}
	if pts > maxPts {
		pts = maxPts
	}
	return pts, pts - neutralPts
}

func medianFloat64(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]float64(nil), vals...)
	sort.Float64s(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[mid]
	}
	return (cp[mid-1] + cp[mid]) / 2.0
}

func sortedRunNodeIDs(run domain.Run) []string {
	ids := make([]string, 0, len(run.Nodes))
	for id := range run.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func nodeDisplayName(run domain.Run, nodeID string) string {
	for _, n := range run.WorkflowSnapshot.Nodes {
		if n.ID == nodeID && strings.TrimSpace(n.Name) != "" {
			return strings.TrimSpace(n.Name)
		}
	}
	return nodeID
}

func missingDeclaredArtifacts(run domain.Run) []string {
	declared := map[string]bool{}
	for _, n := range run.WorkflowSnapshot.Nodes {
		for _, out := range n.ArtifactOutputs {
			name := strings.TrimSpace(out.Path)
			if name == "" {
				name = strings.TrimSpace(out.Name)
			}
			if name != "" {
				declared[name] = true
			}
		}
	}
	if len(declared) == 0 {
		return nil
	}
	var missing []string
	for want := range declared {
		if _, found := findRunArtifact(run, want); !found {
			missing = append(missing, want)
		}
	}
	sort.Strings(missing)
	return missing
}

func normalizeErrorSig(msg string) string {
	s := strings.TrimSpace(strings.ToLower(msg))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func formatCompactTokens(v int64) string {
	if v >= 1_000_000 {
		return fmt.Sprintf("%.2fM", float64(v)/1_000_000.0)
	}
	if v >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(v)/1_000.0)
	}
	return fmt.Sprintf("%d", v)
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

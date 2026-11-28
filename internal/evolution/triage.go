package evolution

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// Triage: is an asset's problem worth fixing? Each asset's problem rate on
// its current version gets a Beta-Binomial posterior whose prior is the
// project-wide rate (empirical Bayes), so few-run assets shrink toward the
// baseline instead of looking alarming after one bad day. Older runs decay
// so the queue drains by itself once an asset stops misbehaving.
const (
	// TriagePriorStrength is how many baseline-like runs the prior is worth.
	TriagePriorStrength = 10.0
	// TriageHalfLife halves the weight of a run every 7 days.
	TriageHalfLife = 7 * 24 * time.Hour
	// TriageFixProb: P(rate > baseline) needed to call it worth fixing.
	TriageFixProb = 0.9
	// TriageNoiseProb: below this the asset is indistinguishable from the baseline.
	TriageNoiseProb = 0.6
	// TriageMinWeeklyExcess: fixing must save at least this many problem runs a week.
	TriageMinWeeklyExcess = 1.0
	// TriageReworkWeight: a rework is half a problem run.
	TriageReworkWeight  = 0.5
	triageMaxDecideRuns = 300
	triageDefaultWindow = 14
	triageMaxTrendDays  = 30
)

func problemWeight(kind string) float64 {
	if kind == SignalRework {
		return TriageReworkWeight
	}
	return 1
}

func decayWeight(now, at time.Time) float64 {
	if at.IsZero() || !at.Before(now) {
		return 1
	}
	return math.Pow(0.5, float64(now.Sub(at))/float64(TriageHalfLife))
}

// betaCDF is the regularized incomplete beta function I_x(a, b).
func betaCDF(x, a, b float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	front := math.Exp(a*math.Log(x) + b*math.Log1p(-x) - lbeta(a, b))
	if x < (a+1)/(a+b+2) {
		return front * betaCF(x, a, b) / a
	}
	return 1 - front*betaCF(1-x, b, a)/b
}

// betaCF evaluates the continued fraction of the incomplete beta (Lentz).
func betaCF(x, a, b float64) float64 {
	const (
		maxIter = 300
		eps     = 1e-12
		tiny    = 1e-300
	)
	qab, qap, qam := a+b, a+1, a-1
	c, d := 1.0, 1-qab*x/qap
	if math.Abs(d) < tiny {
		d = tiny
	}
	d = 1 / d
	h := d
	for m := 1; m <= maxIter; m++ {
		fm := float64(m)
		m2 := 2 * fm
		aa := fm * (b - fm) * x / ((qam + m2) * (a + m2))
		d = 1 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		h *= d * c
		aa = -(a + fm) * (qab + fm) * x / ((a + m2) * (qap + m2))
		d = 1 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < eps {
			break
		}
	}
	return h
}

// betaQuantile inverts betaCDF by bisection.
func betaQuantile(p, a, b float64) float64 {
	lo, hi := 0.0, 1.0
	for i := 0; i < 60; i++ {
		mid := (lo + hi) / 2
		if betaCDF(mid, a, b) < p {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// triageStats are the decayed and raw tallies of one asset.
type triageStats struct {
	n, k       float64 // decayed exposure and problem weight
	runs       int     // raw exposure
	problem    float64 // raw problem weight
	stale      int
	kinds      map[string]int
	windowDays int
}

// TriageBaseline is the decayed project-wide problem rate, clamped so a
// spotless or disastrous project still leaves room for a signal.
func TriageBaseline(n, k float64) float64 {
	if n < 1 {
		return 0.1
	}
	return math.Min(0.9, math.Max(0.02, k/n))
}

func evalTriage(s triageStats, p0 float64) domain.EvoTriage {
	m := TriagePriorStrength
	a := m*p0 + s.k
	b := m*(1-p0) + math.Max(0, s.n-s.k)
	t := domain.EvoTriage{
		Runs: s.runs, StaleRuns: s.stale, ProblemRuns: round2(s.problem), Kinds: s.kinds,
		BaselineRate: round4(p0),
		Rate:         round4(a / (a + b)),
		RateLow:      round4(betaQuantile(0.05, a, b)),
		RateHigh:     round4(betaQuantile(0.95, a, b)),
		ProbWorse:    round4(1 - betaCDF(p0, a, b)),
	}
	days := s.windowDays
	if days <= 0 {
		days = triageDefaultWindow
	}
	t.WeeklyRuns = round2(float64(s.runs) / float64(days) * 7)
	t.WeeklyExcess = round2(math.Max(0, t.Rate-p0) * t.WeeklyRuns)
	t.Score = round4(t.ProbWorse * t.WeeklyExcess)
	switch {
	case t.ProbWorse >= TriageFixProb && t.WeeklyExcess >= TriageMinWeeklyExcess:
		t.Verdict = domain.EvoVerdictFix
		t.Reason = fmt.Sprintf("比项目平均差的概率 %s，每周约多 %.1f 次问题运行", pct(t.ProbWorse), t.WeeklyExcess)
	case t.ProbWorse >= TriageFixProb:
		t.Verdict = domain.EvoVerdictWatch
		t.Reason = fmt.Sprintf("大概率比平均差，但每周只多 %.1f 次问题运行，影响小", t.WeeklyExcess)
	case t.ProbWorse >= TriageNoiseProb:
		t.Verdict = domain.EvoVerdictWatch
		t.RunsToDecide = runsToDecide(a, b, p0)
		if t.RunsToDecide > 0 {
			t.Reason = fmt.Sprintf("证据还不够，按当前问题率再跑约 %d 次可判定", t.RunsToDecide)
		} else {
			t.Reason = "略高于项目平均，证据还不够"
		}
	default:
		t.Verdict = domain.EvoVerdictNoise
		t.Reason = "问题率与项目平均接近，可能是偶发"
	}
	return t
}

// runsToDecide: how many more runs at the posterior-mean rate until
// P(rate > p0) reaches TriageFixProb; 0 when the mean is not above p0 or it
// takes more than triageMaxDecideRuns.
func runsToDecide(a, b, p0 float64) int {
	mean := a / (a + b)
	if mean <= p0 {
		return 0
	}
	reach := func(r int) bool {
		fr := float64(r)
		return 1-betaCDF(p0, a+fr*mean, b+fr*(1-mean)) >= TriageFixProb
	}
	if !reach(triageMaxDecideRuns) {
		return 0
	}
	lo, hi := 0, triageMaxDecideRuns
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		if reach(mid) {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

func verdictSeverity(verdict string) string {
	switch verdict {
	case domain.EvoVerdictFix:
		return "high"
	case domain.EvoVerdictWatch:
		return "medium"
	}
	return "low"
}

func verdictRank(verdict string) int {
	switch verdict {
	case domain.EvoVerdictFix:
		return 3
	case domain.EvoVerdictWatch:
		return 2
	}
	return 1
}

var (
	clusterDigits = regexp.MustCompile(`[0-9]+`)
	clusterSpace  = regexp.MustCompile(`\s+`)
)

// clusterLabel normalises a signal detail so repeats of one error group
// together: first line, numbers masked, whitespace collapsed.
func clusterLabel(detail string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(detail), "\n", 2)[0])
	if line == "" {
		return "（无详情）"
	}
	line = clusterDigits.ReplaceAllString(line, "#")
	line = clusterSpace.ReplaceAllString(line, " ")
	return domain.TrimEvoText(line, 80)
}

func buildClusters(signals []domain.EvoSignal, now time.Time, days int) []domain.EvoSignalCluster {
	type acc struct {
		c    domain.EvoSignalCluster
		runs map[string]domain.EvoClusterOccurrence
	}
	byKey := map[string]*acc{}
	for _, sig := range signals {
		if sig.RunID == "" {
			continue
		}
		label := clusterLabel(sig.Detail)
		key := sig.Kind + "|" + sig.NodeID + "|" + strings.ToLower(label)
		x := byKey[key]
		if x == nil {
			x = &acc{c: domain.EvoSignalCluster{ID: domain.ContentHash(key), Kind: sig.Kind, NodeID: sig.NodeID, Label: label}, runs: map[string]domain.EvoClusterOccurrence{}}
			byKey[key] = x
		}
		at := sig.CreatedAt
		if at.IsZero() {
			at = now
		}
		x.c.Count++
		if x.c.FirstSeenAt.IsZero() || at.Before(x.c.FirstSeenAt) {
			x.c.FirstSeenAt = at
		}
		o := x.runs[sig.RunID]
		o.Count++
		if o.RunID == "" || at.After(o.CreatedAt) || (at.Equal(o.CreatedAt) && sig.Detail < o.Detail) {
			o.RunID, o.Detail, o.CreatedAt = sig.RunID, sig.Detail, at
		}
		x.runs[sig.RunID] = o
	}
	out := make([]domain.EvoSignalCluster, 0, len(byKey))
	for _, x := range byKey {
		weights, times := map[string]float64{}, map[string]time.Time{}
		for id, o := range x.runs {
			x.c.Occurrences = append(x.c.Occurrences, o)
			weights[id], times[id] = 1, o.CreatedAt
		}
		sort.Slice(x.c.Occurrences, func(i, j int) bool {
			a, b := x.c.Occurrences[i], x.c.Occurrences[j]
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.After(b.CreatedAt)
			}
			return a.RunID < b.RunID
		})
		x.c.Runs = len(x.c.Occurrences)
		x.c.RunID, x.c.LastSeenAt = x.c.Occurrences[0].RunID, x.c.Occurrences[0].CreatedAt
		x.c.Trend = buildTrend(now, days, weights, times)
		out = append(out, x.c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runs != out[j].Runs {
			return out[i].Runs > out[j].Runs
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// buildTrend buckets problem runs by day, oldest first; today is last.
func buildTrend(now time.Time, days int, problems map[string]float64, at map[string]time.Time) []float64 {
	if days <= 0 {
		days = triageDefaultWindow
	}
	days = min(days, triageMaxTrendDays)
	out := make([]float64, days)
	today := now.Truncate(24 * time.Hour)
	for id, w := range problems {
		t := at[id]
		if t.IsZero() {
			t = now
		}
		ago := int(today.Sub(t.Truncate(24*time.Hour)) / (24 * time.Hour))
		if ago < 0 {
			ago = 0
		}
		if ago < days {
			out[days-1-ago] += w
		}
	}
	return out
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

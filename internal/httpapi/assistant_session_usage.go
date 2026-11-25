package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// CodeBuddy tokens and dollars use session cursors. Claude tokens are scoped
// to the current turn while dollars include resumed history. Keep those scopes
// separate and prefer CodeBuddy's versioned turn-token snapshot when available.
type assistantSessionUsage struct {
	path, session, provider string
	resume                  bool
	previous                *assistantUsageCursor
	cumulative              *domain.TokenUsage
	turn                    *domain.TokenUsage
}

type assistantUsageCursor struct {
	Version int               `json:"version"`
	Session string            `json:"session"`
	Usage   domain.TokenUsage `json:"usage"`
}

func newAssistantSessionUsage(root, session string, resume bool, provider string) *assistantSessionUsage {
	hash := sha256.Sum256([]byte(session))
	c := &assistantSessionUsage{path: filepath.Join(root, provider+"-usage-"+hex.EncodeToString(hash[:])+".json"), session: session, resume: resume, provider: provider}
	var previous assistantUsageCursor
	if data, err := os.ReadFile(c.path); resume && err == nil && json.Unmarshal(data, &previous) == nil && previous.Version == 1 {
		c.previous = &previous
		c.session = previous.Session
	}
	return c
}

func (c *assistantSessionUsage) result(session string, usage map[string]any, cost *float64, meta json.RawMessage) {
	if session != "" {
		c.session = session
	}
	c.turn = nil
	if usage != nil || cost != nil {
		u := runtime.ParseTokenUsage(usage)
		// CodeBuddy currently serializes total_cost_usd:0 as a compatibility
		// placeholder. Credits are separate; this zero is not a monetary bill.
		if cost != nil && (c.provider != assistantEngineCodeBuddy || *cost != 0) {
			u.CostUSD, u.CostStatus = *cost, "reported"
		}
		if c.provider == assistantEngineClaude && c.previous != nil && c.previous.Session == c.session && u.CostUSD < c.previous.Usage.CostUSD {
			// Crash results can zero the monetary counter. It is no longer a
			// usable baseline; retain turn tokens for the public-price fallback.
			u.CostUSD, u.CostStatus = 0, ""
		}
		c.cumulative = &u
		if c.provider == assistantEngineClaude && usage != nil {
			turn := u
			turn.CostUSD, turn.CostStatus = 0, ""
			if assistantUsageReported(turn) {
				c.turn = &turn // Claude tokens are turn-scoped; dollars are cumulative.
			}
		}
	}
	if c.provider != assistantEngineCodeBuddy {
		return
	}
	var snapshot struct {
		Version int    `json:"version"`
		Scope   string `json:"scope"`
		Kind    string `json:"kind"`
		Total   struct {
			Usage map[string]any `json:"usage"`
		} `json:"total"`
	}
	if json.Unmarshal(meta, &snapshot) == nil && snapshot.Version == 1 && snapshot.Scope == "turn" && snapshot.Kind == "snapshot" && snapshot.Total.Usage != nil {
		u := runtime.ParseTokenUsage(snapshot.Total.Usage)
		c.turn = &u // An explicit zero snapshot also overrides cumulative usage.
	}
}

func (c *assistantSessionUsage) finish(messages domain.TokenUsage) domain.TokenUsage {
	if c.cumulative != nil {
		cursor := assistantUsageCursor{Version: 1, Session: c.session, Usage: *c.cumulative}
		if err := c.save(cursor); err != nil {
			log.Printf("assistant %s usage cursor could not be saved: %v", c.provider, err)
			// Do not bill a value that the next invocation cannot deduplicate.
			return domain.TokenUsage{}
		}
	} else if c.turn != nil || assistantUsageReported(messages) {
		// A partial invocation was charged from message/turn facts. Without a
		// final cumulative value the old cursor would charge that usage again.
		if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
			log.Printf("assistant %s usage cursor could not be invalidated: %v", c.provider, err)
			return domain.TokenUsage{}
		}
	}
	var billed *domain.TokenUsage
	if c.cumulative != nil && !c.resume {
		billed = c.cumulative
	} else if c.cumulative != nil && c.previous != nil && c.previous.Session == c.session {
		if c.provider == assistantEngineClaude {
			if amount, ok := reportedSessionCostDelta(*c.cumulative, c.previous.Usage); ok {
				delta := domain.TokenUsage{}
				delta.CostUSD, delta.CostStatus = amount, "reported"
				billed = &delta
			}
		} else if delta, ok := codeBuddyUsageDelta(*c.cumulative, c.previous.Usage); ok {
			billed = &delta
		}
	}
	if c.turn != nil {
		out := *c.turn
		// Token snapshots and the monetary session cursor have separate
		// scopes; use the former's tokens and the latter's dollar delta.
		if billed != nil {
			out.CostUSD, out.CostStatus = billed.CostUSD, billed.CostStatus
		}
		return out
	}
	if billed != nil {
		return *billed
	}
	// A missing/reset cursor cannot separate historical tokens from this turn.
	// Per-message facts are safe; otherwise leave the turn unmetered and use
	// this result only as the baseline for the next invocation.
	return messages
}

func codeBuddyUsageDelta(current, previous domain.TokenUsage) (domain.TokenUsage, bool) {
	var out domain.TokenUsage
	for _, field := range []struct {
		current, previous int64
		dst               *int64
	}{
		{current.InputTokens, previous.InputTokens, &out.InputTokens},
		{current.OutputTokens, previous.OutputTokens, &out.OutputTokens},
		{current.CacheReadTokens, previous.CacheReadTokens, &out.CacheReadTokens},
		{current.CacheWriteTokens, previous.CacheWriteTokens, &out.CacheWriteTokens},
		{current.ReasoningTokens, previous.ReasoningTokens, &out.ReasoningTokens},
		{current.TotalTokens, previous.TotalTokens, &out.TotalTokens},
	} {
		if field.current < field.previous || field.previous < 0 {
			return domain.TokenUsage{}, false
		}
		*field.dst = field.current - field.previous
	}
	if amount, ok := reportedSessionCostDelta(current, previous); ok {
		out.CostUSD, out.CostStatus = amount, "reported"
	}
	return out, true
}

func reportedSessionCostDelta(current, previous domain.TokenUsage) (float64, bool) {
	if current.CostStatus != "reported" || previous.CostStatus != "reported" || previous.CostUSD < 0 || current.CostUSD < previous.CostUSD || math.IsNaN(current.CostUSD) || math.IsNaN(previous.CostUSD) || math.IsInf(current.CostUSD, 0) || math.IsInf(previous.CostUSD, 0) {
		return 0, false
	}
	return current.CostUSD - previous.CostUSD, true
}

func (c *assistantSessionUsage) save(cursor assistantUsageCursor) error {
	f, err := os.CreateTemp(filepath.Dir(c.path), c.provider+"-usage-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(cursor)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), c.path)
}

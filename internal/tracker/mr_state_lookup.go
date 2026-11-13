package tracker

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

const mrStateTTL = 2 * time.Minute

type mrCacheEntry struct {
	state        string
	targetBranch string
	mergedAt     time.Time
	at           time.Time
}

func (s *Service) lookupMRState(client *MCPClient, project string, iid int) (state, targetBranch string, mergedAt time.Time) {
	cacheKey := project + "#" + strconv.Itoa(iid)
	if raw, ok := s.mrCache.Load(cacheKey); ok {
		if entry, ok := raw.(mrCacheEntry); ok && time.Since(entry.at) < mrStateTTL {
			return entry.state, entry.targetBranch, entry.mergedAt
		}
	}
	state = "unknown"
	if client == nil {
		client = s.gongfeng
	}
	if client != nil {
		raw, err := client.CallTool("search_merge_request", map[string]any{
			"project_id": project,
			"iid":        iid,
			"per_page":   1,
		})
		if err == nil {
			if parsed, target, at := parseGongfengMRDetails(raw, iid); parsed != "" {
				state = parsed
				targetBranch = target
				mergedAt = at
			}
		}
	}
	s.mrCache.Store(cacheKey, mrCacheEntry{state: state, targetBranch: targetBranch, mergedAt: mergedAt, at: time.Now()})
	return state, targetBranch, mergedAt
}

func parseGongfengMRState(raw string, wantIID int) string {
	state, _ := parseGongfengMRStatus(raw, wantIID)
	return state
}

func parseGongfengMRStatus(raw string, wantIID int) (state, targetBranch string) {
	state, targetBranch, _ = parseGongfengMRDetails(raw, wantIID)
	return state, targetBranch
}

func parseGongfengMRDetails(raw string, wantIID int) (state, targetBranch string, mergedAt time.Time) {
	try := func(items []gongfengListedMR) (string, string, time.Time) {
		for _, item := range items {
			if wantIID > 0 && item.IID != 0 && item.IID != wantIID {
				continue
			}
			if parsed := normalizeMRState(item.State); parsed != "" {
				return parsed, strings.TrimSpace(item.TargetBranch), gongfengMRMergedAt(item)
			}
		}
		return "", "", time.Time{}
	}
	visitGongfengMRPayload(raw, func(items []gongfengListedMR) bool {
		state, targetBranch, mergedAt = try(items)
		return state != ""
	})
	return state, targetBranch, mergedAt
}

func parseGongfengSourceCommit(raw string, wantIID int) string {
	type mr struct {
		IID          int    `json:"iid"`
		SourceCommit string `json:"source_commit"`
	}
	var found string
	try := func(items []mr) {
		for _, item := range items {
			if wantIID > 0 && item.IID != 0 && item.IID != wantIID {
				continue
			}
			if sha := strings.TrimSpace(item.SourceCommit); sha != "" {
				found = sha
				return
			}
		}
	}
	var wrapped struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal([]byte(raw), &wrapped) == nil && len(wrapped.Data) > 0 {
		var list []mr
		if json.Unmarshal(wrapped.Data, &list) == nil {
			try(list)
		}
		if found == "" {
			var one mr
			if json.Unmarshal(wrapped.Data, &one) == nil {
				try([]mr{one})
			}
		}
	}
	if found == "" {
		var list []mr
		if json.Unmarshal([]byte(raw), &list) == nil {
			try(list)
		}
	}
	return found
}

func parseGongfengRefNames(raw string) ([]string, bool) {
	type ref struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	collect := func(items []ref) ([]string, bool) {
		names := make([]string, 0, len(items))
		for _, item := range items {
			name := strings.TrimSpace(item.Name)
			if name == "" {
				continue
			}
			if item.Type != "" && !strings.EqualFold(item.Type, "branch") {
				continue
			}
			names = append(names, name)
		}
		return names, true
	}
	var wrapped struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal([]byte(raw), &wrapped) == nil && len(wrapped.Data) > 0 {
		var list []ref
		if json.Unmarshal(wrapped.Data, &list) == nil {
			return collect(list)
		}
	}
	var list []ref
	if json.Unmarshal([]byte(raw), &list) == nil {
		return collect(list)
	}
	return nil, false
}

func normalizeMRState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "merged", "opened", "reopened", "closed":
		return strings.ToLower(strings.TrimSpace(state))
	default:
		return ""
	}
}

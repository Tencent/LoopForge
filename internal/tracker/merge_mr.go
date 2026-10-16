package tracker

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// ErrNoOpenMR reports that no open MR exists for the requested source branch.
// Callers treat it as a normal outcome (the SOP may deliver without an MR).
var ErrNoOpenMR = errors.New("no open MR for source branch")

// MergeSourceBranchMR merges the first open Gongfeng MR whose source branch is
// sourceBranch. Prefer EnsureAndMergeBranchMR when the target branch is known.
func (s *Service) MergeSourceBranchMR(project, sourceBranch string) (string, error) {
	return s.EnsureAndMergeBranchMR(project, sourceBranch, "", "", "")
}

// EnsureAndMergeBranchMR finds or creates a Gongfeng MR from source→target and
// merges it with a merge commit. The source branch is kept. An empty target
// only merges an already-open MR for the source (no create). Same source and
// target is a no-op.
func (s *Service) EnsureAndMergeBranchMR(project, sourceBranch, targetBranch, title, description string) (string, error) {
	if strings.HasPrefix(project, "github:") {
		return s.githubMergeBranch(project, sourceBranch, targetBranch, title, description)
	}
	project = strings.TrimSpace(project)
	src := strings.TrimSpace(sourceBranch)
	tgt := strings.TrimSpace(targetBranch)
	if src == "" {
		return "", errors.New("source branch is required")
	}
	if tgt != "" && src == tgt {
		return "", nil
	}
	if tgt != "" && project == "" {
		return "", errors.New("project, source branch and target branch are required")
	}
	client, _, err := s.gongfengClientForRemoteProject(project)
	if err != nil {
		return "", err
	}

	globalID, iid, found, searchErr := s.findOpenBranchMR(client, project, src, tgt)
	if searchErr != nil {
		return "", searchErr
	}
	if !found {
		if tgt == "" {
			return "", ErrNoOpenMR
		}
		createTitle := strings.TrimSpace(title)
		if createTitle == "" {
			createTitle = fmt.Sprintf("AgentFlow: %s → %s", src, tgt)
		}
		createDesc := strings.TrimSpace(description)
		if createDesc == "" {
			createDesc = fmt.Sprintf("定时 SOP 将 `%s` 合入 `%s`。", src, tgt)
		}
		_, createErr := client.Clone(30*time.Second).CallTool("create_merge_request", map[string]any{
			"project_id":    project,
			"source_branch": src,
			"target_branch": tgt,
			"title":         createTitle,
			"description":   createDesc,
		})
		// Search again even when create returned an error: another instance may
		// have won the create race and left the reusable MR open.
		globalID, iid, found, searchErr = s.findOpenBranchMR(client, project, src, tgt)
		if searchErr != nil {
			return "", searchErr
		}
		if !found {
			if createErr != nil {
				return "", fmt.Errorf("create MR %s → %s: %w", src, tgt, createErr)
			}
			return "", fmt.Errorf("create_merge_request did not return an id")
		}
	}
	mergeArgs := map[string]any{
		"project_id":       project,
		"merge_request_id": globalID,
		"merge_type":       "merge",
	}
	if msg := strings.TrimSpace(title); msg != "" {
		mergeArgs["merge_commit_message"] = msg
	}
	if _, err := client.Clone(60*time.Second).CallTool("merge_merge_request", mergeArgs); err != nil {
		return "", fmt.Errorf("merge MR !%d: %w", iid, err)
	}
	return "!" + strconv.Itoa(iid), nil
}

func (s *Service) findOpenBranchMR(client *MCPClient, project, sourceBranch, targetBranch string) (globalID, iid int, found bool, err error) {
	args := map[string]any{
		"project_id":    project,
		"source_branch": sourceBranch,
		"state":         "opened",
		"per_page":      5,
	}
	if targetBranch != "" {
		args["target_branch"] = targetBranch
	}
	raw, err := client.Clone(30*time.Second).CallTool("search_merge_request", args)
	if err != nil {
		return 0, 0, false, fmt.Errorf("search MR for %s: %w", sourceBranch, err)
	}
	globalID, iid, found = parseGongfengOpenMR(raw, targetBranch)
	return globalID, iid, found, nil
}

// parseGongfengMROpenID extracts the (global id, iid) pair of the first open
// MR in a search_merge_request response. The MCP payload shape varies, so it
// mirrors parseGongfengMRState's layered decoding.
func parseGongfengMROpenID(raw string) (globalID, iid int, ok bool) {
	return parseGongfengOpenMR(raw, "")
}

type gongfengListedMR struct {
	ID           int    `json:"id"`
	IID          int    `json:"iid"`
	State        string `json:"state"`
	TargetBranch string `json:"target_branch"`
	SourceBranch string `json:"source_branch"`
	MergedAt     string `json:"merged_at"`
	ResolvedAt   string `json:"resolved_at"`
	UpdatedAt    string `json:"updated_at"`
	WebURL       string `json:"web_url"`
}

// FindBranchMR returns the delivery MR whose source is sourceBranch: the
// newest non-closed one, else the newest closed one. It is the authoritative
// MR of a run repository; agents' MR links in artifacts are only a fallback.
func (s *Service) FindBranchMR(project, sourceBranch string) (domain.DeliveryMR, bool, error) {
	if strings.HasPrefix(project, "github:") {
		return s.githubFindBranch(project, sourceBranch)
	}
	project = strings.TrimSpace(project)
	src := strings.TrimSpace(sourceBranch)
	if project == "" || src == "" {
		return domain.DeliveryMR{}, false, nil
	}
	client, _, err := s.gongfengClientForRemoteProject(project)
	if err != nil {
		return domain.DeliveryMR{}, false, err
	}
	raw, err := client.Clone(15*time.Second).CallTool("search_merge_request", map[string]any{
		"project_id":    project,
		"source_branch": src,
		"per_page":      20,
	})
	if err != nil {
		return domain.DeliveryMR{}, false, fmt.Errorf("search MR for %s: %w", src, err)
	}
	item, ok := pickBranchMR(parseGongfengMRRecords(raw), src)
	if !ok {
		return domain.DeliveryMR{}, false, nil
	}
	url := strings.TrimSpace(item.WebURL)
	if _, _, parsed, ok := domain.ParseGongfengMR(url); ok {
		url = parsed
	} else {
		url = domain.GongfengMRURL(project, item.IID)
	}
	return domain.DeliveryMR{
		Project:       project,
		IID:           item.IID,
		URL:           url,
		TargetBranch:  item.TargetBranch,
		GongfengState: normalizeMRState(item.State),
	}, true, nil
}

func pickBranchMR(items []gongfengListedMR, sourceBranch string) (gongfengListedMR, bool) {
	var best gongfengListedMR
	found, bestClosed := false, false
	for _, item := range items {
		if s := strings.TrimSpace(item.SourceBranch); s != "" && !domain.SameGitBranch(s, sourceBranch) {
			continue
		}
		closed := normalizeMRState(item.State) == "closed"
		switch {
		case !found:
		case bestClosed && !closed:
		case bestClosed == closed && item.IID > best.IID:
		default:
			continue
		}
		best, found, bestClosed = item, true, closed
	}
	return best, found
}

func parseGongfengMRRecords(raw string) []gongfengListedMR {
	var out []gongfengListedMR
	seen := map[int]struct{}{}
	add := func(items []gongfengListedMR) {
		for _, item := range items {
			if item.IID <= 0 {
				continue
			}
			if _, ok := seen[item.IID]; ok {
				continue
			}
			seen[item.IID] = struct{}{}
			out = append(out, item)
		}
	}
	var wrapped struct {
		Data  json.RawMessage    `json:"data"`
		Items []gongfengListedMR `json:"items"`
	}
	if json.Unmarshal([]byte(raw), &wrapped) == nil {
		var list []gongfengListedMR
		if json.Unmarshal(wrapped.Data, &list) == nil {
			add(list)
		}
		var one gongfengListedMR
		if json.Unmarshal(wrapped.Data, &one) == nil {
			add([]gongfengListedMR{one})
		}
		add(wrapped.Items)
	}
	var list []gongfengListedMR
	if json.Unmarshal([]byte(raw), &list) == nil {
		add(list)
	}
	var one gongfengListedMR
	if json.Unmarshal([]byte(raw), &one) == nil {
		add([]gongfengListedMR{one})
	}
	return out
}

func parseGongfengTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.000-0700",
		"2006-01-02T15:04:05-0700",
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func parseGongfengOpenMR(raw, wantTarget string) (globalID, iid int, ok bool) {
	first := func(items []gongfengListedMR) (int, int, bool) {
		want := strings.TrimSpace(wantTarget)
		for _, item := range items {
			switch normalizeMRState(item.State) {
			case "opened", "reopened":
				if want != "" && strings.TrimSpace(item.TargetBranch) != "" && !strings.EqualFold(strings.TrimSpace(item.TargetBranch), want) {
					continue
				}
				return item.ID, item.IID, item.ID != 0 || item.IID != 0
			}
		}
		return 0, 0, false
	}
	return decodeGongfengMRList(raw, first)
}

func decodeGongfengMRList(raw string, first func([]gongfengListedMR) (int, int, bool)) (globalID, iid int, ok bool) {
	var wrapped struct {
		Data  json.RawMessage    `json:"data"`
		Items []gongfengListedMR `json:"items"`
	}
	if json.Unmarshal([]byte(raw), &wrapped) == nil {
		var list []gongfengListedMR
		if json.Unmarshal(wrapped.Data, &list) == nil {
			if id, miid, found := first(list); found {
				return id, miid, true
			}
		}
		var one gongfengListedMR
		if json.Unmarshal(wrapped.Data, &one) == nil {
			if id, miid, found := first([]gongfengListedMR{one}); found {
				return id, miid, true
			}
		}
		if id, miid, found := first(wrapped.Items); found {
			return id, miid, true
		}
	}
	var list []gongfengListedMR
	if json.Unmarshal([]byte(raw), &list) == nil {
		if id, miid, found := first(list); found {
			return id, miid, true
		}
	}
	var one gongfengListedMR
	if json.Unmarshal([]byte(raw), &one) == nil {
		if id, miid, found := first([]gongfengListedMR{one}); found {
			return id, miid, true
		}
	}
	return 0, 0, false
}

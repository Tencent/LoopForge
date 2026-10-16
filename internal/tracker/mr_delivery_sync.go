package tracker

import (
	"log"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

const (
	mrDeliverySyncInterval = 5 * time.Minute
	mrDeliveryStaleAfter   = 5 * time.Minute
)

// MRDeliveryStore is the persistence surface used by the MR state background sync.
type MRDeliveryStore interface {
	DiscoverMRDeliveryStatesFromArtifacts() error
	MRDeliveryStatesNeedingRefresh(staleBefore time.Time) ([]domain.MRDeliveryState, error)
	UpdateMRDeliveryState(project string, iid int, state string, targetBranch string, checkedAt time.Time) error
	ReconcileMergedMRDeliveries() error
	ReconcileCompletedDeliveries() error
	ReconcileUnlinkedDeliveryMRs() error
	ReconcileDeliveryMRStates() error
	Repositories() []domain.Repository
	EnsureMRDeliveryState(project string, iid int, url, issueID string) error
	MergedWorkMRs(project string) ([]domain.MRDeliveryState, error)
	ApplyDefaultBranchLanding(issueID, mrURL string, onDefault bool) error
}

// StartMRStateSync launches a background loop that discovers MR links and
// refreshes merge states from Gongfeng MCP into MySQL.
func (s *Service) StartMRStateSync(store MRDeliveryStore) {
	if s == nil || store == nil {
		return
	}
	s.autoMu.Lock()
	if s.mrTicker != nil {
		s.autoMu.Unlock()
		return
	}
	s.mrTicker = time.NewTicker(mrDeliverySyncInterval)
	s.mrStopCh = make(chan struct{})
	ticker, stopCh := s.mrTicker, s.mrStopCh
	s.autoMu.Unlock()

	go func() {
		s.syncMRDeliveryStates(store)
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				s.syncMRDeliveryStates(store)
			}
		}
	}()
}

// StopMRStateSync halts the MR delivery background sync.
func (s *Service) StopMRStateSync() {
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	if s.mrTicker != nil {
		s.mrTicker.Stop()
		s.mrTicker = nil
	}
	if s.mrStopCh != nil {
		close(s.mrStopCh)
		s.mrStopCh = nil
	}
}

// SyncMRDeliveryStatesOnce runs discovery + refresh immediately (for tests/admin).
func (s *Service) SyncMRDeliveryStatesOnce(store MRDeliveryStore) {
	if s == nil || store == nil {
		return
	}
	s.syncMRDeliveryStates(store)
}

func (s *Service) syncMRDeliveryStates(store MRDeliveryStore) {
	if store == nil {
		return
	}
	s.deliveryReconcileOnce.Do(func() {
		if err := store.ReconcileMergedMRDeliveries(); err != nil {
			log.Printf("mr-delivery reconcile: %v", err)
		}
		if err := store.ReconcileCompletedDeliveries(); err != nil {
			log.Printf("delivery reconcile: %v", err)
		}
		if err := store.ReconcileUnlinkedDeliveryMRs(); err != nil {
			log.Printf("delivery unlinked-mr reconcile: %v", err)
		}
	})
	if err := store.ReconcileDeliveryMRStates(); err != nil {
		log.Printf("delivery mr-state reconcile: %v", err)
	}
	if err := store.DiscoverMRDeliveryStatesFromArtifacts(); err != nil {
		log.Printf("mr-delivery discover: %v", err)
		return
	}
	s.discoverPromoteMRs(store)
	s.syncWorkCommitsOnDefaultBranch(store)
	staleBefore := time.Now().UTC().Add(-mrDeliveryStaleAfter)
	pending, err := store.MRDeliveryStatesNeedingRefresh(staleBefore)
	if err != nil {
		log.Printf("mr-delivery list: %v", err)
		return
	}
	if len(pending) == 0 {
		return
	}
	now := time.Now().UTC()
	updated := 0
	for _, row := range pending {
		if strings.HasPrefix(row.MRProject, "github:") {
			if err := s.refreshGitHubDelivery(store, row.MRProject, row.MRIID); err != nil {
				log.Printf("GitHub delivery refresh: %v", err)
			} else {
				updated++
			}
			continue
		}
		client, _, clientErr := s.gongfengClientForRemoteProject(row.MRProject)
		if clientErr != nil {
			log.Printf("mr-delivery auth %s: %v", row.MRProject, clientErr)
			continue
		}
		client = client.Clone(5 * time.Second)
		state, target := s.lookupMRState(client, row.MRProject, row.MRIID)
		if state == "" {
			state = "unknown"
		}
		if err := store.UpdateMRDeliveryState(row.MRProject, row.MRIID, state, target, now); err != nil {
			log.Printf("mr-delivery update %s!%d: %v", row.MRProject, row.MRIID, err)
			continue
		}
		updated++
	}
	if updated > 0 {
		log.Printf("mr-delivery sync: refreshed %d/%d MR states", updated, len(pending))
	}
}

func (s *Service) discoverPromoteMRs(store MRDeliveryStore) {
	if s == nil || store == nil {
		return
	}
	now := time.Now().UTC()
	for _, repo := range store.Repositories() {
		project := strings.TrimSpace(repo.GongfengProjectID)
		if strings.HasPrefix(project, "github:") {
			continue
		}
		def := strings.TrimSpace(repo.DefaultBranch)
		integ := domain.NormalizeIntegrateBranch(repo.IntegrateBranch)
		if project == "" || def == "" || integ == "" || domain.SameGitBranch(integ, def) {
			continue
		}
		client, _, clientErr := s.trackerClientForProject("gongfeng", repo.ProjectID)
		if clientErr != nil {
			log.Printf("mr-delivery auth %s: %v", repo.ProjectID, clientErr)
			continue
		}
		client = client.Clone(15 * time.Second)
		for _, state := range []string{"opened", "merged"} {
			raw, err := client.CallTool("search_merge_request", map[string]any{
				"project_id":    project,
				"source_branch": integ,
				"target_branch": def,
				"state":         state,
				"per_page":      8,
				"order_by":      "updated_at",
				"sort":          "desc",
			})
			if err != nil {
				log.Printf("mr-delivery discover promote %s %s: %v", project, state, err)
				continue
			}
			for _, mr := range parseGongfengMRRecords(raw) {
				if mr.IID <= 0 {
					continue
				}
				url := "https://git.example.com/" + project + "/merge_requests/" + strconv.Itoa(mr.IID)
				if err := store.EnsureMRDeliveryState(project, mr.IID, url, domain.BaselinePromoteIssueID); err != nil {
					log.Printf("mr-delivery ensure promote %s!%d: %v", project, mr.IID, err)
					continue
				}
				st := normalizeMRState(mr.State)
				if st == "" {
					st = state
				}
				target := strings.TrimSpace(mr.TargetBranch)
				if target == "" {
					target = def
				}
				checked, ok := promoteMRCheckedAt(mr, st, now)
				if !ok {
					// A merged promote MR with no parseable time must not use "now".
					// That would treat every earlier baseline merge as already on the default branch.
					log.Printf("mr-delivery promote %s!%d: merge time unknown, skip", project, mr.IID)
					continue
				}
				if err := store.UpdateMRDeliveryState(project, mr.IID, st, target, checked); err != nil {
					log.Printf("mr-delivery update promote %s!%d: %v", project, mr.IID, err)
				}
			}
		}
	}
}

// syncWorkCommitsOnDefaultBranch marks a baseline MR as on the default branch
// only when its source commit is contained in that branch. A missing or failed
// lookup leaves the current label unchanged.
func (s *Service) syncWorkCommitsOnDefaultBranch(store MRDeliveryStore) {
	if s == nil || store == nil {
		return
	}
	for _, repo := range store.Repositories() {
		project := strings.TrimSpace(repo.GongfengProjectID)
		if strings.HasPrefix(project, "github:") {
			continue
		}
		def := strings.TrimSpace(repo.DefaultBranch)
		if project == "" || def == "" {
			continue
		}
		rows, err := store.MergedWorkMRs(project)
		if err != nil {
			log.Printf("mr-delivery work commits %s: %v", project, err)
			continue
		}
		client, _, clientErr := s.trackerClientForProject("gongfeng", repo.ProjectID)
		if clientErr != nil {
			log.Printf("mr-delivery work commits auth %s: %v", repo.ProjectID, clientErr)
			continue
		}
		client = client.Clone(15 * time.Second)
		for _, row := range rows {
			if row.MRIID <= 0 || domain.IsBaselinePromoteIssueID(row.IssueID) {
				continue
			}
			if domain.IsMainlineBranch(row.TargetBranch, def) {
				continue
			}
			cacheKey := project + "#" + strconv.Itoa(row.MRIID)
			if _, seen := s.landedOnDefault.Load(cacheKey); seen {
				continue
			}
			onDefault, ok := workCommitOnBranch(client, project, row.MRIID, def)
			if !ok {
				continue
			}
			if err := store.ApplyDefaultBranchLanding(row.IssueID, row.MRURL, onDefault); err != nil {
				log.Printf("mr-delivery landing %s!%d: %v", project, row.MRIID, err)
				continue
			}
			if onDefault {
				s.landedOnDefault.Store(cacheKey, true)
			}
		}
	}
}

func workCommitOnBranch(client *MCPClient, project string, iid int, branch string) (onDefault bool, ok bool) {
	if client == nil || iid <= 0 || strings.TrimSpace(branch) == "" {
		return false, false
	}
	raw, err := client.CallTool("search_merge_request", map[string]any{
		"project_id": project,
		"iid":        iid,
		"per_page":   1,
	})
	if err != nil {
		log.Printf("mr-delivery source commit %s!%d: %v", project, iid, err)
		return false, false
	}
	sha := parseGongfengSourceCommit(raw, iid)
	if sha == "" {
		return false, false
	}
	const pageSize = 100
	for page := 1; page <= 20; page++ {
		refsRaw, err := client.CallTool("get_commit_refs", map[string]any{
			"project_id": project,
			"commit_sha": sha,
			"type":       "branch",
			"page":       page,
			"per_page":   pageSize,
		})
		if err != nil {
			log.Printf("mr-delivery commit refs %s %s: %v", project, sha, err)
			return false, false
		}
		names, parsed := parseGongfengRefNames(refsRaw)
		if !parsed {
			return false, false
		}
		if domain.BranchListed(names, branch) {
			return true, true
		}
		if len(names) < pageSize {
			return false, true
		}
	}
	// The ref list was truncated before the default branch could be ruled out.
	return false, false
}

// promoteMRCheckedAt is when a baseline → default MR landed. Open MRs use the
// poll time. A merged MR must use Gongfeng's own timestamp: merged_at, then
// resolved_at, then updated_at. "now" is not a merge time.
func promoteMRCheckedAt(mr gongfengListedMR, state string, now time.Time) (time.Time, bool) {
	if normalizeMRState(state) != "merged" {
		if now.IsZero() {
			now = time.Now().UTC()
		}
		return now, true
	}
	for _, raw := range []string{mr.MergedAt, mr.ResolvedAt, mr.UpdatedAt} {
		if parsed := parseGongfengTime(raw); !parsed.IsZero() {
			return parsed, true
		}
	}
	return time.Time{}, false
}

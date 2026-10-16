package repo

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
)

// ---------- MR delivery states ( Gongfeng MCP delivery analytics ) ----------

func (r mrDeliveryStateRow) toDomain() domain.MRDeliveryState {
	item := domain.MRDeliveryState{
		MRProject: r.MRProject, MRIID: r.MRIID, MRURL: r.MRURL, State: r.State,
		TargetBranch: r.TargetBranch, IssueID: r.IssueID,
	}
	if r.CheckedAt != nil {
		item.CheckedAt = r.CheckedAt.UTC()
	}
	if r.Created != nil {
		item.CreatedAt = r.Created.UTC()
	}
	if r.Updated != nil {
		item.UpdatedAt = r.Updated.UTC()
	}
	return item
}

func mrDeliveryRowValues(item domain.MRDeliveryState, now time.Time) map[string]any {
	checked := timePtr(item.CheckedAt)
	return map[string]any{
		"mr_project": item.MRProject, "mr_iid": item.MRIID, "mr_url": item.MRURL,
		"state": item.State, "target_branch": domain.NormalizeBranchName(item.TargetBranch),
		"issue_id": item.IssueID, "checked_at": checked,
		"created_at": timePtr(item.CreatedAt), "updated_at": timePtr(now),
	}
}

func (r *DeliveryRepo) mrDeliveryState(project string, iid int) (domain.MRDeliveryState, bool) {
	return mrDeliveryStateDB(r.DB, project, iid)
}

func mrDeliveryStateDB(db *gorm.DB, project string, iid int) (domain.MRDeliveryState, bool) {
	var row mrDeliveryStateRow
	if err := db.Where("mr_project = ? AND mr_iid = ?", project, iid).Take(&row).Error; err != nil {
		return domain.MRDeliveryState{}, false
	}
	return row.toDomain(), true
}

// EnsureMRDeliveryState registers (or refreshes) the link between an MR and an
// issue. The baseline-promote marker issue is protected from being overwritten
// by regular work items.
func (r *DeliveryRepo) EnsureMRDeliveryState(project string, iid int, url, issueID string) error {
	if project == "" || iid <= 0 {
		return nil
	}
	url = strings.TrimSpace(url)
	issueID = strings.TrimSpace(issueID)
	now := time.Now().UTC()
	return r.DB.Transaction(func(tx *gorm.DB) error {
		existing, ok := mrDeliveryStateDB(tx, project, iid)
		if !ok {
			_, err := insertIgnore(tx, "mr_delivery_states", mrDeliveryRowValues(domain.MRDeliveryState{
				MRProject: project, MRIID: iid, MRURL: url, State: "unknown", IssueID: issueID,
			}, now))
			return err
		}
		originalURL, originalIssueID := existing.MRURL, existing.IssueID
		if url != "" {
			existing.MRURL = url
		}
		if existing.IssueID == issueID || issueID == domain.BaselinePromoteIssueID {
			// keep
		} else if existing.IssueID == domain.BaselinePromoteIssueID {
			// never demote the baseline marker
		} else if issueID != "" {
			existing.IssueID = issueID
		}
		if existing.MRURL == originalURL && existing.IssueID == originalIssueID {
			return nil
		}
		return upsert(tx, "mr_delivery_states", mrDeliveryRowValues(existing, now))
	})
}

func (r *DeliveryRepo) UpdateMRDeliveryState(project string, iid int, state string, targetBranch string, checkedAt time.Time) error {
	if project == "" || iid <= 0 {
		return nil
	}
	state = strings.TrimSpace(state)
	if state == "" {
		state = "unknown"
	}
	targetBranch = domain.NormalizeBranchName(targetBranch)
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	if err := r.recordMRState(project, iid, state, targetBranch, checkedAt); err != nil {
		return err
	}
	classified := r.classifyMRDelivery(domain.MRDeliveryState{
		MRProject:    project,
		State:        state,
		TargetBranch: targetBranch,
	})
	existing, ok := r.mrDeliveryState(project, iid)
	if !ok {
		return nil
	}
	if domain.IsBaselinePromoteIssueID(existing.IssueID) {
		if classified == "merged" {
			return r.PromoteIntegratedIssuesIfBaselineLanded(project, checkedAt)
		}
		return nil
	}
	if existing.IssueID != "" {
		if !r.shouldWriteIssueDeliveryFromMR(existing.IssueID, existing.MRURL) {
			return nil
		}
		if classified == "merged" {
			return r.MarkIssueDelivered(existing.IssueID, existing.MRURL, checkedAt)
		}
		return r.syncIssueMRState(existing.IssueID, existing.MRURL, classified)
	}
	return nil
}

// recordMRState stores a looked-up Gongfeng state without touching issues.
func (r *DeliveryRepo) recordMRState(project string, iid int, state, targetBranch string, checkedAt time.Time) error {
	return r.DB.Model(&mrDeliveryStateRow{}).
		Where("mr_project = ? AND mr_iid = ?", project, iid).
		Updates(map[string]any{"state": state, "target_branch": domain.NormalizeBranchName(targetBranch), "checked_at": checkedAt.UTC(), "updated_at": time.Now().UTC()}).Error
}

// MarkIssueDelivered locks a work item from MR polling / reconcile. mrURL must
// be one of the item's delivery MRs (or the item has none yet).
func (r *DeliveryRepo) MarkIssueDelivered(issueID, mrURL string, deliveredAt time.Time) error {
	return r.markIssueDelivered(issueID, singleDeliveryMR(mrURL), deliveredAt, false)
}

// MarkIssueDeliveredFromSOP locks a work item after a new SOP run succeeds.
// Unlike MarkIssueDelivered, it may lock a human-reopened item again.
func (r *DeliveryRepo) MarkIssueDeliveredFromSOP(issueID, mrURL string, deliveredAt time.Time) error {
	return r.MarkIssueDeliveredFromSOPMRs(issueID, singleDeliveryMR(mrURL), deliveredAt)
}

// MarkIssueDeliveredFromSOPMRs is MarkIssueDeliveredFromSOP for a run that
// delivered one MR per repository. The list replaces the item's delivery MRs;
// an empty list keeps MRs from earlier deliveries.
func (r *DeliveryRepo) MarkIssueDeliveredFromSOPMRs(issueID string, mrs []domain.DeliveryMR, deliveredAt time.Time) error {
	return r.markIssueDelivered(issueID, mrs, deliveredAt, true)
}

func singleDeliveryMR(mrURL string) []domain.DeliveryMR {
	mrURL = strings.TrimSpace(mrURL)
	if mrURL == "" {
		return nil
	}
	mr := domain.DeliveryMR{URL: mrURL}
	if project, iid, _, ok := domain.ParseGongfengMR(mrURL); ok {
		mr.Project, mr.IID = project, iid
	}
	return []domain.DeliveryMR{mr}
}

// registerDeliveryMR links the MR to the issue for polling and returns its
// classified state. A live Gongfeng lookup result is stored first.
func (r *DeliveryRepo) registerDeliveryMR(issueID string, mr domain.DeliveryMR, at time.Time) string {
	project, iid := mr.Project, mr.IID
	if project == "" || iid <= 0 {
		if p, n, _, ok := domain.ParseGongfengMR(mr.URL); ok {
			project, iid = p, n
		}
	}
	if project == "" || iid <= 0 {
		return "unknown"
	}
	_ = r.EnsureMRDeliveryState(project, iid, mr.URL, issueID)
	if st := strings.TrimSpace(mr.GongfengState); st != "" {
		_ = r.recordMRState(project, iid, st, mr.TargetBranch, at)
	}
	return r.lookupMRDeliveryState(mr.URL)
}

func (r *DeliveryRepo) markIssueDelivered(issueID string, mrs []domain.DeliveryMR, deliveredAt time.Time, fromSOP bool) error {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil
	}
	issue, ok := r.issues.Issue(issueID)
	if !ok {
		return nil
	}
	// A manually closed issue is an explicit terminal state. Background MR
	// polling and stale SOP callbacks must not change it.
	if issue.ManuallyClosed() {
		return nil
	}
	if deliveredAt.IsZero() {
		deliveredAt = time.Now().UTC()
	}
	// A human reopen must stay open until a new SOP succeeds. MR poll / reconcile
	// uses "now" as checkedAt and would otherwise immediately lock the item again.
	if strings.EqualFold(issue.DeliveryState, "reopened") && !fromSOP {
		return nil
	}
	// Poll / reconcile may see leftover MRs from earlier SOP runs. Those rows
	// stay in mr_delivery_states for analytics, but must not replace the
	// work item's delivery MRs.
	if !fromSOP {
		for _, mr := range mrs {
			if len(issue.DeliveryMRList()) > 0 && !issue.HasDeliveryMR(mr.URL) {
				return nil
			}
		}
	}
	if issue.DeliveryState == "merged" && !deliveredAt.After(issue.DeliveryUpdatedAt) {
		return nil
	}
	issue.DeliveryState = "merged"
	if len(mrs) > 0 && (fromSOP || len(issue.DeliveryMRList()) == 0) {
		list := make([]domain.DeliveryMR, 0, len(mrs))
		for _, mr := range mrs {
			mr.State = r.registerDeliveryMR(issue.ID, mr, deliveredAt)
			list = append(list, mr)
		}
		issue.SetDeliveryMRs(list)
	} else {
		for _, mr := range mrs {
			issue.SetDeliveryMRState(mr.URL, r.lookupMRDeliveryState(mr.URL))
		}
		if fromSOP {
			list := issue.DeliveryMRList()
			for idx := range list {
				list[idx].State = r.lookupMRDeliveryState(list[idx].URL)
			}
			issue.SetDeliveryMRs(list)
		}
	}
	issue.DeliveryUpdatedAt = deliveredAt.UTC()
	if issue.Status != "closed" {
		issue.Status = "done"
	}
	issue.AutomationState = "ignored"
	return r.issues.PutIssue(issue)
}

func (r *DeliveryRepo) ReopenDeliveredIssue(issueID string) (domain.Issue, error) {
	issue, ok := r.issues.Issue(strings.TrimSpace(issueID))
	if !ok {
		return domain.Issue{}, fmt.Errorf("issue not found")
	}
	if !strings.EqualFold(issue.Status, "closed") && !issue.DeliveryLocked() {
		return domain.Issue{}, fmt.Errorf("工作项尚未完成交付，无需重新打开")
	}
	if issue.TrackerClosed() {
		return domain.Issue{}, fmt.Errorf("外部工作项仍处于关闭状态，请先在外部系统重新打开")
	}
	if issue.DeliveryLocked() {
		issue.DeliveryState = "reopened"
	}
	issue.DeliveryUpdatedAt = time.Now().UTC()
	issue.Status = "todo"
	issue.CloseSource = ""
	issue.AutomationState = "eligible"
	if err := r.issues.PutIssue(issue); err != nil {
		return domain.Issue{}, err
	}
	stored, _ := r.issues.Issue(issue.ID)
	return stored, nil
}

// ReconcileMergedMRDeliveries applies terminal delivery state for MR rows that
// were already merged before this version was deployed. Human-reopened items
// stay open until a new SOP succeeds.
func (r *DeliveryRepo) ReconcileMergedMRDeliveries() error {
	var rows []mrDeliveryStateRow
	if err := r.DB.Where("state = 'merged' AND issue_id <> '' AND issue_id <> ?", domain.BaselinePromoteIssueID).Find(&rows).Error; err != nil {
		return err
	}
	type mergedDelivery struct {
		issueID, mrURL string
		at             time.Time
	}
	items := []mergedDelivery{}
	for _, row := range rows {
		if r.classifyMRDelivery(row.toDomain()) != "merged" {
			continue
		}
		at := time.Time{}
		if row.Updated != nil {
			at = *row.Updated
		}
		if row.CheckedAt != nil {
			at = *row.CheckedAt
		}
		items = append(items, mergedDelivery{issueID: row.IssueID, mrURL: row.MRURL, at: at})
	}
	for _, item := range items {
		if err := r.MarkIssueDelivered(item.issueID, item.mrURL, item.at); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileUnlinkedDeliveryMRs attaches MR links that were present in a
// succeeded run but missed at delivery time. It is a one-shot backfill for
// items already locked with an empty link; new runs record the link when they
// finish. Items with nothing to recover stay empty and are not retried here.
func (r *DeliveryRepo) ReconcileUnlinkedDeliveryMRs() error {
	now := time.Now().UTC()
	for _, issue := range r.issues.Issues() {
		if !issue.DeliveryLocked() || len(issue.DeliveryMRList()) > 0 {
			continue
		}
		recovered := r.recoverUnlinkedDeliveryMRs(issue)
		if len(recovered) == 0 {
			continue
		}
		for idx := range recovered {
			recovered[idx].State = r.registerDeliveryMR(issue.ID, recovered[idx], now)
		}
		issue.SetDeliveryMRs(recovered)
		if err := r.issues.PutIssue(issue); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileCompletedDeliveries locks issues whose latest SOP already succeeded
// but were created before delivery locking shipped.
func (r *DeliveryRepo) ReconcileCompletedDeliveries() error {
	for _, issue := range r.issues.Issues() {
		if issue.DeliveryLocked() || strings.EqualFold(issue.DeliveryState, "reopened") {
			continue
		}
		taskID := strings.TrimSpace(issue.LatestTaskID)
		if taskID == "" {
			continue
		}
		task, ok := r.tasks.Task(taskID)
		if !ok || task.Status != "completed" || strings.TrimSpace(task.RunID) == "" {
			continue
		}
		run, ok := r.runs.Run(task.RunID)
		if !ok || run.Status != "succeeded" {
			continue
		}
		at := run.UpdatedAt
		if at.IsZero() {
			at = time.Now().UTC()
		}
		if err := r.markIssueDelivered(issue.ID, domain.ResolveRunDeliveryMRs(run, nil), at, false); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileDeliveryMRStates refreshes deliveryMrState labels for locked issues.
func (r *DeliveryRepo) ReconcileDeliveryMRStates() error {
	for _, issue := range r.issues.Issues() {
		if !issue.DeliveryLocked() {
			continue
		}
		list := issue.DeliveryMRList()
		if len(list) == 0 {
			if issue.DeliveryMRState != "" {
				issue.DeliveryMRState = ""
				_ = r.issues.PutIssue(issue)
			}
			continue
		}
		for idx := range list {
			state := r.lookupMRDeliveryState(list[idx].URL)
			// Keep a mainline label after the human-owned baseline → default MR
			// landed; the work MR still targets the integrate branch.
			if state == "" || domain.KeepMainlineDeliveryLabel(list[idx].State, state) {
				continue
			}
			list[idx].State = state
		}
		before := issue
		issue.SetDeliveryMRs(list)
		if deliveryMRFieldsEqual(before, issue) {
			continue
		}
		_ = r.issues.PutIssue(issue)
	}
	return r.ReconcileBaselinePromoteDeliveries()
}

// PromoteIssueToMainline marks a baseline-integrated issue as on the default
// branch after the human-owned promote MR merged. The work MR URL is kept.
func (r *DeliveryRepo) PromoteIssueToMainline(issueID, mrURL string, at time.Time) error {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil
	}
	issue, ok := r.issues.Issue(issueID)
	if !ok {
		return nil
	}
	if strings.EqualFold(issue.DeliveryState, "reopened") {
		return nil
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	changed := false
	if issue.DeliveryState != "merged" {
		issue.DeliveryState = "merged"
		if issue.Status != "closed" {
			issue.Status = "done"
		}
		issue.AutomationState = "ignored"
		changed = true
	}
	before := issue
	if trimmed := strings.TrimSpace(mrURL); trimmed != "" {
		issue.SetDeliveryMRState(trimmed, "merged")
	} else if len(issue.DeliveryMRList()) == 0 {
		issue.DeliveryMRState = "merged"
	}
	if !deliveryMRFieldsEqual(before, issue) {
		changed = true
	}
	if at.After(issue.DeliveryUpdatedAt) {
		issue.DeliveryUpdatedAt = at.UTC()
		changed = true
	}
	if !changed {
		return nil
	}
	return r.issues.PutIssue(issue)
}

// PromoteIntegratedIssuesIfBaselineLanded no longer upgrades labels from a
// promote MR's timestamp. Whether baseline work is on the default branch is
// decided by commit containment in the MR sync.
func (r *DeliveryRepo) PromoteIntegratedIssuesIfBaselineLanded(string, time.Time) error {
	return nil
}

// ReconcileBaselinePromoteDeliveries used to fan a merged promote MR out by
// timestamp. Commit containment in the MR sync owns that decision now.
func (r *DeliveryRepo) ReconcileBaselinePromoteDeliveries() error {
	return nil
}

func (r *DeliveryRepo) promoteIntegratedIssues(project string, promoteAt time.Time) error {
	seen := map[string]struct{}{}
	var rows []mrDeliveryStateRow
	if err := r.DB.Where("mr_project = ? AND issue_id <> '' AND issue_id <> ?", project, domain.BaselinePromoteIssueID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		item := row.toDomain()
		if domain.IsBaselinePromoteIssueID(item.IssueID) {
			continue
		}
		at := item.UpdatedAt
		if !item.CheckedAt.IsZero() {
			at = item.CheckedAt
		}
		if r.classifyMRDelivery(domain.MRDeliveryState{MRProject: project, State: item.State, TargetBranch: item.TargetBranch}) != "integrated" {
			continue
		}
		if !domain.WorkLandedWithPromote(at.UTC(), promoteAt) {
			continue
		}
		if err := r.PromoteIssueToMainline(item.IssueID, item.MRURL, promoteAt); err != nil {
			return err
		}
		seen[item.IssueID] = struct{}{}
	}
	for _, issue := range r.issues.Issues() {
		if _, ok := seen[issue.ID]; ok {
			continue
		}
		if !domain.WorkLandedWithPromote(issue.DeliveryUpdatedAt, promoteAt) {
			continue
		}
		for _, mr := range issue.DeliveryMRList() {
			if !strings.EqualFold(mr.State, "integrated") {
				continue
			}
			proj, _, _, ok := domain.ParseGongfengMR(mr.URL)
			if !ok || !strings.EqualFold(strings.TrimSpace(proj), project) {
				continue
			}
			if err := r.PromoteIssueToMainline(issue.ID, mr.URL, promoteAt); err != nil {
				return err
			}
		}
	}
	return nil
}

// recoverUnlinkedDeliveryMRs reads the issue's latest succeeded SOP. Branch
// lookup already ran at delivery time; this pass only recovers MR URLs quoted
// in artifacts so a missed link can be attached without reopening the item.
func (r *DeliveryRepo) recoverUnlinkedDeliveryMRs(issue domain.Issue) []domain.DeliveryMR {
	if r.tasks == nil || r.runs == nil {
		return nil
	}
	taskID := strings.TrimSpace(issue.LatestTaskID)
	if taskID == "" {
		return nil
	}
	task, ok := r.tasks.Task(taskID)
	if !ok || strings.TrimSpace(task.RunID) == "" {
		return nil
	}
	run, ok := r.runs.Run(task.RunID)
	if !ok || run.Status != "succeeded" {
		return nil
	}
	return domain.ResolveRunDeliveryMRs(run, nil)
}

// MergedWorkMRs lists merged work MRs for one Gongfeng project. Baseline
// promote rows are omitted; the caller decides landing from commit refs.
func (r *DeliveryRepo) MergedWorkMRs(project string) ([]domain.MRDeliveryState, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, nil
	}
	var rows []mrDeliveryStateRow
	if err := r.DB.Where("mr_project = ? AND state = ? AND issue_id <> '' AND issue_id <> ?", project, "merged", domain.BaselinePromoteIssueID).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.MRDeliveryState, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

// ApplyDefaultBranchLanding sets the delivery label from whether the work
// commit is contained in the default branch. The work MR URL is kept.
func (r *DeliveryRepo) ApplyDefaultBranchLanding(issueID, mrURL string, onDefault bool) error {
	if onDefault {
		return r.PromoteIssueToMainline(issueID, mrURL, time.Now().UTC())
	}
	return r.demoteIssueToBaseline(issueID, mrURL)
}

func (r *DeliveryRepo) demoteIssueToBaseline(issueID, mrURL string) error {
	issue, ok := r.issues.Issue(strings.TrimSpace(issueID))
	if !ok || strings.EqualFold(issue.DeliveryState, "reopened") {
		return nil
	}
	mrURL = strings.TrimSpace(mrURL)
	if mrURL == "" || !strings.EqualFold(issue.DeliveryMRState, "merged") {
		return nil
	}
	before := issue
	if !issue.SetDeliveryMRState(mrURL, "integrated") {
		return nil
	}
	if deliveryMRFieldsEqual(before, issue) {
		return nil
	}
	return r.issues.PutIssue(issue)
}

func (r *DeliveryRepo) lookupMRDeliveryState(mrURL string) string {
	project, iid, _, ok := domain.ParseGongfengMR(mrURL)
	if !ok {
		return ""
	}
	states := r.MRDeliveryStatesMap([]domain.MRDeliveryKey{{Project: project, IID: iid}})
	if st, ok := states[domain.MRDeliveryKey{Project: project, IID: iid}]; ok {
		if classified := r.classifyMRDelivery(st); classified != "" {
			return classified
		}
	}
	return "unknown"
}

func (r *DeliveryRepo) classifyMRDelivery(st domain.MRDeliveryState) string {
	def, integ := r.branchContextForMRProject(st.MRProject)
	return domain.ClassifyMRDeliveryState(st.State, st.TargetBranch, def, integ)
}

func (r *DeliveryRepo) branchContextForMRProject(project string) (defaultBranch, integrate string) {
	integrate = domain.DefaultIntegrateBranch
	project = strings.TrimSpace(project)
	if project == "" {
		return "", integrate
	}
	for _, repo := range r.catalog.Repositories() {
		if strings.EqualFold(strings.TrimSpace(repo.GongfengProjectID), project) {
			return strings.TrimSpace(repo.DefaultBranch), strings.TrimSpace(repo.IntegrateBranch)
		}
	}
	return "", integrate
}

func (r *DeliveryRepo) shouldWriteIssueDeliveryFromMR(issueID, mrURL string) bool {
	issue, ok := r.issues.Issue(strings.TrimSpace(issueID))
	if !ok {
		return false
	}
	if len(issue.DeliveryMRList()) == 0 {
		return true
	}
	return issue.HasDeliveryMR(mrURL)
}

func (r *DeliveryRepo) syncIssueMRState(issueID, mrURL, state string) error {
	issue, ok := r.issues.Issue(strings.TrimSpace(issueID))
	if !ok {
		return nil
	}
	if !r.shouldWriteIssueDeliveryFromMR(issue.ID, mrURL) {
		return nil
	}
	state = strings.TrimSpace(state)
	if strings.TrimSpace(mrURL) == "" || state == "" {
		return nil
	}
	before := issue
	issue.SetDeliveryMRState(mrURL, state)
	if deliveryMRFieldsEqual(before, issue) {
		return nil
	}
	return r.issues.PutIssue(issue)
}

func deliveryMRFieldsEqual(a, b domain.Issue) bool {
	if a.DeliveryMRURL != b.DeliveryMRURL || a.DeliveryMRState != b.DeliveryMRState || len(a.DeliveryMRs) != len(b.DeliveryMRs) {
		return false
	}
	for i := range a.DeliveryMRs {
		if a.DeliveryMRs[i] != b.DeliveryMRs[i] {
			return false
		}
	}
	return true
}

func (r *DeliveryRepo) MRDeliveryStatesMap(keys []domain.MRDeliveryKey) map[domain.MRDeliveryKey]domain.MRDeliveryState {
	out := map[domain.MRDeliveryKey]domain.MRDeliveryState{}
	if len(keys) == 0 {
		return out
	}
	conditions := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys)*2)
	for _, k := range keys {
		if k.Project == "" || k.IID <= 0 {
			continue
		}
		conditions = append(conditions, "(mr_project = ? AND mr_iid = ?)")
		args = append(args, k.Project, k.IID)
	}
	if len(conditions) == 0 {
		return out
	}
	var rows []mrDeliveryStateRow
	if err := r.DB.Where(strings.Join(conditions, " OR "), args...).Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		item := row.toDomain()
		out[domain.MRDeliveryKey{Project: item.MRProject, IID: item.MRIID}] = item
	}
	return out
}

func (r *DeliveryRepo) MRDeliveryStatesNeedingRefresh(staleBefore time.Time) ([]domain.MRDeliveryState, error) {
	var rows []mrDeliveryStateRow
	if err := r.DB.
		Where("(state NOT IN ('merged', 'closed') OR (state='merged' AND target_branch='')) AND (checked_at IS NULL OR checked_at < ?)", staleBefore.UTC()).
		Order("checked_at IS NULL DESC, checked_at ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := []domain.MRDeliveryState{}
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

func (r *DeliveryRepo) ListMRDeliveryStates() ([]domain.MRDeliveryState, error) {
	var rows []mrDeliveryStateRow
	if err := r.DB.Order("updated_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := []domain.MRDeliveryState{}
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

// DiscoverMRDeliveryStatesFromArtifacts registers MR links found in run artifacts.
func (r *DeliveryRepo) DiscoverMRDeliveryStatesFromArtifacts() error {
	type artifactHit struct {
		runID  string
		name   string
		nodeID string
	}
	var rows []struct {
		ExecutionContext string `gorm:"column:execution_context"`
		Name             string `gorm:"column:name"`
		NodeID           string `gorm:"column:node_id"`
		Content          string `gorm:"column:content"`
	}
	if err := r.DB.Table("run_artifacts").
		Joins("JOIN runs r ON r.id = run_artifacts.run_id").
		Where("run_artifacts.content LIKE ? AND (run_artifacts.name IN ('mr-body','e2e-report','closeout') OR run_artifacts.node_id='mr' OR run_artifacts.name LIKE '%mr%') AND run_artifacts.name NOT LIKE '%patch%' AND run_artifacts.name NOT LIKE '%diff%'", "%merge_requests/%").
		Select("r.execution_context AS execution_context, run_artifacts.name AS name, run_artifacts.node_id AS node_id, run_artifacts.content AS content").
		Find(&rows).Error; err != nil {
		return err
	}
	type pending struct {
		project string
		iid     int
		url     string
		issueID string
		pri     int
	}
	seen := map[string]pending{}
	for _, row := range rows {
		pri := domain.MRArtifactPriority(row.Name, "", row.NodeID)
		if pri <= 0 {
			continue
		}
		issueID := issueIDFromExecutionContext(row.ExecutionContext)
		for _, ref := range domain.ParseGongfengMRs(row.Content) {
			key := ref.Project + "#" + fmt.Sprintf("%d", ref.IID)
			if cur, exists := seen[key]; exists && (cur.pri > pri || (cur.pri == pri && len(cur.url) >= len(ref.URL))) {
				continue
			}
			seen[key] = pending{project: ref.Project, iid: ref.IID, url: ref.URL, issueID: issueID, pri: pri}
		}
	}
	for _, item := range seen {
		if err := r.EnsureMRDeliveryState(item.project, item.iid, item.url, item.issueID); err != nil {
			return err
		}
	}
	return nil
}

func issueIDFromExecutionContext(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var ctx domain.TelemetryContext
	if json.Unmarshal([]byte(raw), &ctx) != nil {
		return ""
	}
	return strings.TrimSpace(ctx.IssueID)
}

// ---------- Delivery analytics ----------

type deliveryAcc struct {
	item        domain.AnalyticsDelivery
	latest      time.Time
	latestRun   domain.Run
	mrAt        time.Time
	displayRank int
	mrs         []mrRef

	// 该需求全部关联执行（含失败与返工）的累计节点求和耗时与执行次数。
	execDuration int64
	runCount     int
	failedRuns   int
	reworks      int
	firstRunAt   time.Time
	lastRunAt    time.Time
	// costKnown 只要有一次执行的成本被识别即为 true，用于区分"未识别"与"零成本"。
	costKnown              bool
	runs                   []domain.Run
	clarificationDuration  int64
	developmentDuration    int64
	testDuration           int64
	clarificationStart     time.Time
	clarificationSource    string
	developmentStart       time.Time
	developmentSource      string
	developmentRunID       string
	phaseSamples           domain.AnalyticsPhaseSamples
	phaseEvidence          []domain.AnalyticsPhaseEvidence
	unclassifiedPhaseNodes int
}

func requirementPhaseDurations(run domain.Run, fallback domain.WorkflowDefinition) (clarification, development, test int64) {
	measurements := requirementPhaseMeasurements(run, fallback)
	return measurements.clarification, measurements.development, measurements.test
}

type phaseMeasurements struct {
	clarification            int64
	development              int64
	test                     int64
	clarificationStart       time.Time
	clarificationStartSource string
	developmentStart         time.Time
	developmentStartSource   string
	samples                  domain.AnalyticsPhaseSamples
	evidence                 []domain.AnalyticsPhaseEvidence
	unclassified             int
}

func requirementPhaseMeasurements(run domain.Run, fallback domain.WorkflowDefinition) phaseMeasurements {
	var result phaseMeasurements
	workflow := run.WorkflowSnapshot
	frozenDefinition := workflow.ID != ""
	if workflow.ID == "" {
		workflow = fallback
	}
	definitions := make(map[string]domain.WorkflowNode, len(workflow.Nodes))
	for _, node := range workflow.Nodes {
		definitions[node.ID] = node
	}
	lane := domain.InferSOPLane(workflow)
	for nodeID, nodeRun := range run.Nodes {
		if nodeRun == nil || nodeRun.StartedAt.IsZero() {
			continue
		}
		definition := definitions[nodeID]
		// A composed SOP node encloses its child Run. Counting both would
		// duplicate the same wall-clock time in requirement phase totals.
		if definition.Type == "sop" {
			continue
		}
		stage, source := strings.TrimSpace(definition.MetricStage), "explicit"
		if !frozenDefinition {
			source = "inferred"
		}
		if stage == "" {
			stage, source = inferLegacyMetricStage(nodeID, definition.Name, lane), "inferred"
		}
		if stage == "clarification" && (result.clarificationStart.IsZero() || nodeRun.StartedAt.Before(result.clarificationStart)) {
			result.clarificationStart, result.clarificationStartSource = nodeRun.StartedAt, source
		}
		if stage == "development" && (result.developmentStart.IsZero() || nodeRun.StartedAt.Before(result.developmentStart)) {
			result.developmentStart, result.developmentStartSource = nodeRun.StartedAt, source
		}
		if nodeRun.FinishedAt.IsZero() || nodeRun.FinishedAt.Before(nodeRun.StartedAt) {
			continue
		}
		duration := nodeRun.FinishedAt.Sub(nodeRun.StartedAt).Milliseconds()
		switch stage {
		case "clarification":
			result.clarification += duration
			result.samples.Clarification++
		case "development":
			result.development += duration
			result.samples.Development++
		case "testing":
			result.test += duration
			result.samples.Testing++
		default:
			if stage != "none" {
				result.unclassified++
			}
			continue
		}
		if source == "inferred" {
			result.samples.Inferred++
		}
		result.evidence = append(result.evidence, domain.AnalyticsPhaseEvidence{
			RunID: run.ID, NodeID: nodeID, Stage: stage, Source: source, DurationMs: duration,
		})
	}
	sort.Slice(result.evidence, func(i, j int) bool {
		if result.evidence[i].RunID != result.evidence[j].RunID {
			return result.evidence[i].RunID < result.evidence[j].RunID
		}
		return result.evidence[i].NodeID < result.evidence[j].NodeID
	})
	return result
}

func inferLegacyMetricStage(nodeID, name, lane string) string {
	text := strings.ToLower(strings.TrimSpace(nodeID + " " + name))
	switch {
	case strings.Contains(text, "澄清") || strings.Contains(text, "需求确认") || strings.Contains(text, "clarif") || strings.Contains(text, "requirement-shaping"):
		return "clarification"
	case strings.Contains(text, "测试") || strings.Contains(text, "验证") || strings.Contains(text, "验收") || strings.Contains(text, "test") || strings.Contains(text, "verify") || strings.Contains(text, " qa"):
		return "testing"
	case strings.Contains(text, "研发") || strings.Contains(text, "开发") || strings.Contains(text, "前端") || strings.Contains(text, "后端") || strings.Contains(text, "implement") || strings.Contains(text, "coding") || strings.Contains(text, "frontend") || strings.Contains(text, "backend"):
		return "development"
	case lane == domain.SOPLaneProduct || lane == domain.SOPLaneDesign:
		return "clarification"
	default:
		return ""
	}
}

// mrRef 记录一个 issue 关联到的某个 MR。同一需求跑多次、提多个 MR 时会有多条。
type mrRef struct {
	project    string
	iid        int
	url        string
	createdAt  time.Time
	sourceRank int
}

// addMR 按 project+iid 去重登记一个 MR。
func (a *deliveryAcc) addMR(project string, iid int, url string, at time.Time, sourceRank int) {
	for i := range a.mrs {
		if a.mrs[i].project == project && a.mrs[i].iid == iid {
			if url != "" && a.mrs[i].url == "" {
				a.mrs[i].url = url
			}
			if at.After(a.mrs[i].createdAt) {
				a.mrs[i].createdAt = at
			}
			if sourceRank > a.mrs[i].sourceRank {
				a.mrs[i].sourceRank = sourceRank
			}
			return
		}
	}
	a.mrs = append(a.mrs, mrRef{project: project, iid: iid, url: url, createdAt: at, sourceRank: sourceRank})
}

func (a *deliveryAcc) addMRsFromText(text string, at time.Time, sourceRank int) {
	for i, ref := range domain.ParseGongfengMRs(text) {
		a.addMR(ref.Project, ref.IID, ref.URL, at, sourceRank)
		if i > 0 {
			continue
		}
		if a.item.MRURL != "" && sourceRank < a.displayRank {
			continue
		}
		if a.item.MRURL != "" && sourceRank == a.displayRank && !at.After(a.mrAt) {
			continue
		}
		a.displayRank = sourceRank
		a.mrAt = at
		a.item.MRURL = ref.URL
		a.item.MRProject = ref.Project
		a.item.MRIID = ref.IID
		a.item.MRState = "unknown"
	}
}

// deliveryGroupKey keeps TAPD throughput at one row per external requirement,
// even when an import created multiple local Issue records for the same story.
func deliveryGroupKey(issueID string, issue domain.Issue, run domain.Run) string {
	if externalID := issue.RequirementTAPDID(); externalID != "" {
		projectID := strings.TrimSpace(issue.ProjectID)
		if projectID == "" {
			projectID = strings.TrimSpace(run.ExecutionContext.ProjectID)
		}
		return "tapd\x00" + projectID + "\x00" + externalID
	}
	return "issue\x00" + issueID
}

// buildDeliveries 按需求聚合全部关联执行。issues 允许传入已加载的需求快照，
// 传 nil 时内部自行加载，避免同一次分析重复查询。
func (r *DeliveryRepo) buildDeliveries(filter domain.AnalyticsFilter, now time.Time, runs []domain.Run, workflowDefs map[string]domain.WorkflowDefinition, issues map[string]domain.Issue) []domain.AnalyticsDelivery {
	byIssue := map[string]*deliveryAcc{}
	if issues == nil {
		issues = map[string]domain.Issue{}
		for _, issue := range r.issues.Issues() {
			issues[issue.ID] = issue
		}
	}
	clarificationByGroup := map[string]time.Time{}
	for issueID, issue := range issues {
		started := issue.Clarification.StartedAt
		if started.IsZero() {
			continue
		}
		key := deliveryGroupKey(issueID, issue, domain.Run{})
		if prior := clarificationByGroup[key]; prior.IsZero() || started.Before(prior) {
			clarificationByGroup[key] = started
		}
	}
	runIDs := make([]string, 0, len(runs))
	for _, run := range runs {
		issueID := strings.TrimSpace(run.ExecutionContext.IssueID)
		if issueID != "" &&
			(filter.WorkflowID == "" || run.WorkflowID == filter.WorkflowID) && analyticsRunInWindow(run, filter) &&
			(filter.UserID == "" || analyticsRunMatchesUser(run, filter.UserID, issues[issueID])) {
			runIDs = append(runIDs, run.ID)
		}
	}
	mrText := r.mrTextsByRun(runIDs)
	for _, run := range runs {
		issueID := strings.TrimSpace(run.ExecutionContext.IssueID)
		if issueID == "" {
			continue
		}
		if filter.WorkflowID != "" && run.WorkflowID != filter.WorkflowID {
			continue
		}
		if !analyticsRunInWindow(run, filter) {
			continue
		}
		if filter.UserID != "" && !analyticsRunMatchesUser(run, filter.UserID, issues[issueID]) {
			continue
		}
		if filter.IssueSource != "" && !analyticsIssueMatchesSource(issues[issueID], filter.IssueSource) {
			continue
		}
		if filter.RequirementsOnly && !analyticsIsRequirementIssue(issues[issueID]) {
			continue
		}
		groupKey := deliveryGroupKey(issueID, issues[issueID], run)
		acc := byIssue[groupKey]
		if acc == nil {
			acc = &deliveryAcc{item: domain.AnalyticsDelivery{IssueID: issueID, MRState: "none"}}
			if started := clarificationByGroup[groupKey]; !started.IsZero() {
				acc.clarificationStart, acc.clarificationSource = started, "recorded"
			}
			byIssue[groupKey] = acc
		}
		// 需求维度的 Token/成本累计其全部关联执行，包含失败与返工。
		acc.item.Tokens += run.Usage.TotalTokens
		acc.item.CostUSD += run.Usage.CostUSD
		if usageCostKnown(run.Usage) {
			acc.costKnown = true
		}
		// A child SOP run lies inside its parent's SOP node: its usage, phases
		// and reworks count, but not as another run or another stretch of time.
		nested := strings.TrimSpace(run.ExecutionContext.ParentRunID) != ""
		if !nested {
			acc.execDuration += runExecutionDuration(run)
		}
		phase := requirementPhaseMeasurements(run, workflowDefs[run.WorkflowID])
		acc.developmentDuration += phase.development
		acc.testDuration += phase.test
		acc.phaseSamples.Development += phase.samples.Development
		acc.phaseSamples.Testing += phase.samples.Testing
		acc.phaseSamples.Inferred += phase.samples.Inferred
		for _, evidence := range phase.evidence {
			if evidence.Stage != "clarification" {
				acc.phaseEvidence = append(acc.phaseEvidence, evidence)
			}
		}
		if started := phase.clarificationStart; !started.IsZero() && (acc.clarificationStart.IsZero() || started.Before(acc.clarificationStart)) {
			acc.clarificationStart, acc.clarificationSource = started, phase.clarificationStartSource
		}
		if started := phase.developmentStart; !started.IsZero() && (acc.developmentStart.IsZero() || started.Before(acc.developmentStart)) {
			acc.developmentStart, acc.developmentSource, acc.developmentRunID = started, phase.developmentStartSource, run.ID
		}
		acc.unclassifiedPhaseNodes += phase.unclassified
		acc.reworks += runReworks(run)
		if !nested {
			acc.runCount++
			if run.Status == "failed" {
				acc.failedRuns++
			}
		}
		if acc.firstRunAt.IsZero() || run.CreatedAt.Before(acc.firstRunAt) {
			acc.firstRunAt = run.CreatedAt
		}
		if ended := runEndedAt(run, now); ended.After(acc.lastRunAt) {
			acc.lastRunAt = ended
		}
		latestNested := strings.TrimSpace(acc.latestRun.ExecutionContext.ParentRunID) != ""
		newer := acc.latest.IsZero() || run.CreatedAt.After(acc.latest)
		if acc.latestRun.ID == "" || (!nested && (latestNested || newer)) || (nested && latestNested && newer) {
			acc.latest = run.CreatedAt
			acc.latestRun = run
			acc.item.IssueID = issueID
		}
		acc.runs = append(acc.runs, run)
		text := mrText[run.ID]
		if text == "" {
			text = runMRText(run)
		}
		acc.addMRsFromText(text, run.CreatedAt, 40)
	}
	out := make([]domain.AnalyticsDelivery, 0, len(byIssue))
	mrsByIssue := make(map[string][]mrRef, len(byIssue))
	projectCache := map[string]domain.Project{}
	for _, acc := range byIssue {
		issueID := acc.item.IssueID
		mrsByIssue[issueID] = acc.mrs
		run := acc.latestRun
		acc.item.RunID = run.ID
		acc.item.RunStatus = run.Status
		acc.item.SOPSucceeded = run.Status == "succeeded"
		acc.item.WorkflowID = run.WorkflowID
		acc.item.WorkflowName = run.WorkflowSnapshot.Name
		if acc.item.WorkflowName == "" {
			acc.item.WorkflowName = workflowDefs[run.WorkflowID].Name
		}
		if acc.item.WorkflowName == "" {
			acc.item.WorkflowName = run.WorkflowID
		}
		duration, _ := analyticsRunDuration(run, now)
		acc.item.DurationMs = duration
		acc.item.RunCount = acc.runCount
		acc.item.FailedRuns = acc.failedRuns
		acc.item.Reworks = acc.reworks
		// 执行耗时为该需求全部执行按 SOP 节点求和的累计值；周期耗时为首次执行开始到最后一次执行结束。
		acc.item.ExecutionDurationMs = acc.execDuration
		if !acc.clarificationStart.IsZero() && !acc.developmentStart.IsZero() && !acc.developmentStart.Before(acc.clarificationStart) {
			acc.clarificationDuration = acc.developmentStart.Sub(acc.clarificationStart).Milliseconds()
			acc.phaseSamples.Clarification = 1
			acc.item.ClarificationStartedAt = acc.clarificationStart
			acc.item.DevelopmentStartedAt = acc.developmentStart
			source := "explicit"
			if acc.clarificationSource == "inferred" || acc.developmentSource == "inferred" {
				source = "inferred"
			}
			acc.phaseEvidence = append(acc.phaseEvidence, domain.AnalyticsPhaseEvidence{RunID: acc.developmentRunID, NodeID: "clarification-to-development", Stage: "clarification", Source: source, DurationMs: acc.clarificationDuration, StartedAt: acc.clarificationStart, FinishedAt: acc.developmentStart})
		}
		acc.item.ClarificationDurationMs = acc.clarificationDuration
		acc.item.DevelopmentDurationMs = acc.developmentDuration
		acc.item.TestDurationMs = acc.testDuration
		acc.item.PhaseSamples = acc.phaseSamples
		acc.item.PhaseEvidence = acc.phaseEvidence
		acc.item.UnclassifiedPhaseNodes = acc.unclassifiedPhaseNodes
		acc.item.FirstRunAt = acc.firstRunAt
		acc.item.LastRunAt = acc.lastRunAt
		if !acc.firstRunAt.IsZero() && acc.lastRunAt.After(acc.firstRunAt) {
			acc.item.CycleDurationMs = acc.lastRunAt.Sub(acc.firstRunAt).Milliseconds()
		}
		acc.item.UserID = analyticsRunUserID(run, issues[issueID])
		acc.item.Category = analyticsWorkflowCategory(run.WorkflowSnapshot, workflowDefs[run.WorkflowID])
		acc.item.CostKnown = acc.costKnown || acc.item.CostUSD > 0
		var project domain.Project
		if issue, ok := issues[issueID]; ok && r.catalog != nil && r.catalog.projects != nil {
			cached, seen := projectCache[issue.ProjectID]
			if !seen {
				cached, _ = r.catalog.projects.Project(issue.ProjectID)
				projectCache[issue.ProjectID] = cached
			}
			project = cached
		}
		acc.item.Lanes = compactDeliveryLanes(BuildRequirementSnapshot(issues[issueID], project, acc.runs, nil, workflowDefs, now).Lanes)
		if issue, ok := issues[issueID]; ok {
			acc.item.Title = issue.Title
			acc.item.Source = issue.Source
			if acc.item.Source == "" {
				acc.item.Source = "local"
			}
			if issue.ExternalRef != nil {
				acc.item.ExternalIID = issue.ExternalRef.ExternalIID
				acc.item.IssueURL = issue.ExternalRef.ExternalURL
			}
			acc.item.ExternalID = issue.RequirementTAPDID()
			for _, mr := range issue.DeliveryMRList() {
				acc.addMRsFromText(mr.URL, issue.DeliveryUpdatedAt, 100)
			}
		} else {
			acc.item.Title = issueID
			acc.item.Source = "local"
		}
		if acc.item.MRURL == "" {
			acc.item.MRState = "none"
		}
		out = append(out, acc.item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Delivered != out[j].Delivered {
			return out[i].Delivered
		}
		if out[i].SOPSucceeded != out[j].SOPSucceeded {
			return out[i].SOPSucceeded
		}
		return out[i].RunID > out[j].RunID
	})
	r.applyMRDeliveryStates(out, mrsByIssue, issues)
	return out
}

func (r *DeliveryRepo) applyMRDeliveryStates(items []domain.AnalyticsDelivery, mrsByIssue map[string][]mrRef, issues map[string]domain.Issue) {
	if len(items) == 0 {
		return
	}
	// 收集该批次所有 issue 关联到的全部 MR（不只是展示用的那个），逐一 ensure 并批量查状态。
	keySet := make(map[domain.MRDeliveryKey]struct{})
	ensured := map[string]struct{}{}
	ensure := func(project string, iid int, url, issueID string) {
		if project == "" || iid <= 0 {
			return
		}
		ensureKey := fmt.Sprintf("%s\x00%d\x00%s\x00%s", project, iid, url, issueID)
		if _, ok := ensured[ensureKey]; !ok {
			_ = r.EnsureMRDeliveryState(project, iid, url, issueID)
			ensured[ensureKey] = struct{}{}
		}
		keySet[domain.MRDeliveryKey{Project: project, IID: iid}] = struct{}{}
	}
	for i := range items {
		for _, ref := range mrsByIssue[items[i].IssueID] {
			ensure(ref.project, ref.iid, ref.url, items[i].IssueID)
		}
		// 兜底：展示用的 MR 一般已在 mrs 中，这里再确保一次。
		ensure(items[i].MRProject, items[i].MRIID, items[i].MRURL, items[i].IssueID)
	}
	if len(keySet) == 0 {
		for i := range items {
			if issue, ok := issues[items[i].IssueID]; ok {
				items[i].MRState = domain.EffectiveAnalyticsMRState(items[i].MRState, issue.DeliveryMRState)
			}
			domain.FinalizeAnalyticsDelivery(&items[i])
		}
		return
	}
	keys := make([]domain.MRDeliveryKey, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	states := r.MRDeliveryStatesMap(keys)
	repositories := r.catalog.Repositories()
	classify := func(st domain.MRDeliveryState) string {
		def, integ := "", domain.DefaultIntegrateBranch
		for _, repository := range repositories {
			if strings.EqualFold(strings.TrimSpace(repository.GongfengProjectID), strings.TrimSpace(st.MRProject)) {
				def, integ = strings.TrimSpace(repository.DefaultBranch), strings.TrimSpace(repository.IntegrateBranch)
				break
			}
		}
		return domain.ClassifyMRDeliveryState(st.State, st.TargetBranch, def, integ)
	}
	for i := range items {
		// 同一需求可能跑多次、提多个 MR。只有合入仓库默认分支才算「已合入/已交付」。
		// 合入集成分支记为 integrated（已合入基线）。多个候选按
		// merged > integrated > opened/closed 取最新的一个。
		preferProject, preferIID := "", 0
		if issue, ok := issues[items[i].IssueID]; ok {
			if p, iid, _, ok := domain.ParseGongfengMR(issue.DeliveryMRURL); ok {
				preferProject, preferIID = p, iid
			}
		}
		var best *mrRef
		bestState := ""
		for j := range mrsByIssue[items[i].IssueID] {
			ref := mrsByIssue[items[i].IssueID][j]
			if ref.project == "" || ref.iid <= 0 {
				continue
			}
			st, ok := states[domain.MRDeliveryKey{Project: ref.project, IID: ref.iid}]
			if !ok {
				continue
			}
			classified := classify(st)
			if classified == "" {
				continue
			}
			if best == nil || betterAnalyticsMR(preferProject, preferIID, *best, bestState, ref, classified) {
				copied := ref
				best = &copied
				bestState = classified
			}
		}
		if best != nil && domain.AnalyticsMRRank(bestState) > 0 {
			items[i].MRProject = best.project
			items[i].MRIID = best.iid
			if best.url != "" {
				items[i].MRURL = best.url
			}
			items[i].MRState = bestState
		} else if items[i].MRProject != "" && items[i].MRIID > 0 {
			if st, ok := states[domain.MRDeliveryKey{Project: items[i].MRProject, IID: items[i].MRIID}]; ok {
				// An unclassified (unknown) lookup must not downgrade a label
				// already carried by the run or the locked issue.
				if classified := classify(st); classified != "" && classified != "unknown" {
					items[i].MRState = classified
				}
			}
		}
		if issue, ok := issues[items[i].IssueID]; ok {
			items[i].MRState = domain.EffectiveAnalyticsMRState(items[i].MRState, issue.DeliveryMRState)
		}
		domain.FinalizeAnalyticsDelivery(&items[i])
	}
}

func betterAnalyticsMR(preferProject string, preferIID int, best mrRef, bestState string, next mrRef, nextState string) bool {
	bestRank := domain.AnalyticsMRRank(bestState)
	nextRank := domain.AnalyticsMRRank(nextState)
	if nextRank != bestRank {
		return nextRank > bestRank
	}
	bestPref := preferIID > 0 && strings.EqualFold(best.project, preferProject) && best.iid == preferIID
	nextPref := preferIID > 0 && strings.EqualFold(next.project, preferProject) && next.iid == preferIID
	if nextPref != bestPref {
		return nextPref
	}
	if next.sourceRank != best.sourceRank {
		return next.sourceRank > best.sourceRank
	}
	return next.createdAt.After(best.createdAt)
}

func runMRText(run domain.Run) string {
	bestPri := 0
	best := ""
	for _, art := range run.Artifacts {
		if strings.TrimSpace(art.Content) == "" {
			continue
		}
		pri := domain.MRArtifactPriority(art.Name, art.Path, art.NodeID)
		if pri <= 0 {
			continue
		}
		if pri > bestPri || (pri == bestPri && (best == "" || len(art.Content) < len(best))) {
			bestPri = pri
			best = art.Content
		}
	}
	if best != "" {
		return best
	}
	var b strings.Builder
	for _, event := range run.Telemetry {
		if event.Attributes == nil {
			continue
		}
		if url, ok := event.Attributes["url"].(string); ok {
			b.WriteString(url)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (r *DeliveryRepo) mrTextsByRun(runIDs []string) map[string]string {
	out := map[string]string{}
	if len(runIDs) == 0 {
		return out
	}
	if len(runIDs) > 500 {
		for start := 0; start < len(runIDs); start += 500 {
			for id, value := range r.mrTextsByRun(runIDs[start:min(start+500, len(runIDs))]) {
				out[id] = value
			}
		}
		return out
	}
	var rows []struct {
		RunID   string `gorm:"column:run_id"`
		Name    string `gorm:"column:name"`
		NodeID  string `gorm:"column:node_id"`
		Content string `gorm:"column:content"`
	}
	if err := r.DB.Table("run_artifacts").Where("run_id IN ?", runIDs).
		Where("content LIKE ? AND (name IN ('mr-body','e2e-report','closeout') OR node_id='mr' OR name LIKE '%mr%') AND name NOT LIKE '%patch%' AND name NOT LIKE '%diff%'", "%merge_requests/%").
		Select("run_id, name, node_id, content").
		Find(&rows).Error; err != nil {
		return out
	}
	type scored struct {
		pri     int
		content string
	}
	best := map[string]scored{}
	for _, row := range rows {
		pri := domain.MRArtifactPriority(row.Name, "", row.NodeID)
		if pri <= 0 {
			continue
		}
		cur, ok := best[row.RunID]
		if !ok || pri > cur.pri || (pri == cur.pri && len(row.Content) < len(cur.content)) {
			best[row.RunID] = scored{pri: pri, content: row.Content}
		}
	}
	for runID, item := range best {
		out[runID] = item.content
	}
	return out
}

func compactDeliveryLanes(lanes []domain.RequirementLane) []domain.RequirementLane {
	if len(lanes) == 0 {
		return nil
	}
	out := make([]domain.RequirementLane, len(lanes))
	for i, lane := range lanes {
		lane.Stages = nil
		out[i] = lane
	}
	return out
}

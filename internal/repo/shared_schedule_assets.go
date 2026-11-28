package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"agentflow/internal/domain"
)

var ErrSharedScheduleReferences = errors.New("共享任务引用资产不可用")

// sharedScheduleAssets resolves only the direct references of a shared
// schedule. Nested dependencies remain owned by the referenced asset's
// existing resolver and are intentionally not pinned into the schedule.
func (r *CatalogRepo) sharedScheduleAssets(refs []domain.AssistantReference) ([]domain.SharedScheduleAsset, error) {
	refs, err := r.directScheduleReferences(refs)
	if err != nil {
		return nil, err
	}
	out := make([]domain.SharedScheduleAsset, 0, len(refs))
	for _, ref := range refs {
		_, shared, found := r.sharedScheduleReference(ref)
		if !found || !shared {
			return nil, fmt.Errorf("%w：%s/%s 不存在或尚未共享", ErrSharedScheduleReferences, ref.Type, ref.ID)
		}
		out = append(out, domain.SharedScheduleAsset{Type: ref.Type, ID: ref.ID, ProjectID: ref.ProjectID})
	}
	return out, nil
}

func (r *CatalogRepo) directScheduleReferences(refs []domain.AssistantReference) ([]domain.AssistantReference, error) {
	if len(refs) > 12 {
		return nil, fmt.Errorf("%w：最多引用 12 个资产", ErrSharedScheduleReferences)
	}
	out := make([]domain.AssistantReference, 0, len(refs))
	seen := map[string]bool{}
	for _, raw := range refs {
		ref := domain.AssistantReference{Type: strings.TrimSpace(raw.Type), ID: strings.TrimSpace(raw.ID)}
		if ref.Type != "agent" && ref.Type != "skill" && ref.Type != "sop" {
			return nil, fmt.Errorf("%w：%s 不支持随 AI 定时任务共享", ErrSharedScheduleReferences, ref.Type)
		}
		key := ref.Type + "/" + ref.ID
		if seen[key] {
			continue
		}
		canonical, _, found := r.sharedScheduleReference(ref)
		if ref.ID == "" || !found {
			return nil, fmt.Errorf("%w：%s 不存在", ErrSharedScheduleReferences, key)
		}
		seen[key] = true
		out = append(out, canonical)
	}
	return out, nil
}

// shareScheduleReference delegates the next dependency boundary to the
// referenced asset's sharing path. The caller supplies the publish transaction.
func (r *CatalogRepo) shareScheduleReference(ref domain.AssistantReference, projectID string) error {
	canonical, shared, found := r.sharedScheduleReference(ref)
	if !found {
		return fmt.Errorf("%w：%s/%s 不存在", ErrSharedScheduleReferences, ref.Type, ref.ID)
	}
	if shared {
		return nil
	}
	if canonical.ProjectID != projectID {
		return fmt.Errorf("%w：%s/%s 不属于当前项目且尚未共享", ErrSharedScheduleReferences, ref.Type, ref.ID)
	}
	var err error
	switch ref.Type {
	case "agent":
		err = r.ShareAgent(ref.ID)
	case "skill":
		err = r.ShareSkill(ref.ID)
	case "sop":
		err = r.ShareWorkflow(ref.ID)
	default:
		return fmt.Errorf("%w：%s 不支持随 AI 定时任务共享", ErrSharedScheduleReferences, ref.Type)
	}
	if err != nil {
		return fmt.Errorf("%w：共享 %s/%s 失败：%w", ErrSharedScheduleReferences, ref.Type, ref.ID, err)
	}
	return nil
}

// Resolve direct references through the same catalog records used by chat.
// Template-internal roles and skills are resolved by the LOOP engine, not here.
func (r *CatalogRepo) sharedScheduleReference(ref domain.AssistantReference) (domain.AssistantReference, bool, bool) {
	ref = domain.AssistantReference{Type: strings.TrimSpace(ref.Type), ID: strings.TrimSpace(ref.ID)}
	switch ref.Type {
	case "agent":
		v, ok := r.Agent(ref.ID)
		ref.Name, ref.ProjectID = v.Name, v.ProjectID
		return ref, v.Shared, ok
	case "skill":
		v, ok := r.Skill(ref.ID)
		ref.Name, ref.ProjectID = v.Name, v.ProjectID
		return ref, v.Shared, ok
	case "sop":
		v, ok := r.Workflow(ref.ID)
		ref.Name, ref.ProjectID = v.Name, v.ProjectID
		return ref, v.Shared || v.Generic, ok
	default:
		return ref, false, false
	}
}

// PreviewSharedSchedulePublish is the read-only dry-run of PublishSharedSchedule:
// it reports the direct references publishing would newly share (including the
// rules and skills a newly shared agent carries) and the references that
// cannot be shared, without writing anything. It reuses the same resolution
// and validation paths as the publish transaction so the two cannot drift.
func (r *CatalogRepo) PreviewSharedSchedulePublish(jobID, projectID string) (domain.SharedSchedulePublishPreview, error) {
	preview := domain.SharedSchedulePublishPreview{WillShare: []domain.AssistantReference{}, Blocked: []domain.SharedScheduleBlockedRef{}}
	var source scheduleJobRow
	if err := r.DB.Where("id = ?", jobID).First(&source).Error; err != nil {
		return preview, err
	}
	var job domain.ScheduleJob
	if err := json.Unmarshal(source.Payload, &job); err != nil {
		return preview, err
	}
	if !job.SkipDispatch || job.SharedTaskID != "" || job.ProjectID != projectID {
		return preview, fmt.Errorf("只能从当前项目的独立 AI 定时任务发布共享版本")
	}
	refs, err := r.directScheduleReferences(job.References)
	if err != nil {
		return preview, err
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		canonical, shared, _ := r.sharedScheduleReference(ref)
		if shared {
			continue
		}
		if canonical.ProjectID != projectID {
			preview.Blocked = append(preview.Blocked, domain.SharedScheduleBlockedRef{Type: ref.Type, ID: ref.ID, Name: canonical.Name, Reason: fmt.Sprintf("%s/%s 不属于当前项目且尚未共享", ref.Type, ref.ID)})
			continue
		}
		// A blocked reference fails the publish transaction, so it must not
		// appear as newly shared.
		if ref.Type == "sop" {
			workflow, _ := r.Workflow(ref.ID)
			workflow.Shared = true
			if err := r.ValidateWorkflowAgentScopes(workflow); err != nil {
				preview.Blocked = append(preview.Blocked, domain.SharedScheduleBlockedRef{Type: ref.Type, ID: ref.ID, Name: canonical.Name, Reason: err.Error()})
				continue
			}
			if err := r.ValidateWorkflowSharing(workflow); err != nil {
				preview.Blocked = append(preview.Blocked, domain.SharedScheduleBlockedRef{Type: ref.Type, ID: ref.ID, Name: canonical.Name, Reason: err.Error()})
				continue
			}
		}
		if !seen[ref.Type+"/"+ref.ID] {
			seen[ref.Type+"/"+ref.ID] = true
			preview.WillShare = append(preview.WillShare, canonical)
		}
		if ref.Type == "agent" {
			r.previewAgentCascade(ref.ID, &preview)
		}
	}
	return preview, nil
}

// previewAgentCascade mirrors ShareAgentAssets: a newly shared agent shares
// its project-private rules and skills in the same save path.
func (r *CatalogRepo) previewAgentCascade(agentID string, preview *domain.SharedSchedulePublishPreview) {
	agent, _ := r.Agent(agentID)
	for _, id := range agent.Rules {
		item, ok := r.Rule(id)
		if !ok || item.Shared || (item.ProjectID != "" && item.ProjectID != agent.ProjectID) {
			continue
		}
		if !seenPreviewAsset(preview, "rule", id) {
			preview.WillShare = append(preview.WillShare, domain.AssistantReference{Type: "rule", ID: id, Name: item.Name, ProjectID: item.ProjectID})
		}
	}
	for _, id := range agent.Skills {
		item, ok := r.Skill(id)
		if !ok || item.Shared || (item.ProjectID != "" && item.ProjectID != agent.ProjectID) {
			continue
		}
		if !seenPreviewAsset(preview, "skill", id) {
			preview.WillShare = append(preview.WillShare, domain.AssistantReference{Type: "skill", ID: id, Name: item.Name, ProjectID: item.ProjectID})
		}
	}
}

func seenPreviewAsset(preview *domain.SharedSchedulePublishPreview, typ, id string) bool {
	for _, a := range preview.WillShare {
		if a.Type == typ && a.ID == id {
			return true
		}
	}
	return false
}

// ValidateSharedScheduleAssets rechecks current permissions against the
// version's direct references. Nested dependencies remain governed by the
// referenced asset's own resolver.
func (r *CatalogRepo) ValidateSharedScheduleAssets(v domain.SharedScheduleVersion) error {
	assets, err := r.sharedScheduleAssets(v.References)
	if err != nil {
		return err
	}
	if len(assets) != len(v.Assets) {
		return fmt.Errorf("%w：直接引用列表与共享版本不一致", ErrSharedScheduleReferences)
	}
	for _, asset := range assets {
		if !slices.Contains(v.Assets, asset) {
			return fmt.Errorf("%w：%s/%s 的归属已变更，请发布新版本", ErrSharedScheduleReferences, asset.Type, asset.ID)
		}
	}
	return nil
}

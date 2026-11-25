package repo

import (
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
// Template-internal roles and skills are resolved by the SOP engine, not here.
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

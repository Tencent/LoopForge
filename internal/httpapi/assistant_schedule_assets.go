package httpapi

import (
	"context"
	"fmt"

	"agentflow/internal/domain"
)

// This grant is supplied only by the scheduler. Browser JSON, session source
// markers and historical messages cannot create it.
type scheduledAssets struct {
	ProjectID string
	Version   domain.SharedScheduleVersion
}

type scheduledAssetsKey struct{}

func scheduledAssetsFrom(ctx context.Context) scheduledAssets {
	if ctx == nil {
		return scheduledAssets{}
	}
	assets, _ := ctx.Value(scheduledAssetsKey{}).(scheduledAssets)
	return assets
}

func sameScheduleReferenceIDs(a, b []domain.AssistantReference) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

func (h *assistantHandlers) validateScheduledAssets(ctx context.Context, projectID string) error {
	scope := scheduledAssetsFrom(ctx)
	if scope.Version.ID == "" {
		return nil
	}
	if scope.ProjectID != projectID {
		return fmt.Errorf("共享任务引用授权不属于当前项目")
	}
	return h.repo.ValidateSharedScheduleAssets(scope.Version)
}

func scheduledAssetRead(ctx context.Context, projectID, operation, id string) bool {
	scope := scheduledAssetsFrom(ctx)
	if scope.ProjectID != projectID || scope.Version.ID == "" {
		return false
	}
	typ := map[string]string{"agents_get": "agent", "workflows_get": "sop"}[operation]
	return typ != "" && scope.Version.HasAsset(typ, id)
}

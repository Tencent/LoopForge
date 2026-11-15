package tracker

import (
	"context"
	"errors"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/forge"
)

func (s *Service) refreshForgeDelivery(store MRDeliveryStore, row domain.MRDeliveryState) error {
	ref, ok := domain.ParseForgeProject(row.MRProject)
	if !ok || row.ForgeUserID <= 0 || row.MRIID <= 0 || row.MRURL != ref.PullURL(row.MRIID) {
		return errors.New("Forgejo 交付缺少已验证的仓库和凭据身份")
	}
	credentials, ok := s.projectAuth.(interface {
		ForgeToken(userID int64, instance string) string
	})
	if !ok {
		return forge.ErrTokenRequired
	}
	// The saved delivery identifies the token owner. Do not borrow a project
	// owner's token or discover a destination from artifact text.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := forge.OpenRepository(ctx, ref, credentials.ForgeToken(row.ForgeUserID, ref.Instance))
	if err != nil {
		return err
	}
	pull, err := client.GetPull(ctx, row.MRIID)
	if err != nil {
		return err
	}
	mr := pull.Delivery(ref)
	return store.UpdateMRDeliveryState(row.MRProject, row.MRIID, mr.GongfengState, mr.TargetBranch, pull.MergedAt, time.Now().UTC())
}

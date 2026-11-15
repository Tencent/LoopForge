package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/forge"
)

func (e *Engine) mergeForgeIntegration(ctx context.Context, run domain.Run, rc domain.RepositoryContext, ref domain.ForgeRepository, head string) (domain.DeliveryMR, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	userID := run.ExecutionContext.ResponsibleUserID
	work, target := strings.TrimSpace(rc.WorkBranch), strings.TrimSpace(rc.IntegrateBranch)
	client, err := forge.OpenRepository(ctx, ref, e.repo.ForgeToken(userID, ref.Instance))
	if err != nil {
		return domain.DeliveryMR{}, err
	}
	if client.DefaultBranch() == "" || domain.IsMainlineBranch(target, client.DefaultBranch()) {
		return domain.DeliveryMR{}, errors.New("Forgejo 定时 SOP 只能合入非默认的集成分支")
	}
	current, err := client.BranchAt(ctx, target, head)
	if err != nil || current {
		return domain.DeliveryMR{}, err
	}
	pr, err := client.SubmitPull(ctx, work, target, head, integrateMRTitle(run, rc, work, target), integrateMRDescription(run, work, target), false)
	if err != nil {
		return domain.DeliveryMR{}, err
	}
	pull, err := client.MergePull(ctx, pr.Pull, work, target, head)
	if err != nil {
		return domain.DeliveryMR{}, err
	}
	mr := pull.Delivery(ref)
	mr.RepositoryID, mr.Repository, mr.ForgeUserID = rc.RepositoryID, rc.Name, userID
	e.updateRun(run.ID, func(r *domain.Run) {
		e.event(r, "", "integration.succeeded", fmt.Sprintf("仓库 %s 已通过 Forgejo PR %s 合入 %s", rc.Name, mr.URL, target))
	})
	return mr, nil
}

func (e *Engine) findForgeDelivery(run domain.Run, rc domain.RepositoryContext) (domain.DeliveryMR, bool) {
	ref, err := forge.RunRepository(rc)
	if err != nil {
		log.Printf("resolve Forgejo delivery run=%s: %v", run.ID, err)
		return domain.DeliveryMR{}, false
	}
	userID := run.ExecutionContext.ResponsibleUserID
	token := e.repo.ForgeToken(userID, ref.Instance)
	if token == "" {
		return domain.DeliveryMR{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := forge.OpenRepository(ctx, ref, token)
	if err != nil {
		log.Printf("resolve Forgejo delivery run=%s: %v", run.ID, err)
		return domain.DeliveryMR{}, false
	}
	target := ""
	if run.ExecutionContext.ScheduleJobID != "" {
		target = rc.IntegrateBranch
	}
	pull, found, err := client.FindPull(ctx, rc.WorkBranch, target, false)
	if err != nil {
		log.Printf("resolve Forgejo delivery run=%s: %v", run.ID, err)
	}
	if !found || err != nil {
		return domain.DeliveryMR{}, false
	}
	mr := pull.Delivery(ref)
	mr.ForgeUserID = userID
	return mr, true
}

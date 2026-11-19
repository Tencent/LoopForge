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

// Forgejo's baseline update is a PR just like the scheduled delivery. It runs
// before dispatch and must respect protection even when Git could push directly.
func (e *Engine) alignForgeBaseline(rc domain.RepositoryContext, userID int64) error {
	target := domain.NormalizeIntegrateBranch(rc.IntegrateBranch)
	if target == "" {
		return nil
	}
	ref, err := forge.RunRepository(rc)
	if err != nil {
		return err
	}
	unlock, err := lockIntegration(e.repo, rc.RepositoryID)
	if err != nil {
		return err
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client, err := forge.OpenRepository(ctx, ref, e.repo.ForgeToken(userID, ref.Instance))
	if err != nil {
		return err
	}
	source := client.DefaultBranch()
	if source == "" || domain.IsMainlineBranch(target, source) {
		return errors.New("Forgejo 定时 SOP 只能合入非默认的集成分支")
	}
	if !domain.SameGitBranch(rc.DefaultBranch, source) {
		return errors.New("仓库配置的默认分支与 Forgejo 不一致，请先更新仓库绑定")
	}
	head, err := client.BranchHead(ctx, source)
	if err != nil {
		return err
	}
	current, err := client.BranchContains(ctx, target, head)
	if err != nil || current {
		return err
	}
	_, err = client.SubmitAndMergePull(ctx, source, target, head,
		fmt.Sprintf("LoopForge: 对齐 %s → %s", source, target),
		"定时 SOP 派发前，将默认分支的提交同步到集成分支。")
	return err
}

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
	current, err := client.BranchContains(ctx, target, head)
	if err != nil || current {
		return domain.DeliveryMR{}, err
	}
	pull, err := client.SubmitAndMergePull(ctx, work, target, head,
		integrateMRTitle(run, rc, work, target), integrateMRDescription(run, work, target))
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
	target := repositoryBaseBranch(rc)
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

// forgeBranchDelivery verifies MR gates with the run's frozen binding and the
// same personal credential as its delivery. A generic SSH binding has no API.
func (e *Engine) forgeBranchDelivery(run domain.Run, rc domain.RepositoryContext, source, target string) ([]domain.DeliveryMR, error) {
	ref, err := forge.RunRepository(rc)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	userID := run.ExecutionContext.ResponsibleUserID
	client, err := forge.OpenRepository(ctx, ref, e.repo.ForgeToken(userID, ref.Instance))
	if err != nil {
		return nil, err
	}
	pulls, err := client.BranchPulls(ctx, source, target)
	if err != nil {
		return nil, err
	}
	mrs := make([]domain.DeliveryMR, 0, len(pulls))
	for _, pull := range pulls {
		mr := pull.Delivery(ref)
		if mr.HeadSHA != "" {
			mrs = append(mrs, mr)
		}
	}
	return mrs, nil
}

// forgeNodeMCP gives an MR node a capability for its own frozen work branches.
// Forgejo personal tokens stay on the platform. Existing runner MCP transport
// carries the node capability, as it does the scoped knowledge proxy token.
func (e *Engine) forgeNodeMCP(run domain.Run, nodeID string) (domain.MCPServerDefinition, error) {
	node := run.Nodes[nodeID]
	if node == nil || node.Status != "running" {
		return domain.MCPServerDefinition{}, errors.New("Forgejo PR 需要正在执行的节点")
	}
	token, err := forge.SignRunGrant(forge.RunGrant{RunID: run.ID, NodeID: nodeID, UserID: run.ExecutionContext.ResponsibleUserID, Generation: run.Generation, Attempt: node.Attempt}, time.Now())
	if err != nil {
		return domain.MCPServerDefinition{}, err
	}
	e.mu.Lock()
	base := e.platformMCPBase
	e.mu.Unlock()
	return domain.MCPServerDefinition{
		ID: domain.ForgeMCPServerID, Name: "Forgejo PR", Transport: "http",
		URL: base + forge.RunMCPPath, Headers: map[string]string{"Authorization": "Bearer " + token}, TimeoutSec: 45,
	}, nil
}

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
)

const alignTaskTimeout = 15 * time.Minute

// AlignBaseline merges the repository's default branch into its integration
// branch. With a cloud runner pool it runs on a pool runner; otherwise, and
// for repositories only reachable through a local path, on this host.
func (e *Engine) AlignBaseline(repository domain.RepositoryContext, gitToken string) error {
	if !ShouldAlignIntegrate(repository) {
		return nil
	}
	runner, ok := e.alignRunner(repository)
	if !ok {
		return AlignIntegrateBaseline(repository, gitToken, e.repo)
	}
	unlock, err := lockIntegration(e.repo, repository.RepositoryID)
	if err != nil {
		return err
	}
	defer unlock()
	integrate := domain.NormalizeIntegrateBranch(repository.IntegrateBranch)
	id := fmt.Sprintf("rt-%d-%s-align", time.Now().UnixNano(), safeRunnerPath(repository.RepositoryID))
	task := domain.RunnerTask{
		ID:          id,
		RunnerID:    runner.ID,
		Provider:    "terminal",
		Action:      domain.RunnerTaskActionAlignBaseline,
		Workspace:   "align/" + id,
		Status:      domain.RunnerTaskQueued,
		MaxAttempts: 1,
		Invocation: &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{{
			RepositoryID:    repository.RepositoryID,
			Name:            repository.Name,
			CloneURL:        repository.CloneURL,
			GitToken:        gitToken,
			IntegrateBranch: integrate,
			DefaultBranch:   strings.TrimSpace(repository.DefaultBranch),
		}}},
	}
	if err := e.repo.PutRunnerTask(task); err != nil {
		return err
	}
	e.notifyRunnerDispatch("terminal")
	ctx, cancel := context.WithTimeout(context.Background(), alignTaskTimeout)
	defer cancel()
	final, err := e.awaitRunnerTask(ctx, id, nil)
	if err != nil {
		return err
	}
	if final.Status != domain.RunnerTaskSuccess {
		msg := strings.TrimSpace(final.Error)
		if msg == "" {
			msg = "align_baseline " + final.Status + " on runner"
		}
		return errors.New(msg)
	}
	var result domain.RunnerAlignResult
	if err := json.Unmarshal([]byte(final.Result), &result); err != nil {
		return fmt.Errorf("decode align result: %w", err)
	}
	if len(result.Conflicts) > 0 {
		return &AlignConflictError{Integrate: integrate, Default: strings.TrimSpace(repository.DefaultBranch), Files: result.Conflicts, Blocks: result.Blocks}
	}
	return nil
}

func (e *Engine) alignRunner(repository domain.RepositoryContext) (domain.Runner, bool) {
	label := e.cloudPoolLabel()
	if label == "" || strings.TrimSpace(repository.CloneURL) == "" {
		return domain.Runner{}, false
	}
	runner, ok := e.pickPoolRunner(label)
	if !ok || !runner.SupportsAlignBaseline() {
		return domain.Runner{}, false
	}
	return runner, true
}

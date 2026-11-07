package engine

import (
	"errors"
	"fmt"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

type executionTarget struct {
	Scope      string
	Repository *domain.RepositoryContext
}

func executionTargetSink(sink runtime.EventSink, target executionTarget) runtime.EventSink {
	if sink == nil || target.Repository == nil {
		return sink
	}
	return func(event domain.TelemetryEvent) {
		attributes := make(map[string]any, len(event.Attributes)+3)
		for key, value := range event.Attributes {
			attributes[key] = value
		}
		attributes["repositoryId"] = target.Repository.RepositoryID
		attributes["repositoryName"] = target.Repository.Name
		attributes["repositoryDir"] = target.Repository.WorkspaceDir
		event.Attributes = attributes
		sink(event)
	}
}

func normalizeExecutionScope(scope string) string {
	if strings.EqualFold(strings.TrimSpace(scope), domain.ExecutionScopeRepositories) {
		return domain.ExecutionScopeRepositories
	}
	return domain.ExecutionScopeRun
}

func executionTargets(run domain.Run, scope string) ([]executionTarget, error) {
	scope = normalizeExecutionScope(scope)
	if scope == domain.ExecutionScopeRun {
		return []executionTarget{{Scope: scope}}, nil
	}
	repositories := run.RepositoryContexts()
	if len(repositories) == 0 {
		return nil, errors.New("逐仓库执行需要 Run 至少绑定一个代码仓库")
	}
	targets := make([]executionTarget, 0, len(repositories))
	for i := range repositories {
		rc := repositories[i]
		targets = append(targets, executionTarget{Scope: scope, Repository: &rc})
	}
	return targets, nil
}

func (t executionTarget) directory() string {
	if t.Repository == nil {
		return ""
	}
	return strings.TrimSpace(t.Repository.WorkspaceDir)
}

func (t executionTarget) label() string {
	if t.Repository == nil {
		return "Run"
	}
	if name := strings.TrimSpace(t.Repository.Name); name != "" {
		return name
	}
	if id := strings.TrimSpace(t.Repository.RepositoryID); id != "" {
		return id
	}
	if dir := t.directory(); dir != "" {
		return dir
	}
	return "repository"
}

// bindExecutionTarget makes script-like commands independent from the
// physical single/multi-repository layout. Repository-scoped commands start
// in the selected checkout; Run-scoped commands start at the workspace root.
func bindExecutionTarget(command string, target executionTarget) string {
	var b strings.Builder
	b.WriteString(`export AGENTFLOW_WORKSPACE="$PWD"`)
	b.WriteString("\nexport AGENTFLOW_EXECUTION_SCOPE=" + shellSingleQuote(target.Scope))
	if target.Repository != nil {
		b.WriteString("\nexport AGENTFLOW_REPOSITORY_ID=" + shellSingleQuote(target.Repository.RepositoryID))
		b.WriteString("\nexport AGENTFLOW_REPOSITORY_NAME=" + shellSingleQuote(target.Repository.Name))
		b.WriteString("\nexport AGENTFLOW_REPOSITORY_DIR=" + shellSingleQuote(target.directory()))
		if dir := target.directory(); dir != "" {
			b.WriteString("\ncd -- " + shellSingleQuote(dir))
		}
	}
	b.WriteString("\n" + command)
	return b.String()
}

func executeForTargets(run domain.Run, scope string, execute func(executionTarget) (string, error)) (string, error) {
	targets, err := executionTargets(run, scope)
	if err != nil {
		return "", err
	}
	if len(targets) == 1 {
		result, runErr := execute(targets[0])
		if runErr != nil {
			return result, fmt.Errorf("%s: %w", targets[0].label(), runErr)
		}
		return result, nil
	}
	var output strings.Builder
	for _, target := range targets {
		result, runErr := execute(target)
		if len(targets) > 1 {
			fmt.Fprintf(&output, "## %s\n\n", target.label())
		}
		if strings.TrimSpace(result) != "" {
			output.WriteString(strings.TrimSpace(result))
			output.WriteString("\n")
		}
		if runErr != nil {
			return strings.TrimSpace(output.String()), fmt.Errorf("%s: %w", target.label(), runErr)
		}
	}
	return strings.TrimSpace(output.String()), nil
}

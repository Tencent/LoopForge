package domain

import (
	"fmt"
	"strings"
)

func commandHasContent(gate VerificationGate) bool {
	for _, command := range gate.Commands {
		if strings.TrimSpace(command) != "" {
			return true
		}
	}
	return false
}

func gateMode(gate VerificationGate) string {
	mode := strings.ToLower(strings.TrimSpace(gate.Mode))
	if mode == "" && commandHasContent(gate) {
		return "command"
	}
	return mode
}

// CommandNeedsRepository reports whether a gate command inspects or mutates a
// git checkout and therefore belongs in executionScope=repositories.
func CommandNeedsRepository(command string) bool {
	cmd := strings.TrimSpace(command)
	if rest, ok := strings.CutPrefix(cmd, "set -e"); ok {
		cmd = strings.TrimSpace(rest)
	}
	if cmd == "" {
		return false
	}
	return strings.Contains(cmd, "git ") || strings.Contains(cmd, "git\t") || strings.Contains(cmd, ".git/") || strings.Contains(cmd, "$(git")
}

func rewriteRepositoryWorkspaceRefs(command string) string {
	out := command
	if strings.Contains(out, ".agentflow/") && !strings.Contains(out, "$AGENTFLOW_WORKSPACE/.agentflow/") && !strings.Contains(out, "${AGENTFLOW_WORKSPACE}/.agentflow/") {
		out = strings.ReplaceAll(out, ".agentflow/", `$AGENTFLOW_WORKSPACE/.agentflow/`)
	}
	if strings.Contains(out, "git ") || strings.Contains(out, "git\t") {
		for _, name := range []string{"change-report.md", "fix-report.md", "e2e-cases.md"} {
			if strings.Contains(out, ":!"+name) || strings.Contains(out, ":(exclude)"+name) {
				continue
			}
			prefixed := `$AGENTFLOW_WORKSPACE/` + name
			if !strings.Contains(out, prefixed) {
				out = strings.ReplaceAll(out, name, prefixed)
			}
		}
	}
	return out
}

func uniqueGateID(base string, used map[string]bool) string {
	id := strings.TrimSpace(base)
	if id == "" {
		id = "gate-git"
	}
	if !used[id] {
		return id
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", id, i)
		if !used[candidate] {
			return candidate
		}
	}
}

// ValidateVerificationGate requires command gates to declare run or repositories.
// Manual gates and empty command lists may omit the field. Diff-coverage gates
// need repositories scope (they diff a checkout) plus a readable report spec.
func ValidateVerificationGate(gate VerificationGate) error {
	mode := gateMode(gate)
	switch mode {
	case "", GateModeCommand, GateModeManual, GateModeDiffCoverage:
	default:
		return fmt.Errorf("verification gate %s has invalid mode %q", gate.ID, gate.Mode)
	}
	scope := strings.TrimSpace(gate.ExecutionScope)
	if scope != "" && scope != ExecutionScopeRun && scope != ExecutionScopeRepositories {
		return fmt.Errorf("verification gate %s has invalid executionScope %q", gate.ID, gate.ExecutionScope)
	}
	if gate.Coverage != nil && mode != GateModeDiffCoverage {
		return fmt.Errorf("verification gate %s only accepts coverage settings with mode %s", gate.ID, GateModeDiffCoverage)
	}
	if mode == GateModeDiffCoverage {
		return validateDiffCoverageGate(gate, scope)
	}
	if mode == GateModeManual || !commandHasContent(gate) {
		return nil
	}
	if scope == "" {
		id := strings.TrimSpace(gate.ID)
		if id == "" {
			id = strings.TrimSpace(gate.Name)
		}
		if id == "" {
			id = "unnamed"
		}
		return fmt.Errorf("verification gate %s requires executionScope %q or %q", id, ExecutionScopeRun, ExecutionScopeRepositories)
	}
	return nil
}

func validateDiffCoverageGate(gate VerificationGate, scope string) error {
	id := strings.TrimSpace(gate.ID)
	if id == "" {
		id = strings.TrimSpace(gate.Name)
	}
	if id == "" {
		id = "unnamed"
	}
	if scope != ExecutionScopeRepositories {
		return fmt.Errorf("diff_coverage gate %s requires executionScope %q", id, ExecutionScopeRepositories)
	}
	if !commandHasContent(gate) {
		return fmt.Errorf("diff_coverage gate %s requires commands that produce a coverage report", id)
	}
	spec := gate.Coverage
	if spec == nil {
		return fmt.Errorf("diff_coverage gate %s requires a coverage report spec", id)
	}
	if strings.TrimSpace(spec.ReportPath) == "" {
		return fmt.Errorf("diff_coverage gate %s requires coverage.reportPath", id)
	}
	format := strings.ToLower(strings.TrimSpace(spec.ReportFormat))
	if format == "" {
		return fmt.Errorf("diff_coverage gate %s requires coverage.reportFormat (%s)", id, strings.Join(GateCoverageReportFormats, ", "))
	}
	known := false
	for _, candidate := range GateCoverageReportFormats {
		if format == candidate {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("diff_coverage gate %s has invalid coverage.reportFormat %q, want one of %s", id, spec.ReportFormat, strings.Join(GateCoverageReportFormats, ", "))
	}
	if spec.ThresholdPct != nil && (*spec.ThresholdPct < 0 || *spec.ThresholdPct > 100) {
		return fmt.Errorf("diff_coverage gate %s has invalid coverage.thresholdPct %v, want 0-100", id, *spec.ThresholdPct)
	}
	if spec.MinChangedLines != nil && *spec.MinChangedLines < 0 {
		return fmt.Errorf("diff_coverage gate %s has invalid coverage.minChangedLines %d, want 0 or more", id, *spec.MinChangedLines)
	}
	return nil
}

func ValidateVerificationGates(gates []VerificationGate) error {
	for _, gate := range gates {
		if err := ValidateVerificationGate(gate); err != nil {
			return err
		}
	}
	return nil
}

// SplitVerificationGates separates legacy empty-scope gates into an explicit
// run-scoped deliverable gate and a repositories-scoped git gate. Gates that
// already declare a scope are left unchanged.
func SplitVerificationGates(gates []VerificationGate) []VerificationGate {
	used := make(map[string]bool, len(gates)*2)
	for _, gate := range gates {
		if id := strings.TrimSpace(gate.ID); id != "" {
			used[id] = true
		}
	}
	out := make([]VerificationGate, 0, len(gates)+4)
	for _, gate := range gates {
		if strings.TrimSpace(gate.ExecutionScope) != "" {
			out = append(out, gate)
			continue
		}
		runCmds, repoCmds := partitionGateCommands(gate.Commands)
		if len(runCmds) == 0 && len(repoCmds) == 0 {
			out = append(out, gate)
			continue
		}
		if len(repoCmds) == 0 {
			gate.Commands = runCmds
			gate.ExecutionScope = ExecutionScopeRun
			out = append(out, gate)
			continue
		}
		if len(runCmds) == 0 {
			gate.Commands = repoCmds
			gate.ExecutionScope = ExecutionScopeRepositories
			out = append(out, gate)
			continue
		}
		runGate := gate
		runGate.Commands = runCmds
		runGate.ExecutionScope = ExecutionScopeRun
		repoGate := repositoryGitGate(gate, repoCmds, used)
		out = append(out, runGate, repoGate)
	}
	return out
}

// partitionGateCommands drops blank commands and splits the rest into
// run-scoped deliverable checks and repository-scoped git checks. Repository
// commands are rewritten so run-root paths keep resolving from each repo.
func partitionGateCommands(commands []string) (runCmds, repoCmds []string) {
	for _, command := range commands {
		if strings.TrimSpace(command) == "" {
			continue
		}
		if CommandNeedsRepository(command) {
			repoCmds = append(repoCmds, rewriteRepositoryWorkspaceRefs(command))
			continue
		}
		runCmds = append(runCmds, command)
	}
	return runCmds, repoCmds
}

// repositoryGitGate derives the repositories-scoped companion of gate that
// runs repoCmds, reserving its unique "-git" ID in used.
func repositoryGitGate(gate VerificationGate, repoCmds []string, used map[string]bool) VerificationGate {
	repoGate := gate
	baseID := strings.TrimSpace(gate.ID)
	if baseID == "" {
		baseID = "gate"
	}
	repoGate.ID = uniqueGateID(baseID+"-git", used)
	used[repoGate.ID] = true
	if name := strings.TrimSpace(gate.Name); name != "" {
		repoGate.Name = name + " · Git 检查"
	} else {
		repoGate.Name = "Git 检查"
	}
	repoGate.Commands = repoCmds
	repoGate.ExecutionScope = ExecutionScopeRepositories
	return repoGate
}

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
// Manual gates and empty command lists may omit the field.
func ValidateVerificationGate(gate VerificationGate) error {
	mode := gateMode(gate)
	switch mode {
	case "", "command", "manual":
	default:
		return fmt.Errorf("verification gate %s has invalid mode %q", gate.ID, gate.Mode)
	}
	scope := strings.TrimSpace(gate.ExecutionScope)
	if scope != "" && scope != ExecutionScopeRun && scope != ExecutionScopeRepositories {
		return fmt.Errorf("verification gate %s has invalid executionScope %q", gate.ID, gate.ExecutionScope)
	}
	if mode == "manual" || !commandHasContent(gate) {
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
		var runCmds, repoCmds []string
		for _, command := range gate.Commands {
			if strings.TrimSpace(command) == "" {
				continue
			}
			if CommandNeedsRepository(command) {
				repoCmds = append(repoCmds, rewriteRepositoryWorkspaceRefs(command))
				continue
			}
			runCmds = append(runCmds, command)
		}
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
		out = append(out, runGate, repoGate)
	}
	return out
}

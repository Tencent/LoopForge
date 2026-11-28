package domain

import "slices"

const (
	ExecutionTargetCluster     = "cluster"
	ExecutionTargetClusterAuto = "cluster-auto"
	ExecutionTargetMachineAuto = "machine-auto"
	ExecutionTargetPersonal    = "personal"
	ExecutionTargetProject     = "project"
)

// ExecutionTarget describes a requested placement or an already bound target.
type ExecutionTarget struct {
	Pool      string `json:"pool"`
	RunnerID  string `json:"runnerId,omitempty"`
	ClusterID string `json:"clusterId,omitempty"`
}

func (t ExecutionTarget) ValidSelection() bool {
	switch t.Pool {
	case ExecutionModeRunner:
		return t.RunnerID != "" && t.ClusterID == ""
	case ExecutionTargetCluster:
		return t.ClusterID != "" && t.RunnerID == ""
	case ExecutionModeCloud, ExecutionTargetMachineAuto, ExecutionTargetClusterAuto, ExecutionTargetPersonal, ExecutionTargetProject:
		return t.RunnerID == "" && t.ClusterID == ""
	default:
		return false
	}
}

// ExecutionCluster is an OpenSandbox endpoint available as a distinct execution target.
// Empty ProjectIDs means the cluster is available platform-wide.
type ExecutionCluster struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Enabled    bool     `json:"enabled"`
	ProjectIDs []string `json:"projectIds,omitempty"`
	OpenSandboxConnection
}

func (c ExecutionCluster) AllowsProject(projectID string) bool {
	return len(c.ProjectIDs) == 0 || slices.Contains(c.ProjectIDs, projectID)
}

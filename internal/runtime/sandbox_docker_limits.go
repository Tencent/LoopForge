package runtime

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"agentflow/internal/domain"
)

const (
	dockerSandboxLimitPrefix = "AGENTFLOW_DOCKER_SANDBOX_"
	dockerSandboxLimitsLabel = "agentflow.sandbox-limits"
)

// Docker limits combine profile settings with caps set by the execution
// machine. Command environment values cannot change the container limits.
type dockerSandboxLimits struct {
	cpuMillicores int64
	memoryBytes   int64
	pids          int64
}

type dockerHostLimits struct {
	NanoCPUs  int64 `json:"NanoCpus"`
	Memory    int64
	PidsLimit int64
}

func (l dockerSandboxLimits) labelValue() string {
	if l == (dockerSandboxLimits{}) {
		return ""
	}
	return fmt.Sprintf("%d/%d/%d", l.cpuMillicores, l.memoryBytes, l.pids)
}

func (l dockerSandboxLimits) createArgs() []string {
	var args []string
	if l.cpuMillicores != 0 {
		args = append(args, "--cpus", fmt.Sprintf("%d.%03d", l.cpuMillicores/1000, l.cpuMillicores%1000))
	}
	if l.memoryBytes != 0 {
		args = append(args, "--memory", strconv.FormatInt(l.memoryBytes, 10))
	}
	if l.pids != 0 {
		args = append(args, "--pids-limit", strconv.FormatInt(l.pids, 10))
	}
	// Override any label inherited from the image, including for an empty policy.
	return append(args, "--label", dockerSandboxLimitsLabel+"="+l.labelValue())
}

func (l dockerSandboxLimits) matches(metadata *dockerWorkspaceInspection) bool {
	return metadata != nil && metadata.limitsLabel == l.labelValue() &&
		(l.cpuMillicores == 0 || metadata.limits.NanoCPUs == l.cpuMillicores*1_000_000) &&
		(l.memoryBytes == 0 || metadata.limits.Memory == l.memoryBytes) &&
		(l.pids == 0 || metadata.limits.PidsLimit == l.pids)
}

func readDockerSandboxLimits(profile domain.SandboxProfile) (dockerSandboxLimits, error) {
	if err := profile.ValidateResourceLimits(); err != nil {
		return dockerSandboxLimits{}, err
	}
	cpu, err := readDockerSandboxLimit("CPU_MILLICORES", domain.DockerSandboxMinCPUMillicores, domain.DockerSandboxMaxCPUMillicores)
	if err != nil {
		return dockerSandboxLimits{}, err
	}
	memory, err := readDockerSandboxLimit("MEMORY_MIB", domain.DockerSandboxMinMemoryMiB, domain.DockerSandboxMaxMemoryMiB)
	if err != nil {
		return dockerSandboxLimits{}, err
	}
	pids, err := readDockerSandboxLimit("PIDS", 1, math.MaxInt32)
	if err != nil {
		return dockerSandboxLimits{}, err
	}
	return dockerSandboxLimits{
		cpuMillicores: lowerDockerLimit(cpu, profile.CPUMillicores),
		memoryBytes:   lowerDockerLimit(memory, profile.MemoryMiB) << 20,
		pids:          pids,
	}, nil
}

func lowerDockerLimit(host, profile int64) int64 {
	switch {
	case host == 0:
		return profile
	case profile == 0:
		return host
	default:
		return min(host, profile)
	}
}

func readDockerSandboxLimit(key string, min, max int64) (int64, error) {
	name := dockerSandboxLimitPrefix + key
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value != 0 && (value < min || value > max) {
		return 0, fmt.Errorf("%s must be 0 or an integer between %d and %d", name, min, max)
	}
	return value, nil
}

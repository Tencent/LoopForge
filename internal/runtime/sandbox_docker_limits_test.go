package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestDockerSandboxResourceLimitConfig(t *testing.T) {
	const prefix = dockerSandboxLimitPrefix
	t.Setenv(prefix+"CPU_MILLICORES", "1250")
	t.Setenv(prefix+"MEMORY_MIB", "64")
	t.Setenv(prefix+"PIDS", "0")
	profile := domain.SandboxProfile{Driver: domain.SandboxDriverDocker}
	limits, err := readDockerSandboxLimits(profile)
	if err != nil || limits != (dockerSandboxLimits{cpuMillicores: 1250, memoryBytes: 64 << 20}) {
		t.Fatalf("limits = %+v, err = %v", limits, err)
	}
	if args := strings.Join(limits.createArgs(), " "); !strings.HasPrefix(args, "--cpus 1.250 --memory 67108864 --label ") {
		t.Fatalf("Docker flags = %s", args)
	}
	want := &dockerWorkspaceInspection{
		limitsLabel: limits.labelValue(),
		limits:      dockerHostLimits{NanoCPUs: 1_250_000_000, Memory: 64 << 20},
	}
	if !limits.matches(want) || (dockerSandboxLimits{}).matches(want) {
		t.Fatal("Docker policy change should replace the old container")
	}
	want.limits.Memory = 0
	if limits.matches(want) {
		t.Fatal("accepted a container without the requested memory constraint")
	}
	requested := profile
	requested.CPUMillicores = 750
	requested.MemoryMiB = 128
	limits, err = readDockerSandboxLimits(requested)
	if err != nil || limits != (dockerSandboxLimits{cpuMillicores: 750, memoryBytes: 64 << 20}) {
		t.Fatalf("lower profile or machine limits = %+v, err = %v", limits, err)
	}
	for _, tc := range []struct{ key, value string }{
		{"CPU_MILLICORES", "9"},
		{"MEMORY_MIB", "5"},
		{"MEMORY_MIB", "8796093022208"},
		{"PIDS", "-1"},
		{"PIDS", "1.5"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv(prefix+tc.key, tc.value)
			if _, err := readDockerSandboxLimits(profile); err == nil || !strings.Contains(err.Error(), prefix+tc.key) {
				t.Fatalf("invalid limit accepted, err = %v", err)
			}
		})
	}
}

func TestDockerPIDLimitFailureDoesNotInvalidateWorkspace(t *testing.T) {
	stderr := "OCI runtime exec failed: unable to start container process: resource temporarily unavailable"
	if isDockerCwdUnsafe(stderr, errors.New("exit status 126")) {
		t.Fatal("PID limit failure was treated as an invalid workspace mount")
	}
}

func TestDockerSandboxResourceLimitsOnLiveDaemon(t *testing.T) {
	if os.Getenv("AGENTFLOW_SANDBOX_SMOKE") != "1" {
		t.Skip("set AGENTFLOW_SANDBOX_SMOKE=1 for live Docker sandbox verification")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	version, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.CgroupVersion}}").Output()
	if err != nil || strings.TrimSpace(string(version)) != "2" {
		t.Skip("Docker with cgroup v2 is required")
	}
	const image = "alpine:3.20"
	if !dockerImageExists(ctx, image) {
		t.Skip("load alpine:3.20 in the local Docker daemon")
	}
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", image)
	// Docker VMs can mount the checkout even when the system temp directory is private.
	root, err := os.MkdirTemp(".", ".sandbox-limits-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	workspace, err := filepath.Abs(filepath.Join(root, "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_DOCKER_NETWORK", "")
	id := fmt.Sprintf("test-limits-%d", time.Now().UnixNano())
	name := dockerContainerName(id)
	t.Cleanup(func() { _ = removeDockerContainer(context.Background(), name) })
	profile := domain.SandboxProfile{Driver: domain.SandboxDriverDocker}
	resources := SandboxResources{Dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	open := func(profile domain.SandboxProfile) Sandbox {
		t.Helper()
		sandbox, err := OpenRemoteSandbox(ctx, id, workspace, profile, SandboxRequirements{}, resources)
		if err != nil {
			t.Fatal(err)
		}
		return sandbox
	}
	execute := func(sandbox Sandbox, args ...string) string {
		t.Helper()
		output, err := sandbox.Exec(ctx, CommandSpec{Argv: args}, nil)
		if err != nil {
			t.Fatalf("sandbox Exec: %v %s", err, output)
		}
		return output
	}
	containerID := func() string {
		t.Helper()
		output, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.Id}}", name).Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(output)
	}
	t.Setenv(dockerSandboxLimitPrefix+"CPU_MILLICORES", "")
	t.Setenv(dockerSandboxLimitPrefix+"MEMORY_MIB", "")
	t.Setenv(dockerSandboxLimitPrefix+"PIDS", "")
	execute(open(profile), "sh", "-c", "touch /workspace/checkout")
	original := containerID()
	t.Setenv(dockerSandboxLimitPrefix+"CPU_MILLICORES", "500")
	t.Setenv(dockerSandboxLimitPrefix+"PIDS", "32")
	limited := profile
	limited.CPUMillicores, limited.MemoryMiB = 250, 64
	output := execute(open(limited), "cat", "/sys/fs/cgroup/cpu.max", "/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/pids.max")
	if output != "25000 100000\n67108864\n32\n" || containerID() == original {
		t.Fatalf("capped container limits = %q", output)
	}
	capped := containerID()
	execute(open(limited), "test", "-f", "/workspace/checkout")
	if containerID() != capped {
		t.Fatal("reopening the sandbox with identical limits replaced the container")
	}
	t.Setenv(dockerSandboxLimitPrefix+"CPU_MILLICORES", "0")
	t.Setenv(dockerSandboxLimitPrefix+"PIDS", "0")
	execute(open(profile), "test", "-f", "/workspace/checkout")
	if containerID() == capped {
		t.Fatal("clearing the configured limits retained the capped container")
	}
}

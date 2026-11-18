package verification

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultMavenImage is the toolchain that runs tests. It pins the JDK, so a
// toolchain fingerprint derived from this string identifies the grading
// environment. Override it through the /verify request's mavenImage.
const DefaultMavenImage = "maven:3.9.11-eclipse-temurin-21"

// DefaultMavenCommand mirrors AegisOS VerificationRunner: surefire keeps
// running after failures, and the verdict comes from the reports.
const DefaultMavenCommand = "mvn -B -Dmaven.test.failure.ignore=true test"

// MavenRunner executes a Maven test command inside a throwaway container.
type MavenRunner struct {
	// Docker is the CLI binary; empty means "docker".
	Docker string
	// Image is the toolchain image; empty means DefaultMavenImage.
	Image string
	// Timeout bounds the whole run; AegisOS allows 30 minutes.
	Timeout time.Duration
	// Network is passed to docker run --network; empty keeps Docker's default.
	Network string
}

// TestOutcome is the acceptance decision and its evidence.
type TestOutcome struct {
	Status     string           `json:"status"` // PASS, FAIL, SKIPPED
	Reason     string           `json:"reason,omitempty"`
	ExitCode   int              `json:"exitCode,omitempty"`
	DurationMs int64            `json:"durationMs,omitempty"`
	Surefire   *SurefireSummary `json:"surefire,omitempty"`
}

// RunTests runs command (default DefaultMavenCommand) in image with the repo
// mounted at /work, then reads surefire reports from the repo. The exit code is
// recorded but never decides the verdict; the reports do.
func (r MavenRunner) RunTests(ctx context.Context, repo, command string) (TestOutcome, error) {
	docker := strings.TrimSpace(r.Docker)
	if docker == "" {
		docker = "docker"
	}
	image := strings.TrimSpace(r.Image)
	if image == "" {
		image = DefaultMavenImage
	}
	command = strings.TrimSpace(command)
	if command == "" {
		command = DefaultMavenCommand
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"run", "--rm", "-v", repo + ":/work", "-w", "/work"}
	if network := strings.TrimSpace(r.Network); network != "" {
		args = append(args, "--network", network)
	}
	args = append(args, image, "sh", "-c", command)
	started := time.Now()
	cmd := exec.CommandContext(callCtx, docker, args...)
	output, err := cmd.CombinedOutput()
	outcome := TestOutcome{DurationMs: time.Since(started).Milliseconds()}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			outcome.ExitCode = exitErr.ExitCode()
		} else {
			return outcome, fmt.Errorf("run maven container: %w", err)
		}
	}
	summary, parseErr := ParseSurefire(repo)
	if parseErr != nil {
		return outcome, fmt.Errorf("read surefire reports: %w", parseErr)
	}
	outcome.Surefire = &summary
	if summary.Pass() {
		outcome.Status = "PASS"
		outcome.Reason = fmt.Sprintf("%d tests in %d report(s)", summary.Tests, summary.Reports)
		return outcome, nil
	}
	outcome.Status = "FAIL"
	outcome.Reason = summary.FailuresText()
	if tail := tailLines(string(output), 20); tail != "" && summary.Reports == 0 {
		outcome.Reason += ": " + tail
	}
	return outcome, nil
}

func tailLines(value string, limit int) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return strings.Join(lines, " | ")
}

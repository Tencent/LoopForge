package verification

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultAcceptanceImage is the locally built JUnit runner
// (deploy/verify-acceptance/Dockerfile). It is not pulled from a registry: the
// image is built once from this repo and pinned by tag.
const DefaultAcceptanceImage = "loopforge-verify-acceptance:local"

// AcceptanceOutcome is the acceptance decision from the JUnit console run.
type AcceptanceOutcome struct {
	Status     string `json:"status"` // PASS, FAIL
	Reason     string `json:"reason,omitempty"`
	ExitCode   int    `json:"exitCode,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Tests      int    `json:"tests,omitempty"`
	Failed     int    `json:"failed,omitempty"`
}

// AcceptanceRunner compiles and runs generated JUnit specs in the acceptance
// image. The verdict follows AegisOS: tests must have run and none may fail;
// a green console with zero tests is not a pass.
type AcceptanceRunner struct {
	Docker  string
	Image   string
	Timeout time.Duration
	// Env is passed into the runner container; LOOPFORGE_SUBJECT_URL becomes
	// -Daegis.baseUrl for the generated tests.
	Env map[string]string
}

func (r AcceptanceRunner) Run(ctx context.Context, specDir string) (AcceptanceOutcome, error) {
	docker := strings.TrimSpace(r.Docker)
	if docker == "" {
		docker = "docker"
	}
	image := strings.TrimSpace(r.Image)
	if image == "" {
		image = DefaultAcceptanceImage
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()
	args := []string{"run", "--rm", "--add-host=host.docker.internal:host-gateway", "-v", specDir + ":/spec:ro"}
	for key, value := range r.Env {
		if key != "" {
			args = append(args, "-e", key+"="+value)
		}
	}
	args = append(args, image, "/spec")
	cmd := exec.CommandContext(callCtx, docker, args...)
	output, err := cmd.CombinedOutput()
	outcome := AcceptanceOutcome{DurationMs: time.Since(started).Milliseconds()}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return outcome, fmt.Errorf("run acceptance container: %w", err)
		}
		outcome.ExitCode = exitErr.ExitCode()
	}
	tests, failed, parsed := parseConsoleSummary(string(output))
	outcome.Tests, outcome.Failed = tests, failed
	switch {
	case !parsed || tests == 0:
		outcome.Status = "FAIL"
		outcome.Reason = "no JUnit tests ran: " + tailLines(string(output), 8)
	case outcome.ExitCode != 0 || failed > 0:
		outcome.Status = "FAIL"
		outcome.Reason = fmt.Sprintf("%d of %d acceptance tests failed", failed, tests)
	default:
		outcome.Status = "PASS"
		outcome.Reason = fmt.Sprintf("%d acceptance tests passed", tests)
	}
	return outcome, nil
}

var consoleCount = regexp.MustCompile(`(\d+) tests (found|skipped|started|aborted|successful|failed)`)

// parseConsoleSummary reads the JUnit console launcher's summary block.
func parseConsoleSummary(output string) (tests, failed int, ok bool) {
	matches := consoleCount.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return 0, 0, false
	}
	for _, match := range matches {
		count, _ := strconv.Atoi(match[1])
		switch match[2] {
		case "found":
			tests = count
		case "failed":
			failed = count
		}
	}
	return tests, failed, true
}

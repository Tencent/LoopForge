package verification

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
	// CacheDir, when set, is mounted at /root/.m2 so repeated runs reuse the
	// local Maven repository instead of downloading it again.
	CacheDir string
	// Offline adds -o, so Maven never reaches the network and fails when the
	// cache is incomplete instead of silently downloading.
	Offline bool
	// Runs repeats the graded run; three runs are required to call a test flaky.
	Runs int
	// MirrorURL, when set, redirects "central" through this Maven repository.
	// The settings file lives in the cache directory, so it travels with it.
	MirrorURL string
}

// TestOutcome is the acceptance decision and its evidence.
type TestOutcome struct {
	Status        string           `json:"status"` // PASS, FAIL, SKIPPED
	Reason        string           `json:"reason,omitempty"`
	ExitCode      int              `json:"exitCode,omitempty"`
	DurationMs    int64            `json:"durationMs,omitempty"`
	Surefire      *SurefireSummary `json:"surefire,omitempty"`
	Runs          int              `json:"runs,omitempty"`
	Verdicts      map[string]bool  `json:"-"`
	Flaky         []string         `json:"flaky,omitempty"`
	AlwaysFailing []string         `json:"alwaysFailing,omitempty"`
	Output        string           `json:"-"`
}

// RunTests runs command (default DefaultMavenCommand) in the image with the
// repo mounted at /work, then reads surefire reports. Repeats (Runs, default 1,
// capped at 3) detect flaky tests; the exit code never decides the verdict.
func (r MavenRunner) RunTests(ctx context.Context, repo, command string) (TestOutcome, error) {
	if err := r.prepareSettings(); err != nil {
		return TestOutcome{}, err
	}
	runs := r.Runs
	if runs < 1 {
		runs = 1
	}
	if runs > 3 {
		runs = 3
	}
	histories := map[string][]bool{}
	var outcome TestOutcome
	var verdicts map[string]bool
	for run := 1; run <= runs; run++ {
		one, current, err := r.runOnce(ctx, repo, command)
		if err != nil {
			return one, err
		}
		outcome = one
		verdicts = current
		for name, failed := range current {
			histories[name] = append(histories[name], failed)
		}
	}
	outcome.Runs = runs
	outcome.Verdicts = verdicts
	if runs >= 3 {
		outcome.Flaky, outcome.AlwaysFailing = FlakyTests(histories)
	}
	executed := outcome.Surefire != nil && outcome.Surefire.Reports > 0
	red := !executed || outcome.Surefire.Failures+outcome.Surefire.Errors > 0 || len(outcome.AlwaysFailing) > 0
	switch {
	case !executed:
		outcome.Status = "FAIL"
		outcome.Reason = "no surefire reports found: " + tailLines(outcome.Output, 20)
	case red:
		outcome.Status = "FAIL"
		outcome.Reason = outcome.Surefire.FailuresText()
		if len(outcome.AlwaysFailing) > 0 {
			outcome.Reason += fmt.Sprintf("; failed in every run: %s", strings.Join(outcome.AlwaysFailing, ", "))
		}
	default:
		outcome.Status = "PASS"
		outcome.Reason = fmt.Sprintf("%d tests in %d report(s)", outcome.Surefire.Tests, outcome.Surefire.Reports)
	}
	if runs >= 3 && len(outcome.Flaky) > 0 {
		outcome.Reason += fmt.Sprintf("; flaky: %s", strings.Join(outcome.Flaky, ", "))
	}
	return outcome, nil
}

// BaselineVerdicts runs the tests on a detached worktree at baseCommit so the
// caller can diff pre-existing failures against the working tree.
func (r MavenRunner) BaselineVerdicts(ctx context.Context, repo, baseCommit, command string) (map[string]bool, error) {
	dir, cleanup, err := PrepareBaseDir(ctx, repo, baseCommit)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	outcome, verdicts, err := r.runOnce(ctx, dir, command)
	if err != nil {
		return nil, err
	}
	if outcome.Surefire == nil || outcome.Surefire.Reports == 0 {
		return nil, fmt.Errorf("baseline run produced no surefire reports: %s", outcome.Reason)
	}
	return verdicts, nil
}

// runOnce executes one container run and parses its reports.
func (r MavenRunner) runOnce(ctx context.Context, repo, command string) (TestOutcome, map[string]bool, error) {
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
	if cache := strings.TrimSpace(r.CacheDir); cache != "" {
		args = append(args, "-v", cache+":/root/.m2")
	}
	mavenArgs := []string{}
	if strings.TrimSpace(r.MirrorURL) != "" {
		mavenArgs = append(mavenArgs, "-s /root/.m2/settings.xml")
	}
	if r.Offline {
		mavenArgs = append(mavenArgs, "-o")
	}
	if len(mavenArgs) > 0 {
		args = append(args, "-e", "MAVEN_ARGS="+strings.Join(mavenArgs, " "))
	}
	if err := r.prepareSettings(); err != nil {
		return TestOutcome{}, nil, err
	}
	if network := strings.TrimSpace(r.Network); network != "" {
		args = append(args, "--network", network)
	}
	args = append(args, image, "sh", "-c", command)
	started := time.Now()
	output, err := exec.CommandContext(callCtx, docker, args...).CombinedOutput()
	outcome := TestOutcome{DurationMs: time.Since(started).Milliseconds(), Output: tailLines(string(output), 40)}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return outcome, nil, fmt.Errorf("run maven container: %w", err)
		}
		outcome.ExitCode = exitErr.ExitCode()
	}
	summary, verdicts, parseErr := ParseSurefireResults(repo)
	if parseErr != nil {
		return outcome, verdicts, fmt.Errorf("read surefire reports: %w", parseErr)
	}
	outcome.Surefire = &summary
	return outcome, verdicts, nil
}

// prepareSettings writes the mirror settings into the cache directory once.
func (r MavenRunner) prepareSettings() error {
	mirror := strings.TrimSpace(r.MirrorURL)
	cache := strings.TrimSpace(r.CacheDir)
	if mirror == "" || cache == "" {
		return nil
	}
	content := fmt.Sprintf(`<settings><mirrors><mirror><id>loopforge-mirror</id><mirrorOf>central</mirrorOf><url>%s</url></mirror></mirrors></settings>
`, mirror)
	path := filepath.Join(cache, "settings.xml")
	if current, err := os.ReadFile(path); err == nil && string(current) == content {
		return nil
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// Prewarm downloads the project's dependencies and plugins into CacheDir so a
// later Offline run can resolve them without the network. It uses the same
// image and cache mount as RunTests.
func (r MavenRunner) Prewarm(ctx context.Context, repo string) error {
	docker := strings.TrimSpace(r.Docker)
	if docker == "" {
		docker = "docker"
	}
	image := strings.TrimSpace(r.Image)
	if image == "" {
		image = DefaultMavenImage
	}
	cache := strings.TrimSpace(r.CacheDir)
	if cache == "" {
		return errors.New("prewarm requires a Maven cache directory")
	}
	if err := r.prepareSettings(); err != nil {
		return err
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	base := []string{"run", "--rm", "-v", repo + ":/work", "-w", "/work", "-v", cache + ":/root/.m2", image,
		"sh", "-c"}
	online := exec.CommandContext(callCtx, docker, append(base, "mvn -B -s /root/.m2/settings.xml -Dmaven.test.failure.ignore=true package")...)
	if output, err := online.CombinedOutput(); err != nil {
		return fmt.Errorf("prewarm maven cache (online): %v: %s", err, tailLines(string(output), 20))
	}
	// AegisOS rejects dependency:go-offline as insufficient: only an identical
	// offline run proves the cache can grade without the network.
	offlineCtx, cancelOffline := context.WithTimeout(ctx, timeout)
	defer cancelOffline()
	offline := exec.CommandContext(offlineCtx, docker, append(base, "mvn -o -B -s /root/.m2/settings.xml -Dmaven.test.failure.ignore=true package")...)
	if output, err := offline.CombinedOutput(); err != nil {
		return fmt.Errorf("prewarm maven cache (offline proof): %v: %s", err, tailLines(string(output), 20))
	}
	stripRemoteRepositories(cache)
	return nil
}

// stripRemoteRepositories drops Maven's origin markers so offline resolution
// does not reject artifacts that came from a mirror.
func stripRemoteRepositories(cache string) {
	_ = filepath.WalkDir(cache, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if entry.Name() == "_remote.repositories" {
			_ = os.Remove(path)
		}
		return nil
	})
}

func tailLines(value string, limit int) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return strings.Join(lines, " | ")
}

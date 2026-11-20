package verification

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseConsoleSummary(t *testing.T) {
	output := `
Test run finished after 120 ms
[         3 tests found           ]
[         1 tests skipped         ]
[         3 tests started         ]
[         0 tests aborted         ]
[         2 tests successful      ]
[         1 tests failed          ]
`
	tests, failed, ok := parseConsoleSummary(output)
	if !ok || tests != 3 || failed != 1 {
		t.Fatalf("parsed tests=%d failed=%d ok=%v", tests, failed, ok)
	}
	if _, _, ok := parseConsoleSummary("no summary here"); ok {
		t.Fatal("output without a summary parsed as valid")
	}
}

func TestAcceptanceRunnerRealContainer(t *testing.T) {
	if os.Getenv("LOOPFORGE_VERIFY_E2E") != "1" {
		t.Skip("set LOOPFORGE_VERIFY_E2E=1 to run the real acceptance container test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("docker", "build", "-q", "-t", DefaultAcceptanceImage,
		filepath.Join(strings.TrimSpace(string(root)), "deploy", "verify-acceptance"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build acceptance image: %v\n%s", err, output)
	}
	runner := AcceptanceRunner{}

	passing := t.TempDir()
	writeFile(t, filepath.Join(passing, "PassingTest.java"), `
import org.junit.jupiter.api.Test;
import static org.junit.jupiter.api.Assertions.assertEquals;

class PassingTest {
    @Test
    void passes() {
        assertEquals(2, 1 + 1);
    }
}
`)
	outcome, err := runner.Run(context.Background(), passing)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "PASS" || outcome.Tests == 0 || outcome.Failed != 0 {
		t.Fatalf("passing spec = %+v", outcome)
	}

	failing := t.TempDir()
	writeFile(t, filepath.Join(failing, "FailingTest.java"), `
import org.junit.jupiter.api.Test;
import static org.junit.jupiter.api.Assertions.assertEquals;

class FailingTest {
    @Test
    void fails() {
        assertEquals(1, 2);
    }
}
`)
	outcome, err = runner.Run(context.Background(), failing)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "FAIL" || outcome.Failed == 0 {
		t.Fatalf("failing spec = %+v", outcome)
	}

	empty := t.TempDir()
	writeFile(t, filepath.Join(empty, "NotATest.java"), "class NotATest {}\n")
	outcome, err = runner.Run(context.Background(), empty)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "FAIL" || outcome.Tests != 0 {
		t.Fatalf("empty spec = %+v", outcome)
	}
}

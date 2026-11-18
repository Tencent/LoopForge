package verification

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestMavenRunnerRealContainer is the M1 gate: a real Maven run in the pinned
// toolchain image, with the verdict coming from surefire. It is opt-in because
// it pulls the Maven image and downloads dependencies.
func TestMavenRunnerRealContainer(t *testing.T) {
	if os.Getenv("LOOPFORGE_VERIFY_E2E") != "1" {
		t.Skip("set LOOPFORGE_VERIFY_E2E=1 to run the real Maven container test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}
	repo := mavenProject(t)
	runner := MavenRunner{CacheDir: t.TempDir(), Timeout: 20 * time.Minute, MirrorURL: mavenMirror()}

	passing, err := runner.RunTests(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if passing.Status != "PASS" || passing.Surefire == nil || passing.Surefire.Reports == 0 || passing.Surefire.Tests == 0 {
		t.Fatalf("passing project = %+v", passing)
	}

	writeFile(t, filepath.Join(repo, "src", "test", "java", "ExampleTest.java"), `
import org.junit.jupiter.api.Test;
import static org.junit.jupiter.api.Assertions.assertEquals;

class ExampleTest {
    @Test
    void fails() {
        assertEquals(1, 2);
    }
}
`)
	failing, err := runner.RunTests(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if failing.Status != "FAIL" || failing.Surefire == nil || failing.Surefire.Failures == 0 {
		t.Fatalf("failing project = %+v", failing)
	}
}

func mavenProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pom.xml"), `<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>example</groupId>
  <artifactId>verify-e2e</artifactId>
  <version>1</version>
  <properties>
    <maven.compiler.release>17</maven.compiler.release>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
  </properties>
  <dependencies>
    <dependency>
      <groupId>org.junit.jupiter</groupId>
      <artifactId>junit-jupiter</artifactId>
      <version>5.10.2</version>
      <scope>test</scope>
    </dependency>
  </dependencies>
  <build>
    <plugins>
      <plugin>
        <groupId>org.apache.maven.plugins</groupId>
        <artifactId>maven-surefire-plugin</artifactId>
        <version>3.2.5</version>
      </plugin>
    </plugins>
  </build>
</project>
`)
	writeFile(t, filepath.Join(root, "src", "test", "java", "ExampleTest.java"), `
import org.junit.jupiter.api.Test;
import static org.junit.jupiter.api.Assertions.assertEquals;

class ExampleTest {
    @Test
    void passes() {
        assertEquals(2, 1 + 1);
    }
}
`)
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMavenRunnerOfflineAfterPrewarm proves the offline path: dependency
// prewarm fills the cache, then the graded run resolves everything with -o.
func TestMavenRunnerOfflineAfterPrewarm(t *testing.T) {
	if os.Getenv("LOOPFORGE_VERIFY_E2E") != "1" {
		t.Skip("set LOOPFORGE_VERIFY_E2E=1 to run the real Maven container test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}
	repo := mavenProject(t)
	runner := MavenRunner{CacheDir: t.TempDir(), Timeout: 25 * time.Minute, MirrorURL: mavenMirror()}
	if err := runner.Prewarm(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	runner.Offline = true
	outcome, err := runner.RunTests(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "PASS" || outcome.Surefire == nil || outcome.Surefire.Tests == 0 {
		t.Fatalf("offline run = %+v", outcome)
	}
}

// mavenMirror keeps the E2E runs reproducible on networks where Maven Central's
// TLS is unreliable; the default is the public Aliyun mirror.
func mavenMirror() string {
	if value := os.Getenv("LOOPFORGE_MAVEN_MIRROR"); value != "" {
		return value
	}
	return "https://maven.aliyun.com/repository/public"
}

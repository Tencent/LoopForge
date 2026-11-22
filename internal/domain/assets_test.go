package domain

import "testing"

func TestValidateSandboxAssets(t *testing.T) {
	docker := func(d *SandboxDockerfile) SandboxProfile {
		return SandboxProfile{ID: "box", Driver: SandboxDriverDocker, Dockerfile: d}
	}
	native := func(i *SandboxInstall) SandboxProfile {
		return SandboxProfile{ID: "box", Driver: SandboxDriverNative, Install: i}
	}
	for _, tc := range []struct {
		name    string
		profile SandboxProfile
		valid   bool
	}{
		{"docker", docker(&SandboxDockerfile{Content: "FROM debian", Files: map[string]string{"etc/a.conf": "x"}}), true},
		{"docker without dockerfile", docker(nil), false},
		{"docker blank dockerfile", docker(&SandboxDockerfile{Content: " "}), false},
		{"docker context escape", docker(&SandboxDockerfile{Content: "FROM debian", Files: map[string]string{"../escape": "x"}}), false},
		{"docker with install", SandboxProfile{Driver: SandboxDriverDocker, Dockerfile: &SandboxDockerfile{Content: "FROM x"}, Install: &SandboxInstall{Script: "x"}}, false},
		{"native", native(&SandboxInstall{Script: "mkdir -p \"$PREFIX/bin\"", PathEntries: []string{"bin"}, Variables: map[string]string{"PHP_VERSION": "8.3.0"}}), true},
		{"native without install", native(nil), true},
		{"native with dockerfile", SandboxProfile{Driver: SandboxDriverNative, Dockerfile: &SandboxDockerfile{Content: "FROM x"}}, false},
		{"native bad variable", native(&SandboxInstall{Variables: map[string]string{"BAD=NAME": "1"}}), false},
		{"native path escape", native(&SandboxInstall{PathEntries: []string{"../bin"}}), false},
		{"native NUL script", native(&SandboxInstall{Script: "bad\x00script"}), false},
		{"local with install", SandboxProfile{Driver: SandboxDriverLocal, Install: &SandboxInstall{Script: "x"}}, false},
	} {
		if err := ValidateSandboxAssets(tc.profile); (err == nil) != tc.valid {
			t.Errorf("%s: err = %v, want valid %v", tc.name, err, tc.valid)
		}
	}
}

func TestRunnerSandboxNamesTheRecipeAfterTheSandbox(t *testing.T) {
	spec := RunnerSandbox(SandboxProfile{ID: "go", Name: "Go", ProjectID: "p", Driver: SandboxDriverDocker, Dockerfile: &SandboxDockerfile{Content: "FROM golang", Files: map[string]string{"a": "b"}}})
	if spec.Dockerfile == nil || spec.Dockerfile.ID != "go" || spec.Dockerfile.Name != "Go" || spec.Dockerfile.Content != "FROM golang" || spec.Dockerfile.Files["a"] != "b" || spec.Profile.DockerfileID != "go" {
		t.Fatalf("docker spec = %+v", spec)
	}
	if spec.Profile.Dockerfile != nil || spec.Environment != nil || spec.Profile.EnvironmentID != "" {
		t.Fatalf("the spec profile must not carry the embedded recipe: %+v", spec.Profile)
	}
	spec = RunnerSandbox(SandboxProfile{ID: "php", Name: "PHP", Driver: SandboxDriverNative, Install: &SandboxInstall{Script: "make", PathEntries: []string{"bin"}, Variables: map[string]string{"V": "$PREFIX/v"}}})
	if spec.Environment == nil || spec.Environment.ID != "php" || spec.Environment.InstallScript != "make" || spec.Environment.PathEntries[0] != "bin" || spec.Environment.Variables["V"] != "$PREFIX/v" || spec.Profile.EnvironmentID != "php" || spec.Profile.Install != nil || spec.Dockerfile != nil {
		t.Fatalf("native spec = %+v", spec)
	}
	if spec := RunnerSandbox(SandboxProfile{ID: "bare", Driver: SandboxDriverNative}); spec.Environment != nil || spec.Dockerfile != nil {
		t.Fatalf("a sandbox without recipe = %+v", spec)
	}
}

func TestValidateMCPPathEntries(t *testing.T) {
	valid := MCPServerDefinition{ID: "custom", Name: "Custom", Transport: "stdio", Command: "zg", PathEntries: []string{"tools/bin"}}
	if err := ValidateMCPServer(valid); err != nil {
		t.Fatal(err)
	}
	valid.PathEntries = []string{"../escape"}
	if err := ValidateMCPServer(valid); err == nil {
		t.Fatal("path traversal in MCP pathEntries should be rejected")
	}
	valid.PathEntries = []string{"/usr/local/bin"}
	if err := ValidateMCPServer(valid); err == nil {
		t.Fatal("absolute MCP pathEntries should be rejected")
	}
}

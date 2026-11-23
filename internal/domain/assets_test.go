package domain

import "testing"

func TestValidateSandboxAssets(t *testing.T) {
	docker := func(d *SandboxDockerfile) SandboxProfile {
		return SandboxProfile{ID: "box", Driver: SandboxDriverDocker, Dockerfile: d}
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
		{"native", SandboxProfile{Driver: SandboxDriverNative, Environment: map[string]string{"PHP_VERSION": "8.3.0"}}, true},
		{"native with dockerfile", SandboxProfile{Driver: SandboxDriverNative, Dockerfile: &SandboxDockerfile{Content: "FROM x"}}, false},
		{"native bad variable", SandboxProfile{Driver: SandboxDriverNative, Environment: map[string]string{"BAD=NAME": "1"}}, false},
		{"local", SandboxProfile{Driver: SandboxDriverLocal}, false},
		{"unknown driver", SandboxProfile{Driver: "vm"}, false},
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
	if spec.Profile.Dockerfile != nil {
		t.Fatalf("the spec profile must not carry the embedded recipe: %+v", spec.Profile)
	}
	if spec := RunnerSandbox(SandboxProfile{ID: "bare", Driver: SandboxDriverNative, Environment: map[string]string{"A": "1"}}); spec.Dockerfile != nil || spec.Profile.Environment["A"] != "1" {
		t.Fatalf("a native sandbox = %+v", spec)
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

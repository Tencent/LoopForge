package domain

import "testing"

func TestValidateDockerfile(t *testing.T) {
	if err := ValidateDockerfile(DockerfileDefinition{ID: "runtime", Name: "Runtime"}); err == nil {
		t.Fatal("empty dockerfile content should be rejected")
	}
	if err := ValidateDockerfile(DockerfileDefinition{ID: "runtime", Name: "Runtime", Content: "FROM debian", Files: map[string]string{"../escape": "x"}}); err == nil {
		t.Fatal("path traversal in dockerfile files should be rejected")
	}
	if err := ValidateDockerfile(DockerfileDefinition{ID: "runtime", Name: "Runtime", Content: "FROM debian"}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateEnvironment(t *testing.T) {
	if err := ValidateEnvironment(EnvironmentDefinition{ID: "php8", Name: "PHP 8", Variables: map[string]string{"BAD=NAME": "1"}}); err == nil {
		t.Fatal("invalid environment variable name should be rejected")
	}
	if err := ValidateEnvironment(EnvironmentDefinition{ID: "php8", Name: "PHP 8", PathEntries: []string{"../bin"}}); err == nil {
		t.Fatal("path traversal in pathEntries should be rejected")
	}
	if err := ValidateEnvironment(EnvironmentDefinition{ID: "php8", Name: "PHP 8", Variables: map[string]string{"PHP_VERSION": "8.3.0"}, PathEntries: []string{"bin"}, InstallScript: "mkdir -p \"$PREFIX/bin\""}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEnvironment(EnvironmentDefinition{ID: "php8", Name: "PHP 8", InstallScript: "bad\x00script"}); err == nil {
		t.Fatal("NUL in install.sh should be rejected")
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

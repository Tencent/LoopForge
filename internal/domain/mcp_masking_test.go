package domain

import (
	"reflect"
	"testing"
)

func TestSensitiveEnvironmentKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"GITHUB_TOKEN", true},
		{"github_token", true},
		{"ClientSecret", true},
		{"DB_PASSWORD", true},
		{"MYSQL_PASSWD", true},
		{"OPENAI_API_KEY", true},
		{"APIKEY", true},
		{"AWS_ACCESS_KEY_ID", true},
		{"SSH_PRIVATE_KEY", true},
		{"AUTHORIZATION", true},
		{"COOKIE_JAR", true},
		{"GOOGLE_CREDENTIALS", true},
		{"SESSION_ID", true},
		{"PATH", false},
		{"LOG_LEVEL", false},
		{"API_BASE_URL", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := sensitiveEnvironmentKey(tt.key); got != tt.want {
			t.Errorf("sensitiveEnvironmentKey(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

func TestPublicMCPServerMasksCredentials(t *testing.T) {
	tests := []struct {
		name        string
		headers     map[string]string
		environment map[string]string
		wantHeaders map[string]string
		wantEnv     map[string]string
	}{
		{
			name:        "nil maps stay nil",
			headers:     nil,
			environment: nil,
			wantHeaders: nil,
			wantEnv:     nil,
		},
		{
			name:        "empty maps stay empty",
			headers:     map[string]string{},
			environment: map[string]string{},
			wantHeaders: map[string]string{},
			wantEnv:     map[string]string{},
		},
		{
			name:        "every literal header is masked",
			headers:     map[string]string{"Authorization": "Bearer abc", "X-Trace": "on"},
			wantHeaders: map[string]string{"Authorization": MaskedSecretValue, "X-Trace": MaskedSecretValue},
		},
		{
			name:        "only credential environment keys are masked",
			environment: map[string]string{"API_TOKEN": "t-1", "LOG_LEVEL": "debug"},
			wantEnv:     map[string]string{"API_TOKEN": MaskedSecretValue, "LOG_LEVEL": "debug"},
		},
		{
			name:        "secret references are shown as-is",
			headers:     map[string]string{"Authorization": "Bearer ${secret:GH_TOKEN}"},
			environment: map[string]string{"API_TOKEN": "${secret:API_TOKEN}"},
			wantHeaders: map[string]string{"Authorization": "Bearer ${secret:GH_TOKEN}"},
			wantEnv:     map[string]string{"API_TOKEN": "${secret:API_TOKEN}"},
		},
		{
			name:        "empty values are not turned into masks",
			headers:     map[string]string{"Authorization": ""},
			environment: map[string]string{"API_TOKEN": ""},
			wantHeaders: map[string]string{"Authorization": ""},
			wantEnv:     map[string]string{"API_TOKEN": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := MCPServerDefinition{ID: "mcp-1", URL: "https://mcp.example.com", Headers: tt.headers, Environment: tt.environment}
			got := PublicMCPServer(in)
			if !reflect.DeepEqual(got.Headers, tt.wantHeaders) {
				t.Errorf("headers = %#v, want %#v", got.Headers, tt.wantHeaders)
			}
			if !reflect.DeepEqual(got.Environment, tt.wantEnv) {
				t.Errorf("environment = %#v, want %#v", got.Environment, tt.wantEnv)
			}
			if got.ID != in.ID || got.URL != in.URL {
				t.Errorf("non-secret fields changed: %#v", got)
			}
		})
	}
}

func TestPublicMCPServerDoesNotMutateInput(t *testing.T) {
	headers := map[string]string{"Authorization": "Bearer abc"}
	env := map[string]string{"API_TOKEN": "t-1"}
	PublicMCPServer(MCPServerDefinition{Headers: headers, Environment: env})
	if headers["Authorization"] != "Bearer abc" || env["API_TOKEN"] != "t-1" {
		t.Fatalf("stored maps were masked in place: %#v %#v", headers, env)
	}
}

func TestPublicMCPServers(t *testing.T) {
	if got := PublicMCPServers(nil); got == nil || len(got) != 0 {
		t.Fatalf("PublicMCPServers(nil) = %#v, want empty non-nil slice", got)
	}
	got := PublicMCPServers([]MCPServerDefinition{
		{ID: "a", Headers: map[string]string{"X-Key": "k"}},
		{ID: "b", Environment: map[string]string{"SECRET": "s", "MODE": "m"}},
	})
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("servers = %#v", got)
	}
	if got[0].Headers["X-Key"] != MaskedSecretValue {
		t.Errorf("header not masked: %#v", got[0].Headers)
	}
	if got[1].Environment["SECRET"] != MaskedSecretValue || got[1].Environment["MODE"] != "m" {
		t.Errorf("environment = %#v", got[1].Environment)
	}
}

func TestRestoreMaskedMCPSecrets(t *testing.T) {
	tests := []struct {
		name        string
		incoming    MCPServerDefinition
		existing    MCPServerDefinition
		wantHeaders map[string]string
		wantEnv     map[string]string
	}{
		{
			name: "echoed masks get stored values back",
			incoming: MCPServerDefinition{
				Headers:     map[string]string{"Authorization": MaskedSecretValue},
				Environment: map[string]string{"API_TOKEN": MaskedSecretValue, "LOG_LEVEL": "info"},
			},
			existing: MCPServerDefinition{
				Headers:     map[string]string{"Authorization": "Bearer old"},
				Environment: map[string]string{"API_TOKEN": "t-old", "LOG_LEVEL": "debug"},
			},
			wantHeaders: map[string]string{"Authorization": "Bearer old"},
			wantEnv:     map[string]string{"API_TOKEN": "t-old", "LOG_LEVEL": "info"},
		},
		{
			name: "new literal values replace stored ones",
			incoming: MCPServerDefinition{
				Headers: map[string]string{"Authorization": "Bearer new"},
			},
			existing: MCPServerDefinition{
				Headers: map[string]string{"Authorization": "Bearer old"},
			},
			wantHeaders: map[string]string{"Authorization": "Bearer new"},
		},
		{
			name: "mask without stored counterpart is dropped",
			incoming: MCPServerDefinition{
				Headers:     map[string]string{"X-New": MaskedSecretValue, "X-Keep": "v"},
				Environment: map[string]string{"NEW_SECRET": MaskedSecretValue},
			},
			existing:    MCPServerDefinition{},
			wantHeaders: map[string]string{"X-Keep": "v"},
			wantEnv:     map[string]string{},
		},
		{
			name:     "nil incoming maps stay nil",
			incoming: MCPServerDefinition{},
			existing: MCPServerDefinition{
				Headers: map[string]string{"Authorization": "Bearer old"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			incoming := tt.incoming
			RestoreMaskedMCPSecrets(&incoming, tt.existing)
			if !reflect.DeepEqual(incoming.Headers, tt.wantHeaders) {
				t.Errorf("headers = %#v, want %#v", incoming.Headers, tt.wantHeaders)
			}
			if !reflect.DeepEqual(incoming.Environment, tt.wantEnv) {
				t.Errorf("environment = %#v, want %#v", incoming.Environment, tt.wantEnv)
			}
		})
	}
}

func TestPublicThenRestoreRoundTripsStoredSecrets(t *testing.T) {
	stored := MCPServerDefinition{
		Headers:     map[string]string{"Authorization": "Bearer real", "X-Ref": "${secret:REF}"},
		Environment: map[string]string{"API_TOKEN": "t-real", "REGION": "ap-guangzhou"},
	}
	edited := PublicMCPServer(stored)
	RestoreMaskedMCPSecrets(&edited, stored)
	if !reflect.DeepEqual(edited.Headers, stored.Headers) || !reflect.DeepEqual(edited.Environment, stored.Environment) {
		t.Fatalf("round trip = %#v %#v, want %#v %#v", edited.Headers, edited.Environment, stored.Headers, stored.Environment)
	}
}

package runtime

import "testing"

func TestBindTelemetryEnvironmentNilLeavesEnvUntouched(t *testing.T) {
	env := map[string]string{"KEEP": "1"}
	bindTelemetryEnvironment(env, nil)
	if len(env) != 1 || env["KEEP"] != "1" {
		t.Fatalf("nil telemetry changed env: %#v", env)
	}
}

func TestBindTelemetryEnvironmentSetsCollectorAndRunTags(t *testing.T) {
	t.Setenv("AGENTFLOW_OTEL_TRACES_ENDPOINT", "http://collector.test/v1/traces")
	env := map[string]string{}
	bindTelemetryEnvironment(env, &InvocationOptions{RunID: "run-1", NodeID: "node-a"})
	want := map[string]string{
		"AGENTFLOW_RUN_ID":                   "run-1",
		"AGENTFLOW_NODE_ID":                  "node-a",
		"CODEBUDDY_CODE_ENABLE_TELEMETRY":    "1",
		"CLAUDE_CODE_ENABLE_TELEMETRY":       "1",
		"OTEL_TRACES_EXPORTER":               "otlp",
		"OTEL_EXPORTER_OTLP_PROTOCOL":        "http/protobuf",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://collector.test/v1/traces",
		"OTEL_LOG_TOOL_DETAILS":              "1",
		"OTEL_RESOURCE_ATTRIBUTES":           "agentflow.run_id=run-1,agentflow.node_id=node-a,service.name=agentflow-codebuddy",
	}
	if len(env) != len(want) {
		t.Fatalf("env = %#v, want %#v", env, want)
	}
	for key, value := range want {
		if env[key] != value {
			t.Fatalf("%s = %q, want %q", key, env[key], value)
		}
	}
}

func TestBindCommandAppliesTelemetryEnvironment(t *testing.T) {
	t.Setenv("AGENTFLOW_OTEL_TRACES_ENDPOINT", "http://collector.test/v1/traces")
	workspace := t.TempDir()
	plain, err := bindCommand(&localSandbox{workspace: workspace}, workspace, CommandSpec{Argv: []string{"true"}}, RunCredentials{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plain.environment["AGENTFLOW_RUN_ID"]; ok {
		t.Fatalf("command without telemetry got telemetry env: %#v", plain.environment)
	}
	traced, err := bindCommand(&localSandbox{workspace: workspace}, workspace, CommandSpec{Argv: []string{"true"}, Telemetry: &InvocationOptions{RunID: "run-2", NodeID: "node-b"}}, RunCredentials{})
	if err != nil {
		t.Fatal(err)
	}
	if traced.environment["AGENTFLOW_RUN_ID"] != "run-2" || traced.environment["AGENTFLOW_NODE_ID"] != "node-b" || traced.environment["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] != "http://collector.test/v1/traces" {
		t.Fatalf("telemetry env not applied: %#v", traced.environment)
	}
}

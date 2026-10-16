package config

import "testing"

func clearEndpointEnv(t *testing.T) {
	for _, key := range []string{"AGENTFLOW_HTTP_PORT", "AGENTFLOW_API_URL", "AGENTFLOW_PLATFORM_MCP_URL", "AGENTFLOW_PLATFORM_MCP_TOKEN", "AGENTFLOW_CONTROL_URL", "AGENTFLOW_CONTROL_TOKEN", "AGENTFLOW_WORKER_URL", "AGENTFLOW_AGENT_TOOL_GATEWAY_URL"} {
		t.Setenv(key, "")
	}
}

func TestLoopbackURL(t *testing.T) {
	cases := map[string]string{":8081": "http://127.0.0.1:8081", "0.0.0.0:8081": "http://127.0.0.1:8081", "10.0.0.2:9000": "http://10.0.0.2:9000", "": "", "bogus": ""}
	for in, want := range cases {
		if got := LoopbackURL(in); got != want {
			t.Fatalf("LoopbackURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadEndpointsAPIFollowsListenAddr(t *testing.T) {
	clearEndpointEnv(t)
	ep, err := LoadEndpoints("api", "0.0.0.0:18090")
	if err != nil {
		t.Fatal(err)
	}
	// -addr 覆盖端口时，助手工具网关和 OTLP 采集地址要跟着走，不能再假定 8080。
	want := "http://127.0.0.1:18090"
	if ep.ListenAddr != "0.0.0.0:18090" || ep.APIBase != want || ep.PlatformBase != want || ep.AgentToolGateway != want || ep.CollectorBase != want {
		t.Fatalf("endpoints = %#v", ep)
	}
	t.Setenv("AGENTFLOW_HTTP_PORT", "9090")
	if ep, _ = LoadEndpoints("all", ""); ep.ListenAddr != ":9090" || ep.APIBase != "http://127.0.0.1:9090" {
		t.Fatalf("port from env: %#v", ep)
	}
	t.Setenv("AGENTFLOW_HTTP_PORT", "nope")
	if _, err := LoadEndpoints("all", ""); err == nil {
		t.Fatal("expected invalid AGENTFLOW_HTTP_PORT error")
	}
}

func TestLoadEndpointsWorker(t *testing.T) {
	clearEndpointEnv(t)
	ep, err := LoadEndpoints("worker", "")
	if err != nil {
		t.Fatal(err)
	}
	if ep.ListenAddr != ":8081" || ep.ControlURL != "http://127.0.0.1:8081" || ep.PlatformBase != "" || ep.CollectorBase != "http://127.0.0.1:8080" {
		t.Fatalf("worker defaults = %#v", ep)
	}
	t.Setenv("AGENTFLOW_CONTROL_URL", "http://10.0.0.2:8081/")
	t.Setenv("AGENTFLOW_PLATFORM_MCP_URL", "http://10.0.0.2:8080/")
	t.Setenv("AGENTFLOW_PLATFORM_MCP_TOKEN", " tok ")
	ep, _ = LoadEndpoints("worker", ":8099")
	if ep.ListenAddr != ":8099" || ep.ControlURL != "http://10.0.0.2:8081" || ep.PlatformBase != "http://10.0.0.2:8080" || ep.PlatformMCPToken != "tok" || ep.CollectorBase != "http://10.0.0.2:8080" {
		t.Fatalf("worker configured = %#v", ep)
	}
}

func TestLoadEndpointsAPIURLFallback(t *testing.T) {
	clearEndpointEnv(t)
	t.Setenv("AGENTFLOW_API_URL", "http://api.internal:8080/")
	ep, _ := LoadEndpoints("worker", "")
	if ep.PlatformBase != "http://api.internal:8080" || ep.APIBase != "http://api.internal:8080" {
		t.Fatalf("API URL fallback = %#v", ep)
	}
}

func TestCurrentDefaultsAndOverride(t *testing.T) {
	clearEndpointEnv(t)
	currentMu.Lock()
	saved := current
	current = nil
	currentMu.Unlock()
	t.Cleanup(func() { currentMu.Lock(); current = saved; currentMu.Unlock() })
	if got := Current().APIBase; got != "http://127.0.0.1:8080" {
		t.Fatalf("default APIBase = %q", got)
	}
	SetCurrent(Endpoints{APIBase: "http://x"})
	if got := Current().APIBase; got != "http://x" {
		t.Fatalf("published APIBase = %q", got)
	}
}

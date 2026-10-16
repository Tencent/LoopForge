// Package config resolves process-wide settings once at startup so the rest
// of the code never re-derives them from the environment.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Endpoints is every address this process listens on or hands to others.
// Keeping them together makes the api/worker/all differences explicit: a
// worker serves no API plane, so it only knows platform addresses that the
// deployment configures.
type Endpoints struct {
	Role string
	// ListenAddr is this process's own HTTP listener.
	ListenAddr string
	// APIBase reaches the API plane from the same host (loopback by default).
	APIBase string
	// PlatformBase reaches the API plane from agent sandboxes. Empty when the
	// deployment did not configure one for a worker; see AGENTFLOW_PLATFORM_MCP_URL.
	PlatformBase     string
	PlatformMCPToken string
	// ControlURL is where the API plane reaches this worker to interrupt runs.
	ControlURL        string
	ControlToken      string
	WorkerFallbackURL string
	// AgentToolGateway is the base the assistant's host-side CLI uses for
	// native agent tools.
	AgentToolGateway string
	// CollectorBase receives OTLP telemetry from agent CLIs.
	CollectorBase string
}

const (
	defaultAPIPort    = "8080"
	defaultWorkerAddr = ":8081"
)

// LoadEndpoints resolves Endpoints for role from the -addr flag and env.
func LoadEndpoints(role, addrFlag string) (Endpoints, error) {
	ep := Endpoints{
		Role:              role,
		PlatformMCPToken:  env("AGENTFLOW_PLATFORM_MCP_TOKEN"),
		ControlToken:      env("AGENTFLOW_CONTROL_TOKEN"),
		WorkerFallbackURL: trimURL(env("AGENTFLOW_WORKER_URL")),
	}
	httpPort := env("AGENTFLOW_HTTP_PORT")
	if httpPort == "" {
		httpPort = defaultAPIPort
	}
	if n, err := strconv.Atoi(httpPort); err != nil || n < 1 || n > 65535 {
		return Endpoints{}, fmt.Errorf("invalid AGENTFLOW_HTTP_PORT %q", httpPort)
	}
	configuredAPI := trimURL(env("AGENTFLOW_API_URL"))
	if role == "worker" {
		ep.ListenAddr = firstNonEmpty(addrFlag, defaultWorkerAddr)
		ep.ControlURL = firstNonEmpty(trimURL(env("AGENTFLOW_CONTROL_URL")), LoopbackURL(ep.ListenAddr))
		ep.APIBase = firstNonEmpty(configuredAPI, "http://127.0.0.1:"+httpPort)
		ep.PlatformBase = firstNonEmpty(trimURL(env("AGENTFLOW_PLATFORM_MCP_URL")), configuredAPI)
	} else {
		ep.ListenAddr = firstNonEmpty(addrFlag, ":"+httpPort)
		ep.APIBase = firstNonEmpty(configuredAPI, LoopbackURL(ep.ListenAddr))
		ep.PlatformBase = firstNonEmpty(trimURL(env("AGENTFLOW_PLATFORM_MCP_URL")), ep.APIBase)
	}
	ep.AgentToolGateway = firstNonEmpty(trimURL(env("AGENTFLOW_AGENT_TOOL_GATEWAY_URL")), ep.APIBase)
	ep.CollectorBase = firstNonEmpty(ep.PlatformBase, ep.APIBase)
	return ep, nil
}

// LoopbackURL turns a listen address into a same-host http URL, mapping
// wildcard hosts to 127.0.0.1. It returns "" for unparseable addresses.
func LoopbackURL(listenAddr string) string {
	addr := strings.TrimSpace(listenAddr)
	if addr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, ":") {
			return "http://127.0.0.1" + addr
		}
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

var (
	currentMu sync.RWMutex
	current   *Endpoints
)

// SetCurrent publishes the endpoints main resolved for package-level helpers
// that cannot take them as a parameter.
func SetCurrent(ep Endpoints) {
	currentMu.Lock()
	defer currentMu.Unlock()
	current = &ep
}

// Current returns the published endpoints, or the "all"-role defaults from the
// environment when main never published any (tests, helper binaries).
func Current() Endpoints {
	currentMu.RLock()
	ep := current
	currentMu.RUnlock()
	if ep != nil {
		return *ep
	}
	fallback, err := LoadEndpoints("all", "")
	if err != nil {
		base := "http://127.0.0.1:" + defaultAPIPort
		return Endpoints{Role: "all", ListenAddr: ":" + defaultAPIPort, APIBase: base, PlatformBase: base, AgentToolGateway: base, CollectorBase: base}
	}
	return fallback
}

func env(key string) string { return strings.TrimSpace(os.Getenv(key)) }

func trimURL(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

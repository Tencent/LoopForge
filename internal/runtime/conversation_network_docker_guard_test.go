package runtime

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// cndInstallFakeDocker puts a scripted docker on PATH for the network lookup.
// `docker inspect` answers per container id from $CND_INSPECT_<id> (or fails
// with $CND_INSPECT_FAIL_<id>); this host's own container id is keyed SELF.
// `docker ps -q` prints $CND_PS or fails when $CND_PS_FAIL is set. Every
// invocation is appended to the returned log file.
func cndInstallFakeDocker(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "docker.log")
	script := `#!/bin/sh
echo "$*" >> "$CND_LOG"
case "$1" in
inspect)
  shift 3
  for id in "$@"; do
    key=$id; [ "$id" = "$CND_HOSTNAME" ] && key=SELF
    eval "fail=\${CND_INSPECT_FAIL_$key-}"
    if [ -n "$fail" ]; then echo "Error: No such object: $id" >&2; exit 1; fi
  done
  for id in "$@"; do
    key=$id; [ "$id" = "$CND_HOSTNAME" ] && key=SELF
    eval "out=\${CND_INSPECT_$key-}"
    [ -n "$out" ] && printf '%s\n' "$out"
  done
  exit 0 ;;
ps)
  [ -n "${CND_PS_FAIL-}" ] && exit 1
  printf '%s' "${CND_PS-}"
  exit 0 ;;
esac
exit 2
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CND_LOG", log)
	return log
}

func cndHostname(t *testing.T) string {
	t.Helper()
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		t.Fatalf("hostname: %q %v", hostname, err)
	}
	t.Setenv("CND_HOSTNAME", hostname)
	return hostname
}

func cndCalls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

const cndInspectPrefix = "inspect --format {{json .NetworkSettings.Networks}} "

func TestFindDaemonConversationNetworkUsesOwnContainerFirst(t *testing.T) {
	log := cndInstallFakeDocker(t)
	hostname := cndHostname(t)
	t.Setenv("CND_INSPECT_SELF", `{"bridge":{"IPAddress":"172.17.0.9"},"lf-net":{"IPAddress":"10.9.0.4"}}`)
	t.Setenv("CND_PS", "other\n")
	t.Setenv("CND_INSPECT_other", `{"bridge":{"IPAddress":"10.9.0.4"}}`)

	ip, network := findDaemonConversationNetwork(context.Background(), map[string]bool{"10.9.0.4": true}, "")
	if ip != "10.9.0.4" || network != "lf-net" {
		t.Fatalf("own container network = %q %q", ip, network)
	}
	calls := cndCalls(t, log)
	if len(calls) != 1 || calls[0] != cndInspectPrefix+hostname {
		t.Fatalf("docker should only inspect this container by hostname, calls = %q", calls)
	}
}

func TestFindDaemonConversationNetworkFallsBackToRunningContainers(t *testing.T) {
	for _, c := range []struct {
		name string
		own  func(t *testing.T)
	}{
		{"hostname is not a container", func(t *testing.T) { t.Setenv("CND_INSPECT_FAIL_SELF", "1") }},
		{"own container lacks daemon address", func(t *testing.T) {
			t.Setenv("CND_INSPECT_SELF", `{"bridge":{"IPAddress":"172.17.0.9"}}`)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			log := cndInstallFakeDocker(t)
			hostname := cndHostname(t)
			c.own(t)
			t.Setenv("CND_PS", "c1\nc2\n")
			t.Setenv("CND_INSPECT_c1", `{"bridge":{"IPAddress":"172.17.0.2"}}`)
			t.Setenv("CND_INSPECT_c2", `{"compose-app":{"IPAddress":"172.20.0.5"}}`)

			ip, network := findDaemonConversationNetwork(context.Background(), map[string]bool{"172.20.0.5": true}, "")
			if ip != "172.20.0.5" || network != "compose-app" {
				t.Fatalf("fallback network = %q %q", ip, network)
			}
			want := []string{cndInspectPrefix + hostname, "ps -q", cndInspectPrefix + "c1 c2"}
			if calls := cndCalls(t, log); strings.Join(calls, "|") != strings.Join(want, "|") {
				t.Fatalf("docker calls = %q, want %q", calls, want)
			}
		})
	}
}

func TestFindDaemonConversationNetworkHonoursRequestedNetwork(t *testing.T) {
	cndInstallFakeDocker(t)
	cndHostname(t)
	// "alpha" sorts first, so ignoring the requested network would pick it.
	t.Setenv("CND_INSPECT_SELF", `{"alpha":{"IPAddress":"10.1.0.2"},"beta":{"IPAddress":"10.2.0.2"}}`)
	addresses := map[string]bool{"10.1.0.2": true, "10.2.0.2": true}

	if ip, network := findDaemonConversationNetwork(context.Background(), addresses, "beta"); ip != "10.2.0.2" || network != "beta" {
		t.Fatalf("requested beta = %q %q", ip, network)
	}
	if ip, network := findDaemonConversationNetwork(context.Background(), addresses, "gamma"); ip != "" || network != "" {
		t.Fatalf("requested network without daemon address = %q %q", ip, network)
	}
}

func TestFindDaemonConversationNetworkStopsWhenNothingRuns(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
	}{
		{"ps fails", map[string]string{"CND_PS_FAIL": "1"}},
		{"no running containers", map[string]string{"CND_PS": " \n"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			log := cndInstallFakeDocker(t)
			hostname := cndHostname(t)
			t.Setenv("CND_INSPECT_FAIL_SELF", "1")
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			if ip, network := findDaemonConversationNetwork(context.Background(), map[string]bool{"10.0.0.1": true}, ""); ip != "" || network != "" {
				t.Fatalf("network = %q %q", ip, network)
			}
			want := []string{cndInspectPrefix + hostname, "ps -q"}
			if calls := cndCalls(t, log); strings.Join(calls, "|") != strings.Join(want, "|") {
				t.Fatalf("docker calls = %q, want %q", calls, want)
			}
		})
	}
}

func TestFindDaemonConversationNetworkWithoutDocker(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if ip, network := findDaemonConversationNetwork(context.Background(), map[string]bool{"10.0.0.1": true}, ""); ip != "" || network != "" {
		t.Fatalf("network without docker = %q %q", ip, network)
	}
}

// cndDaemonAddress returns one address daemonIPv4Addresses really reports so
// conversationDockerAccess can be driven end to end through the fake docker.
func cndDaemonAddress(t *testing.T) string {
	t.Helper()
	addresses, err := daemonIPv4Addresses()
	if err != nil {
		t.Fatal(err)
	}
	list := make([]string, 0, len(addresses))
	for address := range addresses {
		list = append(list, address)
	}
	if len(list) == 0 {
		t.Fatal("test host has no global unicast IPv4 interface address")
	}
	sort.Strings(list)
	return list[0]
}

func cndInContainer(t *testing.T, host, network string) {
	t.Helper()
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
	t.Setenv("AGENTFLOW_CONVERSATION_RELAY_HOST", host)
	t.Setenv("AGENTFLOW_CONVERSATION_DOCKER_NETWORK", network)
}

func TestConversationDockerAccessDiscoversDaemonNetworkInContainer(t *testing.T) {
	address := cndDaemonAddress(t)
	cndInstallFakeDocker(t)
	cndHostname(t)
	t.Setenv("CND_INSPECT_SELF", `{"alpha":{"IPAddress":"`+address+`"},"beta":{"IPAddress":"`+address+`"}}`)

	cndInContainer(t, "", "")
	if host, network, err := conversationDockerAccess(context.Background()); err != nil || host != address || network != "alpha" {
		t.Fatalf("discovered = %q %q %v, want %q alpha", host, network, err, address)
	}

	cndInContainer(t, " relay.internal ", "")
	if host, network, err := conversationDockerAccess(context.Background()); err != nil || host != "relay.internal" || network != "alpha" {
		t.Fatalf("relay host override = %q %q %v", host, network, err)
	}

	cndInContainer(t, "", " beta ")
	if host, network, err := conversationDockerAccess(context.Background()); err != nil || host != address || network != "beta" {
		t.Fatalf("network override = %q %q %v", host, network, err)
	}
}

func TestConversationDockerAccessReportsUndiscoverableNetwork(t *testing.T) {
	address := cndDaemonAddress(t)
	cndInstallFakeDocker(t)
	cndHostname(t)
	t.Setenv("CND_PS", "")

	for _, c := range []struct{ host, network, self string }{
		// The daemon is on alpha, but the requested network is not.
		{"", "missing", `{"alpha":{"IPAddress":"` + address + `"}}`},
		// A relay host override alone still needs a discovered network.
		{"relay.internal", "", `{"alpha":{"IPAddress":"198.51.100.7"}}`},
	} {
		t.Setenv("CND_INSPECT_SELF", c.self)
		cndInContainer(t, c.host, c.network)
		host, network, err := conversationDockerAccess(context.Background())
		if err == nil || host != "" || network != "" {
			t.Fatalf("host %q network %q = %q %q %v, want error", c.host, c.network, host, network, err)
		}
		if !strings.Contains(err.Error(), "AGENTFLOW_CONVERSATION_RELAY_HOST") || !strings.Contains(err.Error(), "AGENTFLOW_CONVERSATION_DOCKER_NETWORK") {
			t.Fatalf("error does not name the overrides: %v", err)
		}
	}
}

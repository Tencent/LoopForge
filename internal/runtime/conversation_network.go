package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Daemons using docker.sock listen in their own network namespace. Join
// native workspaces to that same network and advertise its reachable IP.
func conversationDockerAccess(ctx context.Context) (string, string, error) {
	host := strings.TrimSpace(os.Getenv("AGENTFLOW_CONVERSATION_RELAY_HOST"))
	network := strings.TrimSpace(os.Getenv("AGENTFLOW_CONVERSATION_DOCKER_NETWORK"))
	if strings.ContainsAny(host, "/\\\r\n\x00") || strings.ContainsAny(network, "\r\n\x00") {
		return "", "", fmt.Errorf("invalid conversation network configuration")
	}
	if host != "" && network != "" {
		return host, network, nil
	}
	_, dockerErr := os.Stat("/.dockerenv")
	_, podmanErr := os.Stat("/run/.containerenv")
	if dockerErr != nil && podmanErr != nil && os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		if host == "" {
			host = "host.docker.internal"
		}
		return host, network, nil
	}
	addresses := map[string]bool{}
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", "", err
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		items, _ := iface.Addrs()
		for _, item := range items {
			ip, _, err := net.ParseCIDR(item.String())
			if err == nil && ip.To4() != nil && ip.IsGlobalUnicast() {
				addresses[ip.String()] = true
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	inspect := func(ids []string) (string, string) {
		args := append([]string{"inspect", "--format", "{{json .NetworkSettings.Networks}}"}, ids...)
		data, err := exec.CommandContext(ctx, "docker", args...).Output()
		if err != nil {
			return "", ""
		}
		return matchConversationNetwork(string(data), addresses, network)
	}
	hostname, _ := os.Hostname()
	ip, selected := inspect([]string{hostname})
	if ip == "" {
		ids, err := exec.CommandContext(ctx, "docker", "ps", "-q").Output()
		if err == nil && len(strings.Fields(string(ids))) > 0 {
			ip, selected = inspect(strings.Fields(string(ids)))
		}
	}
	if ip == "" {
		return "", "", fmt.Errorf("无法确定工作区可访问的容器网络，请设置 AGENTFLOW_CONVERSATION_RELAY_HOST 和 AGENTFLOW_CONVERSATION_DOCKER_NETWORK")
	}
	if host == "" {
		host = ip
	}
	return host, selected, nil
}

func matchConversationNetwork(data string, addresses map[string]bool, requested string) (string, string) {
	for _, line := range strings.Split(data, "\n") {
		var networks map[string]struct{ IPAddress string }
		if json.Unmarshal([]byte(line), &networks) != nil {
			continue
		}
		names := make([]string, 0, len(networks))
		for name := range networks {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if (requested == "" || requested == name) && addresses[networks[name].IPAddress] {
				return networks[name].IPAddress, name
			}
		}
	}
	return "", ""
}

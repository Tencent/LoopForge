package runtime

import "testing"

func TestConversationNetworkMatchesDaemonInterface(t *testing.T) {
	data := `{"unrelated":{"IPAddress":"172.17.0.2"}}
{"compose-app":{"IPAddress":"172.20.0.5"},"bridge":{"IPAddress":"172.17.0.3"}}`
	addresses := map[string]bool{"172.20.0.5": true}
	if host, network := matchConversationNetwork(data, addresses, ""); host != "172.20.0.5" || network != "compose-app" {
		t.Fatalf("workspace would not join the daemon network: %s %s", host, network)
	}
	if host, _ := matchConversationNetwork(data, addresses, "unrelated"); host != "" {
		t.Fatal("selected a network without the daemon's interface")
	}
}

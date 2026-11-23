package domain

import "testing"

func TestActiveConnectionsInheritTopLevelProvider(t *testing.T) {
	settings := KnowledgeSettings{
		Enabled:  true,
		Provider: "weknora",
		Connections: []KnowledgeConnection{
			{ID: "inherit", Name: "继承", URL: "http://kb/mcp", Token: "tok", Enabled: true},
			{ID: "own", Name: "自有", Provider: "lightcloud", URL: "http://lc/mcp", Token: "tok", Enabled: true},
			{ID: "off", Name: "停用", URL: "http://kb2/mcp", Token: "tok", Enabled: false},
		},
	}
	conns := settings.ActiveConnections()
	if len(conns) != 2 {
		t.Fatalf("ActiveConnections() returned %d connections, want 2", len(conns))
	}
	if conns[0].ID != "inherit" || conns[1].ID != "own" {
		t.Fatalf("unexpected connection order: %q, %q", conns[0].ID, conns[1].ID)
	}
	// 空 Provider 必须继承顶层 EffectiveProvider，避免能力指纹与运行时分流误判。
	if conns[0].Provider != "weknora" {
		t.Fatalf("inherited provider = %q, want weknora", conns[0].Provider)
	}
	if conns[1].Provider != "lightcloud" {
		t.Fatalf("explicit provider was overwritten: %q", conns[1].Provider)
	}
	// 返回的是克隆，修改结果不得回写原始设置。
	conns[0].Provider = "mutated"
	if settings.Connections[0].Provider != "" {
		t.Fatalf("ActiveConnections() leaked a mutable reference: %q", settings.Connections[0].Provider)
	}
}

func TestActiveConnectionsEmptyProviderWithoutTopLevelProvider(t *testing.T) {
	settings := KnowledgeSettings{Enabled: true, Connections: []KnowledgeConnection{{ID: "legacy", URL: "http://kb/mcp", Token: "tok", Enabled: true}}}
	conns := settings.ActiveConnections()
	if len(conns) != 1 {
		t.Fatalf("ActiveConnections() returned %d connections, want 1", len(conns))
	}
	if conns[0].Provider != settings.EffectiveProvider() {
		t.Fatalf("provider = %q, want fallback %q", conns[0].Provider, settings.EffectiveProvider())
	}
}

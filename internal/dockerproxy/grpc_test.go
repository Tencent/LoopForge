package dockerproxy

import (
	"context"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func solveMessage(frontendAttrs map[string]string, entitlements []string, exporters ...map[string]string) []byte {
	entry := func(k, v string) []byte {
		b := protowire.AppendTag(nil, mapKey, protowire.BytesType)
		b = protowire.AppendString(b, k)
		b = protowire.AppendTag(b, mapValue, protowire.BytesType)
		return protowire.AppendString(b, v)
	}
	var msg []byte
	msg = protowire.AppendTag(msg, 1, protowire.BytesType)
	msg = protowire.AppendString(msg, "ref-1")
	msg = protowire.AppendTag(msg, 6, protowire.BytesType)
	msg = protowire.AppendString(msg, "dockerfile.v0")
	for k, v := range frontendAttrs {
		msg = protowire.AppendTag(msg, solveFrontendAttrs, protowire.BytesType)
		msg = protowire.AppendBytes(msg, entry(k, v))
	}
	for _, e := range entitlements {
		msg = protowire.AppendTag(msg, solveEntitlements, protowire.BytesType)
		msg = protowire.AppendString(msg, e)
	}
	for _, attrs := range exporters {
		var ex []byte
		ex = protowire.AppendTag(ex, exporterType, protowire.BytesType)
		ex = protowire.AppendString(ex, attrs["type"])
		for k, v := range attrs {
			if k == "type" {
				continue
			}
			ex = protowire.AppendTag(ex, exporterAttrs, protowire.BytesType)
			ex = protowire.AppendBytes(ex, entry(k, v))
		}
		msg = protowire.AppendTag(msg, solveExporters, protowire.BytesType)
		msg = protowire.AppendBytes(msg, ex)
	}
	return msg
}

func TestSolveChecksMatchClassicBuild(t *testing.T) {
	h := start(t)
	p, err := Start(Config{Upstream: h.upstream, Socket: filepath.Join(t.TempDir(), "s"), Workspace: "ws1"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ok := solveMessage(map[string]string{"label:loopforge.workspace": "ws2", "build-arg:X": "1"}, nil,
		map[string]string{"type": "moby", "name": "myapp:dev,new:1"})
	out, err := p.checkSolve(context.Background(), ok)
	if err != nil {
		t.Fatalf("ordinary solve refused: %v", err)
	}
	attrs := map[string]string{}
	for b := out; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		b = b[n:]
		if typ != protowire.BytesType {
			t.Fatalf("unexpected field type %v", typ)
		}
		v, m := protowire.ConsumeBytes(b)
		b = b[m:]
		if num == solveFrontendAttrs {
			if err := parseMapEntry(v, attrs); err != nil {
				t.Fatal(err)
			}
		}
	}
	if attrs["label:"+Label] != "ws1" || attrs["build-arg:X"] != "1" {
		t.Fatalf("workspace label must win over the request's: %v", attrs)
	}
	for name, msg := range map[string][]byte{
		"entitlement":    solveMessage(nil, []string{"network.host"}),
		"insecure":       solveMessage(nil, []string{"security.insecure"}),
		"host network":   solveMessage(map[string]string{"force-network-mode": "host"}, nil),
		"cgroup parent":  solveMessage(map[string]string{"cgroup-parent": "/"}, nil),
		"foreign tag":    solveMessage(nil, nil, map[string]string{"type": "moby", "name": "new:1,team/other:1"}),
		"public tag":     solveMessage(nil, nil, map[string]string{"type": "image", "name": "alpine:3"}),
		"docker tarball": solveMessage(nil, nil, map[string]string{"type": "docker", "name": "alpine:3"}),
	} {
		if _, err := p.checkSolve(context.Background(), msg); err == nil {
			t.Errorf("%s should be refused", name)
		} else if _, isForbidden := err.(forbidden); !isForbidden {
			t.Errorf("%s: want a refusal, got %v", name, err)
		}
	}
	if _, err := p.checkSolve(context.Background(), solveMessage(nil, nil, map[string]string{"type": "local", "dest": "out"})); err != nil {
		t.Errorf("local exporter writes to the client, not the daemon: %v", err)
	}
	for _, method := range []string{"/moby.buildkit.v1.Control/Prune", "/moby.buildkit.v1.Control/UpdateBuildHistory", "/moby.buildkit.v1.Control/DiskUsage", "/containerd.services.content.v1.Content/Delete"} {
		if grpcMethodAllowed(method) {
			t.Errorf("%s must be refused", method)
		}
	}
	if !grpcMethodAllowed(grpcSolve) || !grpcMethodAllowed("/moby.buildkit.v1.frontend.LLBBridge/Solve") {
		t.Error("build calls must be relayed")
	}
}

func TestBuildHistoryOnlyByRef(t *testing.T) {
	all := protowire.AppendTag(nil, 1, protowire.VarintType)
	all = protowire.AppendVarint(all, 1) // ActiveOnly, no ref
	if _, err := checkHistory(context.Background(), all); err == nil {
		t.Error("listing every build on the daemon must be refused")
	}
	if _, err := checkHistory(context.Background(), nil); err == nil {
		t.Error("an empty request lists every build and must be refused")
	}
	own := protowire.AppendTag(all, historyRef, protowire.BytesType)
	own = protowire.AppendString(own, "k3qz0ref")
	if out, err := checkHistory(context.Background(), own); err != nil || string(out) != string(own) {
		t.Errorf("following one's own build by ref must pass unchanged: %v", err)
	}
}

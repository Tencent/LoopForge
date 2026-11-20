package runner

import (
	"context"
	"testing"

	"agentflow/internal/domain"
)

func TestRuntimeProviderSpecsDefaultsOnly(t *testing.T) {
	specs := runtimeProviderSpecs(nil)
	if len(specs) != len(defaultProviders) {
		t.Fatalf("len(specs) = %d, want %d", len(specs), len(defaultProviders))
	}
	for i, spec := range defaultProviders {
		if specs[i].Kind != spec.Kind || specs[i].Binary != spec.Binary {
			t.Fatalf("specs[%d] = %+v, want %+v", i, specs[i], spec)
		}
	}
}

func TestRuntimeProviderSpecsAppendsSortedExtraKinds(t *testing.T) {
	specs := runtimeProviderSpecs(map[string]string{
		"zeta":        "/opt/zeta",
		"alpha":       "/opt/alpha",
		"claude-code": "/opt/claude",
	})
	if len(specs) != len(defaultProviders)+2 {
		t.Fatalf("len(specs) = %d, want %d", len(specs), len(defaultProviders)+2)
	}
	for i, spec := range defaultProviders {
		// An extra binary for a default kind is an override applied later,
		// not a replacement of the default spec.
		if specs[i].Kind != spec.Kind || specs[i].Binary != spec.Binary {
			t.Fatalf("specs[%d] = %+v, want %+v", i, specs[i], spec)
		}
	}
	extra := specs[len(defaultProviders):]
	want := []providerSpec{{Kind: "alpha", Binary: "/opt/alpha"}, {Kind: "zeta", Binary: "/opt/zeta"}}
	for i := range want {
		if extra[i].Kind != want[i].Kind || extra[i].Binary != want[i].Binary {
			t.Fatalf("extra[%d] = %+v, want %+v", i, extra[i], want[i])
		}
	}
}

func TestBuiltinRuntimes(t *testing.T) {
	got := builtinRuntimes()
	if len(got) != 2 {
		t.Fatalf("len(builtinRuntimes()) = %d, want 2", len(got))
	}
	want := []struct {
		provider   string
		capability string
	}{{mockProviderKind, "non_interactive"}, {terminalProviderKind, "terminal"}}
	for i, w := range want {
		rt := got[i]
		if rt.Provider != w.provider || rt.BinaryPath != "(builtin)" || rt.Status != domain.RuntimeStatusOnline {
			t.Fatalf("builtinRuntimes()[%d] = %+v", i, rt)
		}
		if len(rt.Capabilities) != 1 || rt.Capabilities[0] != w.capability {
			t.Fatalf("builtinRuntimes()[%d].Capabilities = %v, want [%s]", i, rt.Capabilities, w.capability)
		}
	}
}

func TestDetectRuntimesStartsWithBuiltinRuntimes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	got := detectRuntimes(context.Background(), nil, nil)
	if len(got) < 2 {
		t.Fatalf("detectRuntimes = %+v, want builtin runtimes first", got)
	}
	if got[0].Provider != mockProviderKind || got[1].Provider != terminalProviderKind {
		t.Fatalf("detectRuntimes providers = %s, %s", got[0].Provider, got[1].Provider)
	}
}

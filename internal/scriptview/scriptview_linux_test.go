//go:build linux

package scriptview

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestContainerIdentityUsesEffectiveIDsAndPreservesGroups(t *testing.T) {
	name := filepath.Join(t.TempDir(), "status")
	if err := os.WriteFile(name, []byte("Name:\ttini\nUid:\t10 1000 1000 1000\nGid:\t20 2000 2000 2000\nGroups:\t20 2000 3000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := readProcessIdentity(name)
	if err != nil {
		t.Fatal(err)
	}
	if identity.uid != 1000 || identity.gid != 2000 || !reflect.DeepEqual(identity.groups, []int{20, 2000, 3000}) {
		t.Fatalf("container identity changed: %+v", identity)
	}
}

func TestContainerIdentityRejectsMissingOrInvalidIDs(t *testing.T) {
	for _, status := range []string{
		"Uid:\t0 0 0 0\nGid:\t0 0 0 0\n",
		"Uid:\t0 invalid 0 0\nGid:\t0 0 0 0\nGroups:\t0\n",
		"Uid:\t0 0 0 0\nGid:\t0 -1 0 0\nGroups:\t0\n",
		"Uid:\t0 0 0 0\nGid:\t0 0 0 0\nGroups:\tinvalid\n",
	} {
		name := filepath.Join(t.TempDir(), "status")
		if err := os.WriteFile(name, []byte(status), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readProcessIdentity(name); err == nil {
			t.Errorf("unsafe container identity accepted: %q", status)
		}
	}
}

func TestMountPathDecodesProcEscapes(t *testing.T) {
	if got := decodeMountPath(`/workspace/project\040name/CLAUDE.md`); got != "/workspace/project name/CLAUDE.md" {
		t.Fatalf("escaped mask mount path=%q", got)
	}
	if got := decodeMountPath(`/workspace/slash\134040/CLAUDE.md`); got != `/workspace/slash\040/CLAUDE.md` {
		t.Fatalf("mount path was decoded more than once: %q", got)
	}
}

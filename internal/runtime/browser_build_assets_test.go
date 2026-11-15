package runtime

import (
	"bytes"
	"testing"
)

func TestBrowserDockerEntrypointUsesLinuxLineEndings(t *testing.T) {
	for _, name := range []string{"entrypoint.sh", "Dockerfile", "cdp-relay.mjs"} {
		original, err := browserAssets.ReadFile("browser_assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		built, err := browserBuildAsset(name)
		if err != nil {
			t.Fatal(err)
		}
		if name == "entrypoint.sh" {
			if !bytes.HasPrefix(built, []byte("#!/bin/sh\n")) || bytes.Contains(built, []byte("\r\n")) {
				t.Fatal("Linux browser entrypoint retains Windows line endings")
			}
		} else if !bytes.Equal(original, built) {
			t.Fatalf("normalization changed %s", name)
		}
	}
}

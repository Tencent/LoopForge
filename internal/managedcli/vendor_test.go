package managedcli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func cursorFixtureVersion(version string) string {
	var major, minor, patch int
	fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch)
	return fmt.Sprintf("%04d.%02d.%02d-aabbccd", 2024+major, 1+minor, 1+patch)
}

func (f *updaterFixture) serveVendorRelease(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	const agyArtifactPath = agyStoragePrefix + "1.0.0-test/linux-x64/cli_linux_x64.tar.gz"
	switch r.Host {
	case "cursor.com":
		_, _ = fmt.Fprintf(w, "DOWNLOAD_URL=\"https://downloads.cursor.com/lab/%s/${OS}/${ARCH}/agent-cli-package.tar.gz\"\n", f.versions["cursor"])
	case "antigravity-cli-auto-updater-974169037036.us-central1.run.app":
		sum := sha512.Sum512(f.vendorArchive(t, "agy"))
		artifactPath := strings.Replace(agyArtifactPath, "1.0.0", f.versions["agy"], 1)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"version": f.versions["agy"], "url": "https://storage.googleapis.com" + artifactPath,
			"sha512": hex.EncodeToString(sum[:]),
		})
	case "storage.googleapis.com", "downloads.cursor.com":
		provider := "agy"
		if r.Host == "downloads.cursor.com" {
			provider = "cursor"
			etag := fmt.Sprintf("%q", f.versions[provider])
			if r.Method == http.MethodGet && r.Header.Get("If-Match") != etag {
				w.WriteHeader(http.StatusPreconditionFailed)
				return true
			}
			w.Header().Set("ETag", etag)
		}
		w.Header().Set("Last-Modified", f.published.Format(http.TimeFormat))
		if r.Method == http.MethodGet {
			f.installs[provider]++
			_, _ = w.Write(f.vendorArchive(t, provider))
		}
	default:
		return false
	}
	return true
}

func (f *updaterFixture) vendorArchive(t *testing.T, provider string) []byte {
	var data bytes.Buffer
	compressed := gzip.NewWriter(&data)
	tw := tar.NewWriter(compressed)
	spec := providerSpecs[provider]
	member := "antigravity"
	probe := "[ \"$AGY_CLI_DISABLE_AUTO_UPDATE\" = true ] || exit 9\n"
	if provider == "cursor" {
		member = "dist-package/cursor-agent"
		probe = "[ \"$1\" = --disable-auto-update ] || exit 9\nshift\n"
	}
	body := fmt.Sprintf("#!/bin/sh\n%sif [ \"$1\" = --version ]; then echo %s; else echo '%s'; fi\n", probe, f.versions[provider], strings.Join(spec.Flags, " "))
	must(t, tw.WriteHeader(&tar.Header{Name: member, Mode: 0700, Size: int64(len(body))}))
	_, err := tw.Write([]byte(body))
	must(t, err)
	must(t, tw.Close())
	must(t, compressed.Close())
	return data.Bytes()
}

func TestCursorPreventsSameDayDowngrades(t *testing.T) {
	now := time.Now().UTC()
	v := "2026.11.19-0123456"
	older := release{Version: "2026.11.19-abcdef0", Published: now.Add(-time.Hour)}
	if !olderRelease("cursor", older, v, &now) {
		t.Fatal("an older Cursor artifact with the same build date was accepted")
	}
}

func TestVendorRollbackWithoutNode(t *testing.T) {
	f := newUpdaterFixture(t)
	f.manager.nodeBinary, f.manager.npmBinary = "", ""
	t.Setenv("PATH", t.TempDir())
	ctx := context.Background()
	_, err := f.manager.UpdateProvider(ctx, "cursor")
	must(t, err)
	first := f.state(t).Current["cursor"]
	f.setVersion("2.0.0")
	_, err = f.manager.UpdateProvider(ctx, "cursor")
	must(t, err)
	report, err := f.manager.Rollback(ctx, "cursor")
	must(t, err)
	if len(report.Results) != 1 || report.Results[0].Status != "rolled-back" || f.state(t).Current["cursor"].Binary != first.Binary {
		t.Fatalf("rollback did not restore the prior Cursor binary: %+v", report)
	}
}

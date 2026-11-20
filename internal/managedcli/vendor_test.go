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
		version := f.versions["agy"]
		sum := sha512.Sum512(vendorFixtureArchive(t, "agy", version))
		artifactPath := strings.Replace(agyArtifactPath, "1.0.0", version, 1)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"version": version, "url": "https://storage.googleapis.com" + artifactPath,
			"sha512": hex.EncodeToString(sum[:]),
		})
	case "storage.googleapis.com", "downloads.cursor.com":
		provider := "agy"
		segment, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, agyStoragePrefix), "/")
		version := strings.TrimSuffix(segment, "-test")
		if r.Host == "downloads.cursor.com" {
			provider = "cursor"
			version, _, _ = strings.Cut(strings.TrimPrefix(r.URL.Path, "/lab/"), "/")
			etag := fmt.Sprintf("%q", version)
			if r.Method == http.MethodGet && r.Header.Get("If-Match") != etag {
				w.WriteHeader(http.StatusPreconditionFailed)
				return true
			}
			w.Header().Set("ETag", etag)
		}
		w.Header().Set("Last-Modified", f.published.Format(http.TimeFormat))
		if r.Method == http.MethodGet {
			if version == f.missingVendorVersion {
				http.NotFound(w, r)
				return true
			}
			f.installs[provider]++
			_, _ = w.Write(vendorFixtureArchive(t, provider, version))
		}
	default:
		return false
	}
	return true
}

func vendorFixtureArchive(t *testing.T, provider, version string) []byte {
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
	body := fmt.Sprintf("#!/bin/sh\n%sif [ \"$1\" = --version ]; then echo %s; else echo '%s'; fi\n", probe, version, strings.Join(spec.Flags, " "))
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
	if newerRelease("cursor", older, v, &now) {
		t.Fatal("an older Cursor artifact with the same build date was accepted")
	}
}

func TestCursorRefusesAmbiguousSameDayUpgrade(t *testing.T) {
	f := newUpdaterFixture(t)
	ctx := context.Background()
	first, second := "2025.12.15-abcdef0", "2025.12.15-0123456"
	f.mu.Lock()
	f.versions["cursor"] = first
	f.mu.Unlock()
	_, err := f.manager.UpdateProvider(ctx, "cursor")
	must(t, err)
	f.mu.Lock()
	f.versions["cursor"] = second
	f.mu.Unlock()
	report, err := f.manager.UpdateProvider(ctx, "cursor")
	must(t, err)
	if len(report.Results) != 1 || report.Results[0].Status != "skipped" || f.state(t).Current["cursor"].Version != first {
		t.Fatalf("Cursor switched between distinct builds with the same publication time: %+v", report)
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

func TestVendorObservationsSurviveDailyChannelChanges(t *testing.T) {
	for _, provider := range []string{"cursor", "agy"} {
		t.Run(provider, func(t *testing.T) {
			f := newUpdaterFixture(t)
			now := f.manager.Now()
			f.manager.Now = func() time.Time { return now }
			fresh := func(version string) {
				f.setVersion(version)
				f.mu.Lock()
				f.published = now.Add(-time.Minute)
				f.mu.Unlock()
			}
			check := func(manager *Manager, status, version string) {
				t.Helper()
				report, err := manager.UpdateProvider(context.Background(), provider)
				must(t, err)
				if len(report.Results) != 1 || report.Results[0].Status != status || report.Results[0].To != version {
					t.Fatalf("got %+v, want %s %s", report.Results, status, version)
				}
			}
			fresh("1.0.0")
			first := f.versions[provider]
			check(f.manager, "skipped", first)

			restarted, err := NewManager(f.manager.Root)
			must(t, err)
			restarted.Client, restarted.RunCommand, restarted.Now = f.manager.Client, f.manager.RunCommand, f.manager.Now
			now = now.Add(23*time.Hour + 50*time.Minute)
			fresh("2.0.0")
			check(restarted, "skipped", f.versions[provider])

			now = now.Add(23*time.Hour + 55*time.Minute)
			fresh("3.0.0")
			third := f.versions[provider]
			check(restarted, "updated", first)

			now = now.Add(24 * time.Hour)
			fresh("4.0.0")
			check(restarted, "updated", third)
		})
	}
}

func TestVendorObservationRetainedAfterFailedInstallAndChannelRetreat(t *testing.T) {
	f := newUpdaterFixture(t)
	now := f.manager.Now()
	f.manager.Now = func() time.Time { return now }
	update := func(version string) (Report, error) {
		t.Helper()
		f.setVersion(version)
		f.mu.Lock()
		f.published = now.Add(-time.Minute)
		f.mu.Unlock()
		return f.manager.UpdateProvider(context.Background(), "agy")
	}
	_, err := update("1.1.0")
	must(t, err)
	now = now.Add(time.Hour)
	_, err = update("1.3.0")
	must(t, err)
	now = now.Add(25 * time.Hour)
	f.mu.Lock()
	f.missingVendorVersion = "1.3.0"
	f.mu.Unlock()
	report, err := update("1.4.0")
	if err == nil || len(report.Results) != 1 || report.Results[0].To != "1.3.0" {
		t.Fatalf("expected a failed attempt to install the newest mature archive: %+v, %v", report, err)
	}
	report, err = update("1.2.0")
	must(t, err)
	if len(report.Results) != 1 || report.Results[0].To != "1.1.0" || report.Results[0].Status != "updated" {
		t.Fatalf("older eligible observation lost after the release channel retreated: %+v", report)
	}
}

func TestVendorWaitsForNewerReleaseAfterRollback(t *testing.T) {
	for _, provider := range []string{"cursor", "agy"} {
		t.Run(provider, func(t *testing.T) {
			f := newUpdaterFixture(t)
			now := f.manager.Now()
			f.manager.Now = func() time.Time { return now }
			ctx := context.Background()
			update := func(version string, age time.Duration, status string) {
				t.Helper()
				f.setVersion(version)
				f.mu.Lock()
				f.published = now.Add(-age)
				f.mu.Unlock()
				report, err := f.manager.UpdateProvider(ctx, provider)
				must(t, err)
				if len(report.Results) != 1 || report.Results[0].To != f.versions[provider] || report.Results[0].Status != status {
					t.Fatalf("update %s got %+v, want %s", version, report.Results, status)
				}
			}
			update("1.0.0", 48*time.Hour, "updated")
			first := f.versions[provider]
			update("2.0.0", time.Minute, "skipped")
			now = now.Add(48 * time.Hour)
			update("3.0.0", 25*time.Hour, "updated")
			rejected := f.versions[provider]
			_, err := f.manager.Rollback(ctx, provider)
			must(t, err)

			update("4.0.0", time.Minute, "skipped")
			update("3.0.0", 25*time.Hour, "skipped")
			state := f.state(t)
			if state.Current[provider].Version != first || state.Rejected[provider] != rejected {
				t.Fatalf("an older observation undid the rollback: %+v", state)
			}
			now = now.Add(48 * time.Hour)
			update("4.0.0", 48*time.Hour, "updated")
			if f.state(t).Rejected[provider] != "" {
				t.Fatal("a newer release did not clear the rollback marker")
			}
		})
	}
}

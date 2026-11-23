package managedcli

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	cursorInstallerURL = "https://cursor.com/install"
	agyReleaseOrigin   = "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app"
	agyStoragePrefix   = "/antigravity-public/antigravity-cli/"
	maxVendorArchive   = 512 << 20
)

type vendorArtifact struct {
	url  string
	etag string
}

func (r release) identity() string {
	if r.Artifact != nil && r.Dist.Integrity == "" {
		return "etag:" + r.Artifact.etag
	}
	return r.Dist.Integrity
}

var cursorDownloadLine = regexp.MustCompile(`(?m)^DOWNLOAD_URL="https://downloads\.cursor\.com/lab/([^/"\r\n]+)/\$\{OS\}/\$\{ARCH\}/agent-cli-package\.tar\.gz"\r?$`)

func cursorArchiveURL(version string) string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return fmt.Sprintf("https://downloads.cursor.com/lab/%s/%s/%s/agent-cli-package.tar.gz", version, runtime.GOOS, arch)
}

func validCursorETag(tag string) bool {
	return len(tag) >= 3 && len(tag) <= 128 && strings.HasPrefix(tag, `"`) && strings.HasSuffix(tag, `"`)
}

func agyArchiveURL(raw, version string) (*url.URL, error) {
	artifact, err := url.Parse(raw)
	if err != nil || artifact.Scheme != "https" || artifact.Host != "storage.googleapis.com" ||
		artifact.User != nil || artifact.RawQuery != "" || artifact.Fragment != "" ||
		artifact.Path != path.Clean(artifact.Path) || !strings.HasSuffix(artifact.Path, ".tar.gz") ||
		!strings.HasPrefix(artifact.Path, agyStoragePrefix+version+"-") {
		return nil, errors.New("invalid Antigravity release archive URL")
	}
	return artifact, nil
}

func (m *Manager) vendorCandidate(ctx context.Context, provider string) (release, error) {
	if (runtime.GOOS != "linux" && runtime.GOOS != "darwin") ||
		(runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return release{}, fmt.Errorf("%s has no managed release for %s/%s", provider, runtime.GOOS, runtime.GOARCH)
	}
	var candidate release
	switch provider {
	case "cursor":
		script, err := m.vendorMetadata(ctx, cursorInstallerURL)
		if err != nil {
			return release{}, err
		}
		match := cursorDownloadLine.FindSubmatch(script)
		if len(match) != 2 || !validProviderVersion(provider, string(match[1])) {
			return release{}, errors.New("Cursor installer did not identify a supported release")
		}
		candidate.Version = string(match[1])
		candidate.Artifact = &vendorArtifact{url: cursorArchiveURL(candidate.Version)}
	case "agy":
		data, err := m.vendorMetadata(ctx, agyReleaseOrigin+"/manifests/"+agyVendorPlatform()+".json")
		if err != nil {
			return release{}, err
		}
		var manifest struct {
			Version string `json:"version"`
			URL     string `json:"url"`
			SHA512  string `json:"sha512"`
		}
		if err := json.Unmarshal(data, &manifest); err != nil || !validProviderVersion(provider, manifest.Version) {
			return release{}, errors.New("invalid Antigravity release manifest")
		}
		artifact, err := agyArchiveURL(manifest.URL, manifest.Version)
		if err != nil {
			return release{}, err
		}
		hash, err := hex.DecodeString(manifest.SHA512)
		if err != nil || len(hash) != sha512.Size {
			return release{}, errors.New("Antigravity release requires a valid SHA512")
		}
		candidate.Version = manifest.Version
		candidate.Artifact = &vendorArtifact{url: artifact.String()}
		candidate.Dist.Integrity = "sha512-" + base64.StdEncoding.EncodeToString(hash)
	default:
		return release{}, fmt.Errorf("unsupported vendor artifact provider %q", provider)
	}

	resp, err := m.vendorRequest(ctx, http.MethodHead, candidate.Artifact.url, "")
	if err != nil {
		return release{}, err
	}
	resp.Body.Close()
	candidate.Published, err = http.ParseTime(resp.Header.Get("Last-Modified"))
	if err != nil || candidate.Published.IsZero() {
		return release{}, errors.New("vendor artifact has no valid publication time")
	}
	if provider == "cursor" {
		// Pin the official build URL and ETag before downloading the archive.
		tag := resp.Header.Get("ETag")
		if !validCursorETag(tag) {
			return release{}, errors.New("Cursor release has no strong ETag")
		}
		candidate.Artifact.etag = tag
	}
	return candidate, nil
}

func agyVendorPlatform() string {
	platform := runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "linux" {
		arch := "aarch64"
		if runtime.GOARCH == "amd64" {
			arch = "x86_64"
		}
		if _, err := os.Stat("/lib/ld-musl-" + arch + ".so.1"); err == nil {
			platform += "_musl"
		}
	}
	return platform
}

func (m *Manager) vendorMetadata(ctx context.Context, address string) ([]byte, error) {
	resp, err := m.vendorRequest(ctx, http.MethodGet, address, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	const maxMetadata = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadata+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMetadata {
		return nil, errors.New("vendor release metadata exceeds size limit")
	}
	return data, nil
}

func (m *Manager) vendorRequest(ctx context.Context, method, address, etag string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	client := *m.Client
	if method == http.MethodGet && (req.URL.Host == "downloads.cursor.com" || req.URL.Host == "storage.googleapis.com") {
		client.Timeout = commandTimeout
	}
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		same := next.URL.Host == req.URL.Host ||
			(req.URL.Host == "cursor.com" && next.URL.Host == "www.cursor.com")
		if next.URL.Scheme != "https" || !same || next.URL.User != nil || len(via) >= 5 {
			return errors.New("refuse redirect outside the HTTPS vendor origin")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch vendor release: %w", err)
	}
	if resp.StatusCode != http.StatusOK || (etag != "" && resp.Header.Get("ETag") != etag) {
		resp.Body.Close()
		return nil, fmt.Errorf("vendor release response did not match the candidate (HTTP %d)", resp.StatusCode)
	}
	return resp, nil
}

func (m *Manager) installVendorArtifact(ctx context.Context, provider string, candidate release, stage string) error {
	resp, err := m.vendorRequest(ctx, http.MethodGet, candidate.Artifact.url, candidate.Artifact.etag)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxVendorArchive {
		return errors.New("vendor archive exceeds size limit")
	}
	archive, err := os.CreateTemp(stage, ".artifact-*")
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	hash := sha512.New()
	size, copyErr := io.Copy(io.MultiWriter(archive, hash), io.LimitReader(resp.Body, maxVendorArchive+1))
	if err := errors.Join(copyErr, archive.Close()); err != nil {
		return err
	}
	if size > maxVendorArchive {
		return errors.New("vendor archive exceeds size limit")
	}
	if integrity := candidate.Dist.Integrity; integrity != "" && integrity != "sha512-"+base64.StdEncoding.EncodeToString(hash.Sum(nil)) {
		return errors.New("vendor artifact SHA512 did not match its manifest")
	}
	if err := extractVendorArchive(ctx, provider, archive.Name(), filepath.Join(stage, "artifact")); err != nil {
		return err
	}
	bin := providerSpecs[provider].Binary
	root, err := os.OpenRoot(filepath.Join(stage, "artifact"))
	if err != nil {
		return err
	}
	defer root.Close()
	payload, err := root.Stat(bin)
	if err != nil || !payload.Mode().IsRegular() || payload.Mode()&0111 == 0 {
		return errors.New("vendor archive did not provide an executable CLI")
	}
	// Pinned versions stay immutable when the upstream CLI would normally
	// perform its own updates during a request.
	// Invoke each launcher directly by its absolute installation path.
	// ${0%/*} locates artifact/ beside $0, so a symlink entry point in a
	// different directory would look for the payload in the wrong place.
	launcher := "#!/bin/sh\nAGY_CLI_DISABLE_AUTO_UPDATE=true exec \"${0%/*}/artifact/agy\" \"$@\"\n"
	if provider == "cursor" {
		launcher = "#!/bin/sh\nexec \"${0%/*}/artifact/cursor-agent\" --disable-auto-update \"$@\"\n"
	}
	return os.WriteFile(filepath.Join(stage, bin), []byte(launcher), 0700)
}

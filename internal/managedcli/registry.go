package managedcli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type release struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Deprecated string `json:"deprecated"`
	Dist       struct {
		Integrity string `json:"integrity"`
	} `json:"dist"`
	Artifact  *vendorArtifact `json:"-"`
	Published time.Time       `json:"-"`
}

type packageMetadata struct {
	Name     string                     `json:"name"`
	Tags     map[string]string          `json:"dist-tags"`
	Versions map[string]release         `json:"versions"`
	Times    map[string]json.RawMessage `json:"time"`
}

func (m *Manager) candidate(ctx context.Context, provider string) (release, error) {
	spec := providerSpecs[provider]
	if spec.Package == "" {
		return m.vendorCandidate(ctx, provider)
	}
	metadata, err := m.fetchPackageMetadata(ctx, spec.Package)
	if err != nil {
		return release{}, err
	}
	version := metadata.Tags["latest"]
	if metadata.Name != spec.Package || !validProviderVersion(provider, version) {
		return release{}, errors.New("registry did not provide the expected package and a supported release version")
	}
	if mature := matureVersion(provider, metadata, version, m.Now().Add(-observationWindow)); mature != "" {
		version = mature
	}
	candidate, ok := metadata.Versions[version]
	if !ok || candidate.Name != spec.Package || candidate.Version != version {
		return release{}, errors.New("release missing or inconsistent in registry metadata")
	}
	if candidate.Deprecated != "" {
		return release{}, errors.New("candidate release is deprecated")
	}
	if !validIntegrity(candidate.Dist.Integrity) {
		return release{}, errors.New("candidate requires a valid SHA512 package integrity value")
	}
	if err := json.Unmarshal(metadata.Times[version], &candidate.Published); err != nil || candidate.Published.IsZero() {
		return release{}, errors.New("candidate release has no valid publication time")
	}
	return candidate, nil
}

// fetchPackageMetadata downloads and decodes the full npm registry document
// for pkg.
func (m *Manager) fetchPackageMetadata(ctx context.Context, pkg string) (packageMetadata, error) {
	endpoint := npmRegistry + "/" + url.PathEscape(pkg)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return packageMetadata{}, err
	}
	// The full metadata includes publish times; abbreviated install metadata
	// does not, and would silently defeat the observation window.
	request.Header.Set("Accept", "application/json")
	response, err := m.Client.Do(request)
	if err != nil {
		return packageMetadata{}, fmt.Errorf("fetch package metadata: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return packageMetadata{}, fmt.Errorf("package metadata HTTP %d", response.StatusCode)
	}
	const limit = 64 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return packageMetadata{}, err
	}
	if len(data) > limit {
		return packageMetadata{}, errors.New("package metadata exceeds size limit")
	}
	var metadata packageMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return packageMetadata{}, fmt.Errorf("invalid package metadata: %w", err)
	}
	return metadata, nil
}

// matureVersion returns the newest supported, non-deprecated release no
// newer than upper that was published before cutoff.
//
// Fast-moving CLIs may publish several times a day. Merely rejecting an
// immature latest tag would then postpone updates forever. Selecting the
// newest mature release, bounded by the official latest tag, keeps updates
// flowing. DSH release candidates are eligible only while latest is an RC.
func matureVersion(provider string, metadata packageMetadata, upper string, cutoff time.Time) string {
	mature := ""
	allowRC := strings.Contains(upper, "-rc.")
	for v, entry := range metadata.Versions {
		if !allowRC && strings.Contains(v, "-rc.") {
			continue
		}
		if !validProviderVersion(provider, v) || compareVersions(v, upper) > 0 || entry.Deprecated != "" {
			continue
		}
		var published time.Time
		if err := json.Unmarshal(metadata.Times[v], &published); err != nil || published.IsZero() || published.After(cutoff) {
			continue
		}
		if mature == "" || compareVersions(v, mature) > 0 {
			mature = v
		}
	}
	return mature
}

func validIntegrity(value string) bool {
	if !strings.HasPrefix(value, "sha512-") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "sha512-"))
	return err == nil && len(decoded) == 64
}

func compareVersions(a, b string) int {
	aCore, aRC, _ := strings.Cut(a, "-rc.")
	bCore, bRC, _ := strings.Cut(b, "-rc.")
	left, right := strings.Split(aCore, "."), strings.Split(bCore, ".")
	for i := 0; i < 3; i++ {
		if cmp := compareVersionNumber(left[i], right[i]); cmp != 0 {
			return cmp
		}
	}
	switch {
	case aRC == bRC:
		return 0
	case aRC == "":
		return 1
	case bRC == "":
		return -1
	default:
		return compareVersionNumber(aRC, bRC)
	}
}

func compareVersionNumber(a, b string) int {
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

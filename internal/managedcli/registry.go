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
	Published time.Time `json:"-"`
}

type packageMetadata struct {
	Name     string                     `json:"name"`
	Tags     map[string]string          `json:"dist-tags"`
	Versions map[string]release         `json:"versions"`
	Times    map[string]json.RawMessage `json:"time"`
}

func (m *Manager) candidate(ctx context.Context, provider string) (release, error) {
	spec := providerSpecs[provider]
	endpoint := npmRegistry + "/" + url.PathEscape(spec.Package)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return release{}, err
	}
	// The full metadata includes publish times; abbreviated install metadata
	// does not, and would silently defeat the observation window.
	request.Header.Set("Accept", "application/json")
	response, err := m.Client.Do(request)
	if err != nil {
		return release{}, fmt.Errorf("fetch package metadata: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return release{}, fmt.Errorf("package metadata HTTP %d", response.StatusCode)
	}
	const limit = 64 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return release{}, err
	}
	if len(data) > limit {
		return release{}, errors.New("package metadata exceeds size limit")
	}
	var metadata packageMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return release{}, fmt.Errorf("invalid package metadata: %w", err)
	}
	version := metadata.Tags["latest"]
	if metadata.Name != spec.Package || !releaseVersion.MatchString(version) {
		return release{}, errors.New("registry did not provide the expected package and a stable release version")
	}
	// Fast-moving CLIs may publish several times a day. Merely rejecting
	// an immature latest tag would then postpone updates forever. Select
	// the newest mature stable release, bounded by the official latest
	// tag so versions published only to another channel cannot leak in.
	upper, mature := version, ""
	cutoff := m.Now().Add(-observationWindow)
	for v, entry := range metadata.Versions {
		if !releaseVersion.MatchString(v) || compareVersions(v, upper) > 0 || entry.Deprecated != "" {
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
	if mature != "" {
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

func validIntegrity(value string) bool {
	if !strings.HasPrefix(value, "sha512-") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "sha512-"))
	return err == nil && len(decoded) == 64
}

func compareVersions(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		if len(left[i]) < len(right[i]) {
			return -1
		}
		if len(left[i]) > len(right[i]) {
			return 1
		}
		if cmp := strings.Compare(left[i], right[i]); cmp != 0 {
			return cmp
		}
	}
	return 0
}

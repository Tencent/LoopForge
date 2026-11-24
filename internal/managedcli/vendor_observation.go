package managedcli

import (
	"slices"
	"time"
)

const maxVendorObservations = 8

type vendorObservation struct {
	Version   string    `json:"version"`
	Published time.Time `json:"published"`
	URL       string    `json:"url"`
	ETag      string    `json:"etag,omitempty"`
	Integrity string    `json:"integrity,omitempty"`
}

func observeVendorRelease(candidate release) vendorObservation {
	return vendorObservation{
		Version: candidate.Version, Published: candidate.Published,
		URL: candidate.Artifact.url, ETag: candidate.Artifact.etag, Integrity: candidate.Dist.Integrity,
	}
}

func (o vendorObservation) release(provider string) (release, bool) {
	if o.Published.IsZero() || !validProviderVersion(provider, o.Version) {
		return release{}, false
	}
	switch provider {
	case "cursor":
		if o.URL != cursorArchiveURL(o.Version) || !validCursorETag(o.ETag) || o.Integrity != "" {
			return release{}, false
		}
	case "agy":
		if _, err := agyArchiveURL(o.URL, o.Version); err != nil || !validIntegrity(o.Integrity) || o.ETag != "" {
			return release{}, false
		}
	default:
		return release{}, false
	}
	candidate := release{
		Version: o.Version, Published: o.Published,
		Artifact: &vendorArtifact{url: o.URL, etag: o.ETag},
	}
	candidate.Dist.Integrity = o.Integrity
	return candidate, true
}

// Vendor manifests advertise only their latest release. Retain observed
// archives across checks so a new publish cannot perpetually reset the
// observation window for the channel.
func (m *Manager) observeVendorReleases(state *State, provider string, latest release) (release, error) {
	installed := state.Current[provider]
	isUpgrade := func(candidate release) bool {
		return !blockedByRollback(state, provider, candidate) &&
			(installed.Version == "" || newerRelease(provider, candidate, installed.Version, installed.PublishedAt))
	}
	previous := state.VendorObservations[provider]
	if !isUpgrade(latest) {
		// A channel retreat removes observations above the advertised
		// version. Wait for a newly advertised upgrade.
		if len(previous) > 0 {
			delete(state.VendorObservations, provider)
			return latest, m.save(*state)
		}
		return latest, nil
	}

	retained := make([]vendorObservation, 0, maxVendorObservations)
	selected := latest
	cutoff := m.Now().Add(-observationWindow)
	for _, observation := range previous {
		seen, ok := observation.release(provider)
		if !ok || !isUpgrade(seen) || !newerRelease(provider, latest, seen.Version, &seen.Published) {
			continue
		}
		// At capacity, keep the earlier observations first so they can reach
		// the 24-hour gate during bursts of releases. Reserve slots for the
		// selected candidate and the latest release.
		if len(retained) < maxVendorObservations-2 {
			retained = append(retained, observation)
		}
		if latest.Published.After(cutoff) && !seen.Published.After(cutoff) &&
			(selected.Version == latest.Version || newerRelease(provider, seen, selected.Version, &selected.Published)) {
			selected = seen
		}
	}
	if selected.Version != latest.Version && !slices.ContainsFunc(retained, func(o vendorObservation) bool {
		return o.Version == selected.Version
	}) {
		retained = append(retained, observeVendorRelease(selected))
	}
	retained = append(retained, observeVendorRelease(latest))
	if !slices.Equal(previous, retained) {
		state.VendorObservations[provider] = retained
		if err := m.save(*state); err != nil {
			return release{}, err
		}
	}
	return selected, nil
}

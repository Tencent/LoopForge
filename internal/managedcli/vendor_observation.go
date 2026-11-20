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
		return candidate.Version != installed.Version && candidate.Version != state.Rejected[provider] &&
			(installed.Version == "" || !olderRelease(provider, candidate, installed.Version, installed.PublishedAt))
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
		if !ok || seen.Version == latest.Version || !isUpgrade(seen) ||
			olderRelease(provider, latest, seen.Version, &seen.Published) {
			continue
		}
		// Keep older observations until activation or a channel retreat
		// supersedes them. Reserve slots for the selection and latest.
		if len(retained) < maxVendorObservations-2 {
			retained = append(retained, observation)
		}
		if latest.Published.After(cutoff) && !seen.Published.After(cutoff) &&
			(selected.Version == latest.Version || olderRelease(provider, selected, seen.Version, &seen.Published)) {
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

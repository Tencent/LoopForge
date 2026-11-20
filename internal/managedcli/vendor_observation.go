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
	if !isUpgrade(latest) {
		return latest, nil
	}

	previous := state.VendorObservations[provider]
	retained := make([]vendorObservation, 0, maxVendorObservations)
	selected := latest
	cutoff := m.Now().Add(-observationWindow)
	if latest.Published.After(cutoff) {
		var mature release
		for _, observation := range previous {
			seen, ok := observation.release(provider)
			if !ok || seen.Version == latest.Version || !isUpgrade(seen) ||
				olderRelease(provider, latest, seen.Version, &seen.Published) {
				continue
			}
			if seen.Published.After(cutoff) {
				// Retain the oldest pending observations if checks happen
				// frequently, so at least one survives long enough to mature.
				if len(retained) < maxVendorObservations-2 {
					retained = append(retained, observation)
				}
				continue
			}
			if mature.Version == "" || olderRelease(provider, mature, seen.Version, &seen.Published) {
				mature = seen
			}
		}
		if mature.Version != "" {
			selected = mature
			retained = append(retained, observeVendorRelease(mature))
		}
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

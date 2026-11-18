package runtime

import "sync"

// ExtraCredentialSource returns deployment-specific run credentials for a
// user (credential name → secret). The open-source build registers none; a
// self-hosted deployment that manages additional credentials — for example a
// token for an internal gateway its tooling calls — registers a source at
// process start. A source answers from local storage only and must not
// perform network calls.
type ExtraCredentialSource func(userID int64) map[string]string

var (
	extraCredentialMu      sync.RWMutex
	extraCredentialSources []ExtraCredentialSource
)

// RegisterExtraCredentialSource adds a source consulted whenever run
// credentials are assembled. It is intended for process-start registration;
// the set stays fixed for the lifetime of the process.
func RegisterExtraCredentialSource(src ExtraCredentialSource) {
	if src == nil {
		return
	}
	extraCredentialMu.Lock()
	defer extraCredentialMu.Unlock()
	extraCredentialSources = append(extraCredentialSources, src)
}

// ExtraRunCredentials merges every registered source for the user. On key
// collisions the later-registered source wins. With no registered sources it
// returns nil so the common open-source path stays allocation-free.
func ExtraRunCredentials(userID int64) map[string]string {
	extraCredentialMu.RLock()
	sources := make([]ExtraCredentialSource, len(extraCredentialSources))
	copy(sources, extraCredentialSources)
	extraCredentialMu.RUnlock()
	if len(sources) == 0 {
		return nil
	}
	merged := make(map[string]string)
	for _, src := range sources {
		if src == nil {
			continue
		}
		for name, secret := range src(userID) {
			merged[name] = secret
		}
	}
	return merged
}

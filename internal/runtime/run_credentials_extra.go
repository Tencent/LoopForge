package runtime

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

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

// MissingExtraCredentialKinds returns the sorted credential kinds requested by
// env (variable name → credential kind) that are absent or empty in extra.
// Callers use it to fail a declared credential up front with the kind named,
// instead of running a command with a silently empty variable.
func MissingExtraCredentialKinds(env, extra map[string]string) []string {
	var missing []string
	for _, kind := range env {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}
		if strings.TrimSpace(extra[kind]) == "" {
			missing = append(missing, kind)
		}
	}
	sort.Strings(missing)
	return missing
}

// CommandSpecWithCredentialEnv applies a node's CredentialEnv declaration to a
// command spec: it appends the BindExtra binding so the responsible user's
// platform-managed credentials are exported as the mapped environment
// variables. A declared credential the user has not saved fails the node up
// front with the kind named — the same contract as the CodeBuddy key binding —
// instead of running a command whose authentication silently falls back to the
// execution machine's own login. Both the engine's script/verification paths
// and workspace preparation replays share it, so a rebuilt environment binds
// exactly what a normal execution bound.
func CommandSpecWithCredentialEnv(spec CommandSpec, credentialEnv map[string]string, credentials RunCredentials) (CommandSpec, error) {
	if len(credentialEnv) == 0 {
		return spec, nil
	}
	if missing := MissingExtraCredentialKinds(credentialEnv, credentials.Extra); len(missing) > 0 {
		return spec, fmt.Errorf("需求责任人未配置凭证 %s，请先在个人配置中保存", strings.Join(missing, "、"))
	}
	spec.CredentialBindings = append(append([]CredentialBinding(nil), spec.CredentialBindings...), BindExtra)
	spec.ExtraCredentialEnv = credentialEnv
	return spec, nil
}

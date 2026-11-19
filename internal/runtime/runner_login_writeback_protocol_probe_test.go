package runtime

// Test-only protocol experiment. There are deliberately no production OAuth
// endpoints, client IDs, keychain calls, native CLI launches or real logins.
// The /profile endpoint below is an assumed trusted identity oracle, NOT a
// claim that every provider exposes this endpoint/contract to its CLI clients.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type probeTokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token,omitempty"`
	ID      string `json:"id_token,omitempty"`
	Expires int64  `json:"expires_at"`
}

type probePrincipal struct {
	Subject, Account, Organization string
}

type probeGrant struct {
	Principal probePrincipal
	Family    string
	Spent     bool
}

type probeIssuer struct {
	mu                         sync.Mutex
	server                     *httptest.Server
	next                       int
	access                     map[string]probeGrant
	refresh                    map[string]probeGrant
	revoked                    map[string]bool
	rotate, revokeOnReuse      bool
	dropResponse, failProfiles bool
	failProfilesAfterExchange  bool
	tokenCalls                 int
	afterExchange              func()
}

func newProbeIssuer(t *testing.T, rotate bool) *probeIssuer {
	t.Helper()
	i := &probeIssuer{rotate: rotate, access: map[string]probeGrant{}, refresh: map[string]probeGrant{}, revoked: map[string]bool{}}
	i.server = httptest.NewTLSServer(http.HandlerFunc(i.serveHTTP))
	i.server.Client().Timeout = 2 * time.Second
	i.server.Client().CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("probe refuses redirects")
	}
	t.Cleanup(i.server.Close)
	return i
}

func (i *probeIssuer) pairLocked(grant probeGrant, refresh string) probeTokens {
	i.next++
	p := probeTokens{Access: fmt.Sprintf("fake-access-%d", i.next), Refresh: refresh,
		ID: fmt.Sprintf("fake-id-%d", i.next), Expires: time.Now().Add(time.Hour).Unix()}
	if refresh == "" {
		p.Refresh = fmt.Sprintf("fake-refresh-%d", i.next)
	}
	i.access[p.Access] = grant
	i.refresh[p.Refresh] = grant
	return p
}

func (i *probeIssuer) issue(p probePrincipal) probeTokens {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.pairLocked(probeGrant{Principal: p, Family: fmt.Sprintf("family-%d", i.next)}, "")
}

func (i *probeIssuer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	i.mu.Lock()
	if r.URL.Path == "/profile" {
		grant, ok := i.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		failed, revoked := i.failProfiles, i.revoked[grant.Family]
		i.mu.Unlock()
		if failed {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else if !ok || revoked {
			w.WriteHeader(http.StatusUnauthorized)
		} else {
			_ = json.NewEncoder(w).Encode(grant.Principal)
		}
		return
	}
	if r.URL.Path != "/token" || r.Method != http.MethodPost {
		i.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "refresh_token" {
		i.mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	i.tokenCalls++
	refresh := r.Form.Get("refresh_token")
	grant, ok := i.refresh[refresh]
	if !ok || grant.Spent || i.revoked[grant.Family] {
		if grant.Spent && i.revokeOnReuse {
			i.revoked[grant.Family] = true
		}
		i.mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	var response probeTokens
	if i.rotate {
		spent := grant
		spent.Spent = true
		i.refresh[refresh] = spent
		response = i.pairLocked(grant, "")
	} else {
		response = i.pairLocked(grant, refresh)
		// Model an OAuth provider that omits refresh_token on renewal.
		response.Refresh = ""
	}
	drop, hook := i.dropResponse, i.afterExchange
	i.dropResponse, i.afterExchange = false, nil
	if i.failProfilesAfterExchange {
		i.failProfiles = true
	}
	i.mu.Unlock()
	if hook != nil {
		hook()
	}
	if drop {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	_ = json.NewEncoder(w).Encode(response)
}

type probeClient struct{ issuer *probeIssuer }

var errProbeInvalidGrant = errors.New("probe issuer rejected refresh")

func (c probeClient) profile(access string) (probePrincipal, bool, error) {
	var identity probePrincipal
	req, _ := http.NewRequest(http.MethodGet, c.issuer.server.URL+"/profile", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := c.issuer.server.Client().Do(req)
	if err != nil {
		return identity, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return identity, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return identity, false, errors.New("probe identity service unavailable")
	}
	err = json.NewDecoder(resp.Body).Decode(&identity)
	return identity, err == nil, err
}

func (c probeClient) exchange(refresh string) (probeTokens, error) {
	var tokens probeTokens
	resp, err := c.issuer.server.Client().PostForm(c.issuer.server.URL+"/token", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refresh},
	})
	if err != nil {
		return tokens, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(resp.Body).Decode(&failure) == nil && resp.StatusCode == http.StatusBadRequest && failure.Error == "invalid_grant" {
			return tokens, errProbeInvalidGrant
		}
		return tokens, errors.New("probe refresh outcome is unknown")
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return tokens, err
	}
	if tokens.Access == "" {
		return tokens, errors.New("probe issuer returned no access token")
	}
	if tokens.Refresh == "" {
		tokens.Refresh = refresh
	}
	return tokens, nil
}

func probeReadTokens(provider string, raw []byte) probeTokens {
	var doc struct {
		Tokens probeTokens `json:"tokens"`
		Token  probeTokens `json:"token"`
		Claude struct {
			Access  string `json:"accessToken"`
			Refresh string `json:"refreshToken"`
			Expires int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		panic("invalid probe credential")
	}
	switch provider {
	case "codex":
		return doc.Tokens
	case "claude-code":
		return probeTokens{Access: doc.Claude.Access, Refresh: doc.Claude.Refresh, Expires: doc.Claude.Expires / 1000}
	default:
		return doc.Token
	}
}

func probeReplaceTokens(provider string, original []byte, tokens probeTokens) []byte {
	var doc map[string]any
	if err := json.Unmarshal(original, &doc); err != nil {
		panic(err)
	}
	key := map[string]string{"codex": "tokens", "claude-code": "claudeAiOauth", "agy": "token"}[provider]
	nested := doc[key].(map[string]any)
	if provider == "claude-code" {
		nested["accessToken"], nested["refreshToken"], nested["expiresAt"] = tokens.Access, tokens.Refresh, tokens.Expires*1000
	} else {
		nested["access_token"], nested["refresh_token"] = tokens.Access, tokens.Refresh
		if provider == "codex" {
			if tokens.ID != "" {
				nested["id_token"] = tokens.ID
			}
			doc["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
		} else {
			nested["expiry"] = time.Unix(tokens.Expires, 0).UTC().Format(time.RFC3339)
		}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return raw
}

// This state is Runner-owned, outside the sandbox. In particular, the pinned
// principal and expected host snapshot must never be accepted from a sandbox.
type probeTransaction struct {
	Provider            string
	Principal           probePrincipal
	Original, Candidate json.RawMessage
	Canonical           *probeTokens
	Phase               string
}

func probeSave(path string, state probeTransaction) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".probe-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func probeFinalize(client probeClient, host, journal string, state *probeTransaction) (string, error) {
	unchanged := func() bool {
		current, err := os.ReadFile(host)
		return err == nil && bytes.Equal(current, state.Original)
	}
	if !unchanged() {
		return "superseded", nil
	}
	if bytes.Equal(state.Candidate, state.Original) {
		return "unchanged", os.Remove(journal)
	}
	if state.Phase == "exchanging" || state.Phase == "uncertain" {
		// A lost response may have consumed the refresh token. Blind retry is
		// not safe for issuers that revoke an entire family on token reuse.
		return "uncertain", nil
	}
	if state.Canonical == nil {
		candidate := probeReadTokens(state.Provider, state.Candidate)
		principal, valid, err := client.profile(candidate.Access)
		if err != nil {
			return "deferred", nil
		}
		if valid && principal != state.Principal {
			return "rejected", nil
		}
		// An expired candidate access token cannot prove identity, but does
		// not block verifying its refresh token against the pinned principal.
		state.Phase = "exchanging"
		if err := probeSave(journal, *state); err != nil {
			return "", err
		}
		canonical, err := client.exchange(candidate.Refresh)
		if errors.Is(err, errProbeInvalidGrant) {
			return "rejected", nil
		}
		if err != nil {
			state.Phase = "uncertain"
			return "uncertain", probeSave(journal, *state)
		}
		state.Canonical, state.Phase = &canonical, "received"
		// Save the replacement pair BEFORE any fallible identity lookup.
		// Never write the sandbox pair back after redeeming its refresh token.
		if err := probeSave(journal, *state); err != nil {
			return "uncertain", err
		}
	}
	principal, valid, err := client.profile(state.Canonical.Access)
	if err != nil {
		return "deferred", nil
	}
	if !valid || principal != state.Principal {
		return "rejected", nil
	}
	if !unchanged() {
		return "superseded", nil
	}
	// Reuse the existing file writer, including its pre-rename recheck. It
	// is not a linearizable CAS against a non-cooperating native CLI.
	canonical := probeReplaceTokens(state.Provider, state.Original, *state.Canonical)
	if err := replaceRunnerLoginFile(filepath.Dir(host), filepath.Base(host), state.Original, canonical); err != nil {
		return "", err
	}
	current, err := os.ReadFile(host)
	if err != nil || !bytes.Equal(current, canonical) {
		return "superseded", err
	}
	return "accepted", os.Remove(journal)
}

type probeFixture struct {
	client        probeClient
	host, journal string
	state         probeTransaction
	initial       probeTokens
}

func newProbeFixture(t *testing.T, provider string, issuer *probeIssuer) *probeFixture {
	t.Helper()
	f := &probeFixture{client: probeClient{issuer: issuer}}
	f.initial = issuer.issue(probePrincipal{Subject: "user-a", Account: "account-a", Organization: "org-a"})
	principal, valid, err := f.client.profile(f.initial.Access)
	if err != nil || !valid {
		t.Fatal("could not establish trusted baseline")
	}
	root := t.TempDir()
	f.host = filepath.Join(root, "host-login.json")
	private := filepath.Join(root, "runner-private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	f.journal = filepath.Join(private, "pending.json")
	f.state = probeTransaction{Provider: provider, Principal: principal,
		Original: probeCredential(provider, "account-a", f.initial), Phase: "staged"}
	if err := os.WriteFile(f.host, f.state.Original, 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *probeFixture) stage(t *testing.T, candidate []byte) {
	t.Helper()
	f.state.Candidate = candidate
	if err := probeSave(f.journal, f.state); err != nil {
		t.Fatal(err)
	}
}

func (f *probeFixture) finish(t *testing.T, want string) {
	t.Helper()
	got, err := probeFinalize(f.client, f.host, f.journal, &f.state)
	if err != nil || got != want {
		t.Fatalf("finalize = %q, %v; want %q", got, err, want)
	}
}

func (f *probeFixture) renew(t *testing.T, tokens probeTokens) probeTokens {
	t.Helper()
	next, err := f.client.exchange(tokens.Refresh)
	if err != nil {
		t.Fatal("mock CLI could not renew its credential")
	}
	return next
}

func TestWritebackProbeIdentityAndRenewal(t *testing.T) {
	for _, provider := range writebackProbeProviders {
		for _, rotate := range []bool{true, false} {
			name := fmt.Sprintf("%s/rotate=%v", provider, rotate)
			t.Run(name, func(t *testing.T) {
				for _, scenario := range []string{"normal", "whole account swap", "forged local identity", "refresh token only", "access token only", "unknown refresh token", "same subject different organization", "same account different subject", "expired candidate access"} {
					t.Run(scenario, func(t *testing.T) {
						issuer := newProbeIssuer(t, rotate)
						f := newProbeFixture(t, provider, issuer)
						candidate := f.renew(t, f.initial) // Official-CLI-shaped refresh, not a real CLI.
						metadata, want := "account-a", "rejected"
						other := issuer.issue(probePrincipal{Subject: "user-b", Account: "account-b", Organization: "org-b"})
						switch scenario {
						case "normal":
							want = "accepted"
						case "whole account swap":
							metadata, candidate = "account-b", other
						case "forged local identity":
							candidate = other
						case "refresh token only":
							candidate.Refresh = other.Refresh
						case "access token only":
							candidate.Access = other.Access
						case "unknown refresh token":
							candidate.Refresh = "fake-unknown-refresh"
						case "same subject different organization":
							candidate = issuer.issue(probePrincipal{Subject: "user-a", Account: "account-a", Organization: "org-b"})
						case "same account different subject":
							candidate = issuer.issue(probePrincipal{Subject: "user-b", Account: "account-a", Organization: "org-a"})
						case "expired candidate access":
							candidate.Access, want = "fake-expired-access", "accepted"
						}
						f.stage(t, probeCredential(provider, metadata, candidate))
						f.finish(t, want)
						raw, err := os.ReadFile(f.host)
						if err != nil {
							t.Fatal(err)
						}
						if want != "accepted" {
							if !bytes.Equal(raw, f.state.Original) {
								t.Fatal("rejected candidate changed host login")
							}
							return
						}
						saved := probeReadTokens(provider, raw)
						if saved.Refresh != f.state.Canonical.Refresh || saved.Access != f.state.Canonical.Access {
							t.Fatal("host did not receive the issuer's canonical pair")
						}
						if rotate && saved.Refresh == candidate.Refresh {
							t.Fatal("spent sandbox refresh token was written back")
						}
						if !rotate && saved.Refresh != f.initial.Refresh {
							t.Fatal("omitted refresh_token erased the reusable token")
						}
						var before, after map[string]any
						_ = json.Unmarshal(f.state.Original, &before)
						_ = json.Unmarshal(raw, &after)
						if provider == "claude-code" {
							b, a := before["claudeAiOauth"].(map[string]any), after["claudeAiOauth"].(map[string]any)
							for _, key := range []string{"subscriptionType", "rateLimitTier", "scopes"} {
								if fmt.Sprint(b[key]) != fmt.Sprint(a[key]) {
									t.Fatal("refresh dropped Claude credential metadata")
								}
							}
						}
						// A subsequent simulated CLI can refresh the persisted pair.
						f.renew(t, saved)
						if info, _ := os.Stat(f.host); info.Mode().Perm() != 0600 {
							t.Fatal("host credential lost its private mode")
						}
						if _, err := os.Stat(f.journal); !os.IsNotExist(err) {
							t.Fatal("accepted pending credential was not removed")
						}
					})
				}
			})
		}
	}
}

func TestWritebackProbeRecoveryAndBoundaries(t *testing.T) {
	for _, provider := range writebackProbeProviders {
		t.Run(provider, func(t *testing.T) {
			t.Run("unchanged login does not trigger refresh", func(t *testing.T) {
				issuer := newProbeIssuer(t, true)
				f := newProbeFixture(t, provider, issuer)
				f.stage(t, f.state.Original)
				f.finish(t, "unchanged")
				issuer.mu.Lock()
				calls := issuer.tokenCalls
				issuer.mu.Unlock()
				if calls != 0 {
					t.Fatal("unchanged login unexpectedly consumed a refresh token")
				}
			})
			t.Run("identity outage after rotation and restart", func(t *testing.T) {
				issuer := newProbeIssuer(t, true)
				f := newProbeFixture(t, provider, issuer)
				f.stage(t, probeCredential(provider, "account-a", f.renew(t, f.initial)))
				issuer.mu.Lock()
				issuer.failProfilesAfterExchange = true
				issuer.mu.Unlock()
				f.finish(t, "deferred")
				if raw, _ := os.ReadFile(f.host); !bytes.Equal(raw, f.state.Original) {
					t.Fatal("unverified identity was committed")
				}
				raw, err := os.ReadFile(f.journal)
				if err != nil {
					t.Fatal(err)
				}
				f.state = probeTransaction{} // Simulated Runner restart; reload ONLY durable state.
				if err := json.Unmarshal(raw, &f.state); err != nil || f.state.Canonical == nil {
					t.Fatal("rotated response was not preserved before identity verification")
				}
				if info, _ := os.Stat(f.journal); info.Mode().Perm() != 0600 {
					t.Fatal("pending login was not private")
				}
				issuer.mu.Lock()
				issuer.failProfiles = false
				calls := issuer.tokenCalls
				issuer.mu.Unlock()
				f.finish(t, "accepted")
				issuer.mu.Lock()
				repeated := issuer.tokenCalls != calls
				issuer.mu.Unlock()
				if repeated {
					t.Fatal("restart redeemed an already consumed refresh token")
				}
				f.renew(t, *f.state.Canonical)
			})
			for _, event := range []string{"host refresh before verification", "host logout before verification", "host refresh during verification", "host logout during verification"} {
				t.Run(event, func(t *testing.T) {
					issuer := newProbeIssuer(t, true)
					f := newProbeFixture(t, provider, issuer)
					f.stage(t, probeCredential(provider, "account-a", f.renew(t, f.initial)))
					var changed []byte
					changeHost := func() {
						if strings.Contains(event, "logout") {
							if err := os.Remove(f.host); err != nil {
								t.Error(err)
							}
						} else {
							changed = probeCredential(provider, "account-b", issuer.issue(probePrincipal{Subject: "host-new-login"}))
							if err := os.WriteFile(f.host, changed, 0600); err != nil {
								t.Error(err)
							}
						}
					}
					if strings.Contains(event, "before") {
						changeHost()
					} else {
						issuer.mu.Lock()
						issuer.afterExchange = changeHost
						issuer.mu.Unlock()
					}
					f.finish(t, "superseded")
					raw, err := os.ReadFile(f.host)
					if strings.Contains(event, "logout") {
						if !os.IsNotExist(err) {
							t.Fatal("writeback resurrected a logged-out host")
						}
					} else if err != nil || !bytes.Equal(raw, changed) {
						t.Fatal("writeback overwrote a newer host login")
					}
				})
			}
			t.Run("lost refresh response cannot be recovered by quarantine", func(t *testing.T) {
				issuer := newProbeIssuer(t, true)
				f := newProbeFixture(t, provider, issuer)
				candidate := f.renew(t, f.initial)
				f.stage(t, probeCredential(provider, "account-a", candidate))
				issuer.mu.Lock()
				issuer.dropResponse = true
				issuer.mu.Unlock()
				f.finish(t, "uncertain")
				if f.state.Canonical != nil {
					t.Fatal("client invented the lost replacement token")
				}
				issuer.mu.Lock()
				spent := issuer.refresh[candidate.Refresh].Spent
				calls := issuer.tokenCalls
				issuer.mu.Unlock()
				if !spent {
					t.Fatal("test did not consume the candidate refresh token")
				}
				f.finish(t, "uncertain")
				issuer.mu.Lock()
				repeated := issuer.tokenCalls != calls
				issuer.mu.Unlock()
				if repeated {
					t.Fatal("ambiguous exchange was blindly retried")
				}
			})
		})
	}
}

func TestWritebackProbeParallelCopiesCanRevokeFamily(t *testing.T) {
	issuer := newProbeIssuer(t, true)
	issuer.revokeOnReuse = true
	client := probeClient{issuer: issuer}
	initial := issuer.issue(probePrincipal{Subject: "user-a"})
	rotated, err := client.exchange(initial.Refresh) // First sandbox/native CLI.
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.exchange(initial.Refresh); !errors.Is(err, errProbeInvalidGrant) {
		t.Fatal("second copy unexpectedly refreshed a spent token")
	}
	if _, err := client.exchange(rotated.Refresh); !errors.Is(err, errProbeInvalidGrant) {
		t.Fatal("test issuer did not revoke the active token family on reuse")
	}
	// File CAS cannot undo this server-side revocation. This is an explicit
	// limitation of the proposed verifier, not a production-provider assertion.
}

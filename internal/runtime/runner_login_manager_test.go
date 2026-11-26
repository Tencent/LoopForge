package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func managedLoginFixture(t *testing.T, provider string) (runnerLoginSource, []byte, []byte) {
	t.Helper()
	root := t.TempDir()
	jwt := func(user string, generation int) string {
		claims, _ := json.Marshal(map[string]any{"sub": user, "jti": generation,
			"exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_user_id": user}})
		return "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	}
	makeLogin := func(generation int) []byte {
		var doc any
		if provider == "codex" {
			doc = map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{
				"id_token": jwt("user-a", generation), "account_id": "account-a", "access_token": jwt("user-a", generation), "refresh_token": fmt.Sprintf("host-refresh-%d", generation)}}
		} else {
			doc = map[string]any{"claudeAiOauth": map[string]any{"accessToken": fmt.Sprintf("claude-access-%d", generation),
				"refreshToken": fmt.Sprintf("host-refresh-%d", generation), "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "test"}}
		}
		raw, _ := json.Marshal(doc)
		return raw
	}
	login, _ := runnerLoginSpec(provider)
	source := fileRunnerLoginSource(login, root)
	if provider == "claude-code" {
		if err := os.WriteFile(filepath.Join(root, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"user-a","organizationUuid":"org-a"}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	original, renewed := makeLogin(0), makeLogin(1)
	if err := os.WriteFile(source.path, original, 0600); err != nil {
		t.Fatal(err)
	}
	return source, original, renewed
}

func TestRunnerLoginManagerCoalescesConcurrentRefresh(t *testing.T) {
	for _, provider := range []string{"codex", "claude-code"} {
		t.Run(provider, func(t *testing.T) {
			source, original, renewed := managedLoginFixture(t, provider)
			var calls atomic.Int32
			manager := &runnerLoginManager{stateDir: t.TempDir(), refresh: func(context.Context, string, []byte) ([]byte, error) { calls.Add(1); return renewed, nil }}
			stale, err := manager.current(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			var workers sync.WaitGroup
			for range 12 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					got, err := manager.renew(context.Background(), source, stale)
					if err != nil || !bytes.Equal(got.raw, renewed) {
						t.Error("concurrent refresh did not reuse the authoritative pair")
					}
				}()
			}
			workers.Wait()
			if calls.Load() != 1 {
				t.Fatalf("issuer calls=%d", calls.Load())
			}
			current, _ := os.ReadFile(source.path)
			if bytes.Equal(current, original) || !bytes.Equal(current, renewed) {
				t.Fatal("host did not receive the renewed pair")
			}
		})
	}
}

func TestRunnerLoginManagerCoordinatesInstances(t *testing.T) {
	source, _, renewed := managedLoginFixture(t, "codex")
	state := t.TempDir()
	var calls atomic.Int32
	refresh := func(context.Context, string, []byte) ([]byte, error) {
		calls.Add(1)
		time.Sleep(40 * time.Millisecond)
		return renewed, nil
	}
	a, b := &runnerLoginManager{stateDir: state, refresh: refresh}, &runnerLoginManager{stateDir: state, refresh: refresh}
	stale, err := a.current(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, manager := range []*runnerLoginManager{a, b} {
		wg.Add(1)
		go func(m *runnerLoginManager) {
			defer wg.Done()
			if _, err := m.renew(context.Background(), source, stale); err != nil {
				t.Error(err)
			}
		}(manager)
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("issuer exchanges = %d", calls.Load())
	}
}

func TestRunnerLoginManagerRecoversBeforeRetry(t *testing.T) {
	source, original, renewed := managedLoginFixture(t, "codex")
	manager := &runnerLoginManager{stateDir: t.TempDir(), refresh: func(context.Context, string, []byte) ([]byte, error) {
		t.Error("consumed refresh token redeemed again")
		return nil, errors.New("unexpected exchange")
	}}
	stale, _ := manager.current(context.Background(), source)
	_, path, _ := manager.entry(source)
	if err := writeRunnerLoginPending(path+".pending", runnerLoginPending{Expected: loginDigest(original), Identity: stale.identity, Login: renewed}); err != nil {
		t.Fatal(err)
	}
	got, err := manager.renew(context.Background(), source, stale)
	if err != nil || !bytes.Equal(got.raw, renewed) {
		t.Fatal("renew did not recover the received pair first")
	}
}

func TestRunnerLoginManagerReceiptWithoutAccountMetadata(t *testing.T) {
	source, _, renewed := managedLoginFixture(t, "claude-code")
	source.identity = func([]byte) string { return "" }
	state := t.TempDir()
	first := &runnerLoginManager{stateDir: state, refresh: func(context.Context, string, []byte) ([]byte, error) { return renewed, nil }}
	stale, err := first.current(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.renew(context.Background(), source, stale); err != nil {
		t.Fatal(err)
	}
	restarted := &runnerLoginManager{stateDir: state, refresh: func(context.Context, string, []byte) ([]byte, error) {
		t.Error("unnecessary second exchange")
		return nil, errors.New("unexpected")
	}}
	got, err := restarted.renew(context.Background(), source, stale)
	if err != nil || !bytes.Equal(got.raw, renewed) {
		t.Fatal("another manager could not recognize its canonical receipt")
	}
	// A host edit without a receipt still needs a new task, even if the JSON
	// schema is unchanged. There is no account identity to safely adopt it.
	foreign := bytes.ReplaceAll(renewed, []byte("claude-access-1"), []byte("foreign-access"))
	if err := os.WriteFile(source.path, foreign, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.renew(context.Background(), source, stale); err == nil {
		t.Fatal("unanchored external change was adopted")
	}
}

type runnerOAuthTransport func(*http.Request) (*http.Response, error)

func (f runnerOAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRunnerLoginOAuthAdapter(t *testing.T) {
	for _, provider := range []string{"codex", "claude-code"} {
		for _, rotate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/rotate=%v", provider, rotate), func(t *testing.T) {
				source, original, renewed := managedLoginFixture(t, provider)
				before, _ := parseRunnerOAuth(provider, original)
				after, _ := parseRunnerOAuth(provider, renewed)
				manager := &runnerLoginManager{stateDir: t.TempDir(), client: &http.Client{Transport: runnerOAuthTransport(func(req *http.Request) (*http.Response, error) {
					if req.Method != "POST" || req.URL.Scheme != "https" {
						t.Error("OAuth exchange used an unexpected transport")
					}
					var request map[string]any
					if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if request["refresh_token"] != before.refresh || request["grant_type"] != "refresh_token" || request["client_id"] == "" {
						t.Error("exchange did not use the host grant")
					}
					response := map[string]any{"access_token": after.access, "expires_in": 3600, "scope": "user:inference user:profile"}
					if rotate {
						response["refresh_token"] = after.refresh
					}
					body, _ := json.Marshal(response)
					return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
				})}}
				stale, err := manager.current(context.Background(), source)
				if err != nil {
					t.Fatal(err)
				}
				got, err := manager.renew(context.Background(), source, stale)
				if err != nil {
					t.Fatal(err)
				}
				wantRefresh := before.refresh
				if rotate {
					wantRefresh = after.refresh
				}
				if got.access != after.access || got.refresh != wantRefresh {
					t.Fatal("canonical pair was not preserved")
				}
				if provider == "claude-code" && time.Until(got.expiry) < 59*time.Minute {
					t.Fatal("Claude expiry was not advanced")
				}
				_, path, _ := manager.entry(source)
				if _, err := os.Stat(path + ".pending"); !os.IsNotExist(err) {
					t.Fatal("completed exchange retained a second refresh token")
				}
				receipt, err := os.ReadFile(path + ".receipt")
				if err != nil || bytes.Contains(receipt, []byte("host-refresh")) {
					t.Fatal("receipt is missing or contains refresh material")
				}
			})
		}
	}
}

func TestRunnerLoginRejectsIncompleteOAuth(t *testing.T) {
	for _, provider := range []string{"codex", "claude-code"} {
		t.Run(provider, func(t *testing.T) {
			login, _ := runnerLoginSpec(provider)
			host := t.TempDir()
			root := t.TempDir()
			t.Setenv(login.configEnv, host)
			t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
			raw := `{"tokens":{"refresh_token":"host-only-refresh"}}`
			if provider == "claude-code" {
				raw = `{"claudeAiOauth":{"refreshToken":"host-only-refresh"}}`
			}
			if err := os.WriteFile(filepath.Join(host, login.file), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if cleanup, _, err := prepareConversationRunnerLogin(root, login, true); err == nil {
				if cleanup != nil {
					_ = cleanup()
				}
				t.Fatal("incomplete OAuth grant entered the sandbox")
			}
			if _, err := os.Stat(filepath.Join(root, "home", login.dir, login.file)); !os.IsNotExist(err) {
				t.Fatal("host refresh token was staged")
			}
		})
	}
}

func TestRunnerLoginManagerUncertainExchange(t *testing.T) {
	for _, definitive := range []bool{false, true} {
		t.Run(fmt.Sprintf("definitive=%v", definitive), func(t *testing.T) {
			source, original, _ := managedLoginFixture(t, "codex")
			var calls atomic.Int32
			manager := &runnerLoginManager{stateDir: t.TempDir(), refresh: func(context.Context, string, []byte) ([]byte, error) {
				calls.Add(1)
				err := errors.New("no response")
				if definitive {
					return nil, &runnerRefreshRejected{err}
				}
				return nil, err
			}}
			stale, _ := manager.current(context.Background(), source)
			for range 2 {
				if _, err := manager.renew(context.Background(), source, stale); err == nil {
					t.Fatal("exchange unexpectedly succeeded")
				}
			}
			want := int32(1)
			if definitive {
				want = 2
			}
			if calls.Load() != want {
				t.Fatalf("exchanges=%d want=%d", calls.Load(), want)
			}
			// A restart preserves the uncertain marker and still permits reading
			// the existing access token. A logout is never resurrected.
			restarted := &runnerLoginManager{stateDir: manager.stateDir, refresh: manager.refresh}
			current, err := restarted.current(context.Background(), source)
			if err != nil || !bytes.Equal(current.raw, original) {
				t.Fatal("refresh failure damaged host credentials")
			}
			if !definitive {
				if _, err := restarted.renew(context.Background(), source, current); err == nil || calls.Load() != 1 {
					t.Fatal("restart retried an uncertain exchange")
				}
			}
		})
	}
}

func TestRunnerLoginLeaseKeepsRefreshTokenOnHost(t *testing.T) {
	source, _, renewed := managedLoginFixture(t, "codex")
	var calls atomic.Int32
	manager := &runnerLoginManager{stateDir: t.TempDir(), refresh: func(context.Context, string, []byte) ([]byte, error) { calls.Add(1); return renewed, nil }}
	var leases []*runnerLoginLease
	for range 3 {
		lease, err := newRunnerLoginLease(context.Background(), manager, source, false)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.close()
		leases = append(leases, lease)
		credential, err := lease.credential()
		if err != nil || bytes.Contains(credential, []byte("host-refresh")) {
			t.Fatal("host refresh token entered a private HOME")
		}
	}
	request := func(lease *runnerLoginLease, nonce string) ([]byte, int, error) {
		body, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": nonce})
		resp, err := http.Post(lease.endpoint, "application/json", bytes.NewReader(body))
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		var result json.RawMessage
		err = json.NewDecoder(resp.Body).Decode(&result)
		return result, resp.StatusCode, err
	}
	var workers sync.WaitGroup
	for _, lease := range leases {
		workers.Add(1)
		go func(l *runnerLoginLease) {
			defer workers.Done()
			raw, status, err := request(l, l.id)
			if err != nil || status != 200 || bytes.Contains(raw, []byte("host-refresh")) {
				t.Error("lease refresh failed or exposed a host token")
			}
		}(lease)
	}
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("issuer calls=%d", calls.Load())
	}
	a, status, err := request(leases[0], leases[0].id)
	if err != nil || status != 200 {
		t.Fatal("lost-response replay failed")
	}
	b, status, err := request(leases[0], leases[0].id)
	if err != nil || status != 200 || !bytes.Equal(a, b) || calls.Load() != 1 {
		t.Fatal("replay repeated the exchange or changed its response")
	}
	_, status, err = request(leases[0], "host-refresh-0")
	if err != nil || status != 400 || calls.Load() != 1 {
		t.Fatal("a sandbox supplied its own upstream refresh token")
	}
}

func TestRunnerLoginPendingRecovery(t *testing.T) {
	for _, scenario := range []string{"recover", "logout", "other account"} {
		t.Run(scenario, func(t *testing.T) {
			source, original, renewed := managedLoginFixture(t, "codex")
			manager := &runnerLoginManager{stateDir: t.TempDir()}
			_, path, err := manager.entry(source)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeRunnerLoginPending(path+".pending", runnerLoginPending{Expected: loginDigest(original), Identity: source.identity(original), Login: renewed}); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "logout":
				if err := os.Remove(source.path); err != nil {
					t.Fatal(err)
				}
			case "other account":
				if err := os.WriteFile(source.path, bytes.ReplaceAll(original, []byte("account-a"), []byte("account-b")), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := manager.current(context.Background(), source)
			if scenario == "recover" && (err != nil || !bytes.Equal(got.raw, renewed)) {
				t.Fatal("durable response not recovered")
			}
			if scenario == "logout" {
				if err == nil {
					t.Fatal("logout resurrected")
				}
				if _, err := os.Stat(source.path); !os.IsNotExist(err) {
					t.Fatal("logout file recreated")
				}
			}
			if scenario == "other account" && (err != nil || !bytes.Contains(got.raw, []byte("account-b"))) {
				t.Fatal("pending refresh replaced another account")
			}
		})
	}
}

func TestRunnerLoginRefreshSurvivesCallerCancellation(t *testing.T) {
	source, _, renewed := managedLoginFixture(t, "codex")
	started, release := make(chan struct{}), make(chan struct{})
	manager := &runnerLoginManager{stateDir: t.TempDir(), refresh: func(ctx context.Context, _ string, _ []byte) ([]byte, error) {
		close(started)
		<-release
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return renewed, nil
	}}
	stale, err := manager.current(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := manager.renew(ctx, source, stale); done <- err }()
	<-started
	cancel()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(source.path)
	if !bytes.Equal(raw, renewed) {
		t.Fatal("cancellation lost a rotated pair")
	}
}

func TestRunnerLoginDoesNotAdoptAnotherAccount(t *testing.T) {
	source, _, renewed := managedLoginFixture(t, "codex")
	manager := &runnerLoginManager{stateDir: t.TempDir(), refresh: func(context.Context, string, []byte) ([]byte, error) {
		t.Fatal("unexpected issuer request")
		return nil, nil
	}}
	stale, err := manager.current(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source.path, bytes.ReplaceAll(renewed, []byte("account-a"), []byte("account-b")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.renew(context.Background(), source, stale); err == nil {
		t.Fatal("adopted another host account")
	}
}

func TestRunnerLoginSourceAliasChanges(t *testing.T) {
	for _, scenario := range []string{"logout", "retarget", "logout-during-exchange", "retarget-during-exchange"} {
		t.Run(scenario, func(t *testing.T) {
			target, original, renewed := managedLoginFixture(t, "codex")
			config := t.TempDir()
			login, _ := runnerLoginSpec("codex")
			alias := filepath.Join(config, login.file)
			if err := os.Symlink(target.path, alias); err != nil {
				t.Fatal(err)
			}
			source := fileRunnerLoginSource(login, config)
			change := func() {
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(scenario, "retarget") {
					other, _, _ := managedLoginFixture(t, "codex")
					if err := os.Symlink(other.path, alias); err != nil {
						t.Fatal(err)
					}
				}
			}
			manager := &runnerLoginManager{stateDir: t.TempDir(), refresh: func(context.Context, string, []byte) ([]byte, error) {
				if !strings.HasSuffix(scenario, "during-exchange") {
					t.Error("detached source refreshed")
					return nil, errors.New("unexpected")
				}
				change()
				return renewed, nil
			}}
			stale, err := manager.current(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(scenario, "during-exchange") {
				change()
			}
			if _, err := manager.renew(context.Background(), source, stale); err == nil {
				t.Fatal("continued using a detached login target")
			}
			got, err := os.ReadFile(target.path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatal("detached host source changed")
			}
		})
	}
}

func TestManagedClaudeBridge(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "claude")
	script := `#!/usr/bin/env python3
import sys,json
assert sys.argv.index('--input-format') < sys.argv.index('--')
for line in sys.stdin:
 x=json.loads(line)
 if x.get('type')=='control_request':
  print(json.dumps({'type':'control_response','response':{'subtype':'success','request_id':x['request_id']}}),flush=True)
 elif x.get('type')=='user':
  content=x['message']['content']
  if isinstance(content, list):
   assert content[0]['text']=='original task prompt' and content[1]['type']=='image'
  else:
   assert content=='original task prompt'
  print(json.dumps({'type':'control_request','request_id':'refresh-one','request':{'subtype':'oauth_token_refresh'}}),flush=True)
 elif x.get('type')=='control_response':
  assert x['response']['response']['accessToken']=='claude-access-1'
  print(json.dumps({'type':'result','is_error':False,'result':'bridge-ok'}),flush=True)
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
	}{
		{"positional prompt", []string{"-p", "--", "original task prompt"}, ""},
		{"streamed image", []string{"-p", "--input-format", "stream-json", "--"}, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"original task prompt"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, _, renewed := managedLoginFixture(t, "claude-code")
			manager := &runnerLoginManager{stateDir: t.TempDir(), refresh: func(context.Context, string, []byte) ([]byte, error) { return renewed, nil }}
			lease, err := newRunnerLoginLease(t.Context(), manager, source, false)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.close()
			command := CommandSpec{Provider: "claude-code", Argv: append([]string{bin}, tc.args...)}
			if tc.stdin != "" {
				command.Stdin = strings.NewReader(tc.stdin)
			}
			command = WithCommandEnvironment(command, lease.environment())
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			out, err := (&localSandbox{workspace: root}).Exec(ctx, command, nil)
			if err != nil || !strings.Contains(out, "bridge-ok") || strings.Contains(out, "oauth_token_refresh") || strings.Contains(out, "claude-access-1") {
				t.Fatalf("managed bridge failed or leaked control messages: %v", err)
			}
		})
	}
}

func TestManagedLoginSandboxReplacement(t *testing.T) {
	for _, provider := range []string{"codex", "claude-code"} {
		for _, mutation := range []string{"whole-account", "refresh-only", "forged-identity"} {
			t.Run(provider+"/"+mutation, func(t *testing.T) {
				source, original, _ := managedLoginFixture(t, provider)
				login, _ := runnerLoginSpec(provider)
				t.Setenv(login.configEnv, filepath.Dir(source.path))
				t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
				previous := nativeRunnerLogins
				nativeRunnerLogins = &runnerLoginManager{stateDir: t.TempDir(), refresh: func(context.Context, string, []byte) ([]byte, error) {
					t.Error("sandbox caused upstream exchange")
					return nil, errors.New("unexpected")
				}}
				defer func() { nativeRunnerLogins = previous }()
				root := t.TempDir()
				cleanup, _, err := prepareConversationRunnerLogin(root, login, true)
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				_ = json.Unmarshal(original, &doc)
				if provider == "codex" {
					tokens := doc["tokens"].(map[string]any)
					tokens["refresh_token"] = "foreign-refresh"
					if mutation != "refresh-only" {
						tokens["access_token"] = "foreign-access"
						tokens["id_token"] = "forged-id"
					}
					if mutation == "whole-account" {
						tokens["account_id"] = "another-account"
					}
				} else {
					tokens := doc["claudeAiOauth"].(map[string]any)
					tokens["refreshToken"] = "foreign-refresh"
					if mutation != "refresh-only" {
						tokens["accessToken"] = "foreign-access"
					}
				}
				candidate, _ := json.Marshal(doc)
				if err := os.WriteFile(filepath.Join(root, "home", login.dir, login.file), candidate, 0600); err != nil {
					t.Fatal(err)
				}
				if err := cleanup(); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(source.path)
				if err != nil || !bytes.Equal(got, original) {
					t.Fatal("sandbox replacement entered host credentials")
				}
			})
		}
	}
}

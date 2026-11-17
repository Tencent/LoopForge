package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

// A CLI that refreshes its rotating OAuth token inside the isolated home must
// not leave the Runner account with an invalidated refresh token: the cleanup
// writes the refreshed login back unless the host changed meanwhile or the
// refreshed file is a different account.
func TestRunnerLoginWritesBackRefreshedToken(t *testing.T) {
	const original = `{"auth_mode":"chatgpt","tokens":{"account_id":"acct-1","refresh_token":"r1"},"last_refresh":"2026-11-01"}`
	const refreshed = `{"auth_mode":"chatgpt","tokens":{"account_id":"acct-1","refresh_token":"r2"},"last_refresh":"2026-11-17"}`
	setup := func(t *testing.T) (conversationLoginSpec, string, string, func() error) {
		t.Helper()
		login, _ := runnerLoginSpec("codex")
		host := t.TempDir()
		t.Setenv(login.configEnv, host)
		if err := os.WriteFile(filepath.Join(host, login.file), []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		cleanup, _, err := prepareConversationRunnerLogin(root, login, true)
		if err != nil {
			t.Fatal(err)
		}
		return login, host, filepath.Join(root, "home", login.dir, login.file), cleanup
	}
	read := func(t *testing.T, path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	t.Run("refreshed", func(t *testing.T) {
		login, host, copy, cleanup := setup(t)
		if err := os.WriteFile(copy, []byte(refreshed), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(host, login.file)); got != refreshed {
			t.Fatalf("host login = %s, want the refreshed one", got)
		}
		if info, _ := os.Stat(filepath.Join(host, login.file)); info.Mode().Perm() != 0o600 {
			t.Fatalf("host login mode = %v", info.Mode().Perm())
		}
		if _, err := os.Stat(copy); !os.IsNotExist(err) {
			t.Fatal("isolated copy was not removed")
		}
		entries, _ := os.ReadDir(host)
		if len(entries) != 1 {
			t.Fatalf("temporary files left in the host config dir: %v", entries)
		}
	})
	t.Run("unchanged", func(t *testing.T) {
		login, host, _, cleanup := setup(t)
		before, _ := os.Stat(filepath.Join(host, login.file))
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
		after, _ := os.Stat(filepath.Join(host, login.file))
		if !after.ModTime().Equal(before.ModTime()) || read(t, filepath.Join(host, login.file)) != original {
			t.Fatal("an unchanged login was rewritten")
		}
	})
	t.Run("host refreshed first", func(t *testing.T) {
		login, host, copy, cleanup := setup(t)
		const hostRefresh = `{"auth_mode":"chatgpt","tokens":{"account_id":"acct-1","refresh_token":"host"},"last_refresh":"2026-11-17"}`
		if err := os.WriteFile(filepath.Join(host, login.file), []byte(hostRefresh), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(copy, []byte(refreshed), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(host, login.file)); got != hostRefresh {
			t.Fatalf("host login overwritten: %s", got)
		}
	})
	t.Run("other account", func(t *testing.T) {
		login, host, copy, cleanup := setup(t)
		if err := os.WriteFile(copy, []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"acct-evil","refresh_token":"x"},"last_refresh":"2026-11-17"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(host, login.file)); got != original {
			t.Fatalf("another account was written back: %s", got)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		login, host, copy, cleanup := setup(t)
		elsewhere := filepath.Join(t.TempDir(), "auth.json")
		if err := os.WriteFile(elsewhere, []byte(refreshed), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(copy); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, copy); err != nil {
			t.Fatal(err)
		}
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(host, login.file)); got != original {
			t.Fatalf("a planted symlink was followed: %s", got)
		}
	})
}

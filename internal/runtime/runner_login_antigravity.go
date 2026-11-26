package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goRuntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

const antigravityTokenFile = "antigravity-oauth-token"

var antigravityKeyringMu sync.Mutex

type antigravityLogin struct {
	AuthMethod string `json:"auth_method"`
	Email      string `json:"id_token_email"`
	IDToken    string `json:"id_token"`
	Token      struct {
		Access  string          `json:"access_token"`
		Refresh string          `json:"refresh_token"`
		Type    json.RawMessage `json:"token_type"`
		Expiry  json.RawMessage `json:"expiry"`
	} `json:"token"`
}

func parseAntigravityLogin(data []byte) (antigravityLogin, error) {
	var login antigravityLogin
	if len(data) > 1<<20 || json.Unmarshal(data, &login) != nil || login.Token.Refresh == "" {
		return login, fmt.Errorf("invalid Antigravity login")
	}
	return login, nil
}

func sameAntigravityLogin(before, after []byte) bool {
	a, err := parseAntigravityLogin(before)
	if err != nil {
		return false
	}
	b, err := parseAntigravityLogin(after)
	if err != nil || a.AuthMethod != b.AuthMethod || a.Token.Refresh != b.Token.Refresh {
		return false
	}
	email := func(login antigravityLogin) string {
		if login.Email != "" {
			return strings.ToLower(strings.TrimSpace(login.Email))
		}
		parts := strings.Split(login.IDToken, ".")
		if len(parts) != 3 {
			return ""
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
		var claims struct {
			Email string `json:"email"`
		}
		if json.Unmarshal(payload, &claims) != nil {
			return ""
		}
		return strings.ToLower(strings.TrimSpace(claims.Email))
	}
	aEmail, bEmail := email(a), email(b)
	if aEmail != "" && bEmail != "" {
		return aEmail == bEmail
	}
	// Legacy records without an email may be refreshed, but cannot change
	// their identity by substituting a different refresh token.
	return a.Token.Refresh == b.Token.Refresh
}

func antigravityUsesFileStorage() bool {
	for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY", "WSL_DISTRO_NAME", "WSL_INTEROP"} {
		if os.Getenv(key) != "" {
			return true
		}
	}
	if goRuntime.GOOS == "linux" {
		if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
			return true
		}
		for _, path := range []string{"/.dockerenv", "/run/.containerenv"} {
			if _, err := os.Stat(path); err == nil {
				return true
			}
		}
	}
	return false
}

func readAntigravityRunnerLogin() ([]byte, func([]byte) error, error) {
	if antigravityUsesFileStorage() {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, err
		}
		dir := filepath.Join(home, ".gemini", "antigravity-cli")
		original, err := readRunnerLoginFile(dir, antigravityTokenFile)
		if err != nil {
			return nil, nil, err
		}
		if _, err := parseAntigravityLogin(original); err != nil {
			return nil, nil, err
		}
		return original, func(refreshed []byte) error {
			if bytes.Equal(original, refreshed) {
				return nil
			}
			if !sameAntigravityLogin(original, refreshed) {
				return fmt.Errorf("refreshed Antigravity login belongs to a different account")
			}
			if current, err := readRunnerLoginFile(dir, antigravityTokenFile); err != nil || !bytes.Equal(current, original) {
				return nil
			}
			canonical, err := canonicalAntigravityRefresh(original, refreshed)
			if err != nil || canonical == nil {
				return err
			}
			return replaceRunnerLoginFile(dir, antigravityTokenFile, original, canonical)
		}, nil
	}
	return antigravityKeyringLogin(readAntigravityKeyring, writeAntigravityKeyring)
}

// The injectable store keeps tests away from the user's real OS credentials.
func antigravityKeyringLogin(read func() (string, error), write func(string) error) ([]byte, func([]byte) error, error) {
	stored, err := read()
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read Antigravity system credentials")
	}
	original := []byte(stored)
	if _, err := parseAntigravityLogin(original); err != nil {
		return nil, nil, err
	}
	return original, func(refreshed []byte) error {
		if bytes.Equal(original, refreshed) {
			return nil
		}
		if !sameAntigravityLogin(original, refreshed) {
			return fmt.Errorf("refreshed Antigravity login belongs to a different account")
		}
		antigravityKeyringMu.Lock()
		defer antigravityKeyringMu.Unlock()
		current, err := read()
		if err != nil || current != stored {
			return nil
		}
		canonical, err := canonicalAntigravityRefresh(original, refreshed)
		if err != nil || canonical == nil {
			return err
		}
		if current, err := read(); err != nil || current != stored {
			return nil
		}
		if err := write(string(canonical)); err != nil {
			return fmt.Errorf("cannot save refreshed Antigravity system credentials")
		}
		return nil
	}, nil
}

func readAntigravityKeyring() (string, error) {
	if goRuntime.GOOS != "darwin" {
		return keyring.Get("gemini", "antigravity")
	}
	// Match agy's go-keyring encoding while bounding a locked keychain prompt.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", "gemini", "-a", "antigravity", "-w").Output()
	if err != nil {
		return "", fmt.Errorf("cannot read Antigravity keychain item")
	}
	value := strings.TrimSpace(string(data))
	if encoded, ok := strings.CutPrefix(value, "go-keyring-base64:"); ok {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		return string(decoded), err
	}
	if encoded, ok := strings.CutPrefix(value, "go-keyring-encoded:"); ok {
		decoded, err := hex.DecodeString(encoded)
		return string(decoded), err
	}
	return value, nil
}

func writeAntigravityKeyring(value string) error {
	if goRuntime.GOOS != "darwin" {
		return keyring.Set("gemini", "antigravity", value)
	}
	encoded := "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(value))
	command := "add-generic-password -U -s gemini -a antigravity -w " + encoded + "\n"
	if len(command) > 4000 {
		return fmt.Errorf("Antigravity keychain item is too large")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Updating in place preserves the existing ACL. The secret is passed only
	// through stdin, never in argv, a shell command line, or diagnostic output.
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(command)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cannot update Antigravity keychain item")
	}
	return nil
}

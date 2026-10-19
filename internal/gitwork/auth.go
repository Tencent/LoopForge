// Package gitwork holds the git mechanics shared by the control plane and the
// runner daemon: authenticated git commands, work-branch push/pull and the
// rules for which workspace files belong in a repository change set. It has
// no knowledge of runs, stores or policy; callers decide what to sync.
package gitwork

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// TokenEnv is the host-level fallback token, only used for output redaction.
const TokenEnv = "AGENTFLOW_GIT_TOKEN"

// HTTPUser is the Basic-auth user for tokens: Gongfeng HTTPS Git rejects
// oauth2 Basic auth, while a PAT works as user "private".
const HTTPUser = "private"

type tokenCtxKey struct{}

// EnvToken returns the host-level token from TokenEnv.
func EnvToken() string {
	return strings.TrimSpace(os.Getenv(TokenEnv))
}

// WithToken attaches the token Run uses for this context's git commands.
func WithToken(ctx context.Context, token string) context.Context {
	token = strings.TrimSpace(token)
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, tokenCtxKey{}, token)
}

// TokenFrom returns the token attached with WithToken.
func TokenFrom(ctx context.Context) string {
	if ctx != nil {
		if t, ok := ctx.Value(tokenCtxKey{}).(string); ok {
			if t = strings.TrimSpace(t); t != "" {
				return t
			}
		}
	}
	return ""
}

// gongfengInsteadOfPrefixes are rewritten to HTTPS + private:token. Same list
// for command-line -c and GIT_CONFIG_COUNT so clone/fetch/push and an Agent
// CLI's own git all see one rewrite.
var gongfengInsteadOfPrefixes = []string{
	"git@git.example.com:",
	"ssh://git@git.example.com/",
	"https://git.example.com/",
	"http://git.example.com/",
}

func authedBase(token string) string {
	return "https://" + HTTPUser + ":" + strings.TrimSpace(token) + "@git.example.com/"
}

// SandboxEnv is the git environment for a child process: no interactive
// prompts, plus Gongfeng URL rewrites carrying the token when one is set.
func SandboxEnv(token string) map[string]string {
	extra := map[string]string{"GIT_TERMINAL_PROMPT": "0"}
	token = strings.TrimSpace(token)
	if token == "" {
		return extra
	}
	// The credential helper reads these. The token stays out of the helper
	// text, which git runs through a shell.
	extra[gitHTTPUserEnv] = HTTPUser
	extra[gitHTTPPassEnv] = token
	authed := authedBase(token)
	extra["GIT_CONFIG_COUNT"] = fmt.Sprintf("%d", len(gongfengInsteadOfPrefixes))
	for i, prefix := range gongfengInsteadOfPrefixes {
		extra[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)] = "url." + authed + ".insteadof"
		extra[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)] = prefix
	}
	return extra
}

// Env is the process environment with inherited git config/askpass overrides
// replaced by SandboxEnv(token).
func Env(token string) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "GIT_CONFIG_COUNT="),
			strings.HasPrefix(kv, "GIT_CONFIG_KEY_"),
			strings.HasPrefix(kv, "GIT_CONFIG_VALUE_"),
			strings.HasPrefix(kv, "GIT_ASKPASS="),
			strings.HasPrefix(kv, "SSH_ASKPASS="),
			strings.HasPrefix(kv, gitHTTPUserEnv+"="),
			strings.HasPrefix(kv, gitHTTPPassEnv+"="):
			continue
		}
		env = append(env, kv)
	}
	for k, v := range SandboxEnv(token) {
		env = append(env, k+"="+v)
	}
	return env
}

// CommandDir picks an existing directory to run git from: the workspace, its
// parent (before a clone creates it), or the process working directory.
func CommandDir(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace != "" {
		if st, err := os.Stat(workspace); err == nil && st.IsDir() {
			return workspace
		}
		parent := filepath.Dir(workspace)
		if st, err := os.Stat(parent); err == nil && st.IsDir() {
			return parent
		}
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return wd
	}
	return os.TempDir()
}

// WithEnvValue sets key in env, replacing every existing entry for it.
func WithEnvValue(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	found := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			if !found {
				out = append(out, prefix+value)
				found = true
			}
			continue
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, prefix+value)
	}
	return out
}

// gitHTTPUserEnv and gitHTTPPassEnv carry the Basic-auth pair for one git
// process. credentialHelper reads them so a token cannot break out of the
// shell snippet git executes for a `!` helper.
const (
	gitHTTPUserEnv = "AGENTFLOW_GIT_HTTP_USER"
	gitHTTPPassEnv = "AGENTFLOW_GIT_HTTP_PASSWORD"
)

func credentialHelper() string {
	return `!f() { test "$1" = get || exit 0; printf 'username=%s\npassword=%s\n' "$` + gitHTTPUserEnv + `" "$` + gitHTTPPassEnv + `"; }; f`
}

// AuthConfigArgs are `git -c` arguments that authenticate one command with
// token without writing it to any git config file.
func AuthConfigArgs(token string) []string {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	args := []string{"-c", "credential.helper=", "-c", "credential.helper=" + credentialHelper()}
	authed := authedBase(token)
	for _, prefix := range gongfengInsteadOfPrefixes {
		args = append(args, "-c", "url."+authed+".insteadof="+prefix)
	}
	return args
}

// RewriteRemoteForToken embeds token in an HTTPS (or git@ SSH) remote URL.
func RewriteRemoteForToken(remote, token string) string {
	remote = strings.TrimSpace(remote)
	token = strings.TrimSpace(token)
	if remote == "" || token == "" {
		return remote
	}
	if strings.HasPrefix(remote, "git@") {
		hostPath := strings.TrimPrefix(remote, "git@")
		host, path, ok := strings.Cut(hostPath, ":")
		if !ok || host == "" || path == "" {
			return remote
		}
		return "https://" + HTTPUser + ":" + token + "@" + host + "/" + strings.TrimPrefix(path, "/")
	}
	u, err := url.Parse(remote)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return remote
	}
	if u.User != nil && u.User.Username() != "" && !strings.EqualFold(u.User.Username(), "oauth2") {
		return remote
	}
	u.User = url.UserPassword(HTTPUser, token)
	return u.String()
}

// Redact masks tokens (and the host-level EnvToken) in git output.
func Redact(s string, tokens ...string) string {
	seen := map[string]struct{}{}
	for _, token := range append(tokens, EnvToken()) {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		s = strings.ReplaceAll(s, token, "***")
	}
	return s
}

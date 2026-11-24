package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"agentflow/internal/gitwork"
)

const (
	// SessionGatewayEnv is the session relay URL inside a workspace turn. It
	// carries the turn's session token and no Git token.
	SessionGatewayEnv = "AGENTFLOW_SESSION_GATEWAY"
	// GitHostsEnv lists "host=platform" pairs the speaker exposes to the
	// workspace, comma separated. It is informational and holds no tokens.
	GitHostsEnv = "AGENTFLOW_GIT_HOSTS"

	gitCredentialMethod = "agentflow/gitCredential"
)

// gitCredentialReply is the gateway's answer; an empty password means the
// platform has no credential for the host.
type gitCredentialReply struct {
	Username          string `json:"username"`
	Password          string `json:"password"`
	PasswordExpiryUTC int64  `json:"password_expiry_utc"`
	Platform          string `json:"platform"`
}

// RunGitHelperCLI handles the hidden workspace helper commands:
//
//	git-credential <get|store|erase>   Git credential helper protocol
//	cli-shim <gh|glab> [args...]       run gh/glab with the host's token
func RunGitHelperCLI(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "git-credential":
		os.Exit(gitCredentialMain(args[1:], os.Stdin, os.Stdout, os.Stderr))
	case "cli-shim":
		os.Exit(cliShimMain(args[1:]))
	default:
		return false
	}
	return true
}

// gitCredentialMain only answers "get". The platform owns the tokens, so
// "store" and "erase" are accepted and ignored; a host without a credential
// gets no output and Git carries on with its default behaviour.
func gitCredentialMain(args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(errOut, "usage: agentflow git-credential <get|store|erase>")
		return 2
	}
	attrs := readGitCredentialAttrs(in)
	if args[0] != "get" || attrs["protocol"] != "https" || attrs["host"] == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reply, err := requestGitCredential(ctx, attrs["protocol"], attrs["host"], "git")
	if err != nil {
		fmt.Fprintln(errOut, "agentflow: "+err.Error())
		return 0
	}
	if reply.Password == "" || strings.ContainsAny(reply.Username+reply.Password, "\r\n\x00") {
		return 0
	}
	fmt.Fprintf(out, "username=%s\npassword=%s\n", reply.Username, reply.Password)
	if reply.PasswordExpiryUTC > 0 {
		fmt.Fprintf(out, "password_expiry_utc=%d\n", reply.PasswordExpiryUTC)
	}
	return 0
}

func readGitCredentialAttrs(in io.Reader) map[string]string {
	attrs := map[string]string{}
	scanner := bufio.NewScanner(io.LimitReader(in, 64<<10))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			attrs[key] = value
		}
	}
	return attrs
}

// requestGitCredential asks the turn's gateway for one host's credential
// through the session relay. The relay is local or adjacent to the sandbox,
// so the request never goes through a proxy.
func requestGitCredential(ctx context.Context, protocol, host, caller string) (gitCredentialReply, error) {
	gateway := strings.TrimSpace(os.Getenv(SessionGatewayEnv))
	if gateway == "" {
		return gitCredentialReply{}, errors.New("not running in a LoopForge workspace turn")
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": gitCredentialMethod,
		"params": map[string]string{"protocol": protocol, "host": host, "caller": caller},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway, bytes.NewReader(body))
	if err != nil {
		return gitCredentialReply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return gitCredentialReply{}, fmt.Errorf("session gateway unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return gitCredentialReply{}, fmt.Errorf("session gateway answered HTTP %d; the turn may have ended", res.StatusCode)
	}
	var envelope struct {
		Result gitCredentialReply `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&envelope); err != nil {
		return gitCredentialReply{}, fmt.Errorf("invalid session gateway reply: %w", err)
	}
	if envelope.Error != nil {
		return gitCredentialReply{}, errors.New(envelope.Error.Message)
	}
	return envelope.Result, nil
}

// cliShimMain runs the real gh or glab with the target host's token in that
// one process's environment, so the token is never written to a config file
// and background processes do not inherit it.
func cliShimMain(args []string) int {
	if len(args) == 0 || (args[0] != "gh" && args[0] != "glab") {
		fmt.Fprintln(os.Stderr, "usage: agentflow cli-shim <gh|glab> [args...]")
		return 2
	}
	name, rest := args[0], args[1:]
	self, _ := os.Executable()
	real, err := lookPathSkipping(name, filepath.Dir(self), os.Getenv("PATH"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: command not found in this workspace; install it or call the platform API with a token from `git credential fill`\n", name)
		return 127
	}
	env := os.Environ()
	if host := cliShimHost(name, rest, os.Getenv, originRemote); host != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		reply, err := requestGitCredential(ctx, "https", host, name)
		cancel()
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "agentflow: no platform token for %s: %v\n", host, err)
		case reply.Password != "":
			env = cliShimEnv(name, host, reply.Password, env)
		}
	}
	if err := syscall.Exec(real, append([]string{name}, rest...), env); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		return 126
	}
	return 0
}

// lookPathSkipping finds name on PATH, skipping skip (the wrapper's own bin
// directory) so the shim never executes itself.
func lookPathSkipping(name, skip, path string) (string, error) {
	skip = filepath.Clean(skip)
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || filepath.Clean(dir) == skip {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

// cliShimHost picks the host gh or glab is about to call: the CLI's host
// variable, then a --hostname flag, then a host-qualified --repo, then the
// current repository's origin, then the CLI's public default.
func cliShimHost(name string, args []string, getenv func(string) string, origin func() string) string {
	vars, fallback := []string{"GH_HOST"}, "github.com"
	if name == "glab" {
		vars, fallback = []string{"GITLAB_HOST", "GL_HOST"}, "gitlab.com"
	}
	for _, key := range vars {
		if host := cliHostScope(getenv(key)); host != "" {
			return host
		}
	}
	if value := cliFlagValue(args, "--hostname", ""); value != "" {
		return cliHostScope(value)
	}
	if repo := cliFlagValue(args, "--repo", "-R"); repo != "" {
		if host := cliRepoHost(repo); host != "" {
			return host
		}
	}
	if host := remoteHost(origin()); host != "" {
		return host
	}
	return fallback
}

func cliHostScope(raw string) string {
	host, err := gitwork.HTTPSHostScope(raw)
	if err != nil {
		return ""
	}
	return host
}

func cliFlagValue(args []string, long, short string) string {
	for i, arg := range args {
		if arg == "--" {
			break
		}
		for _, flag := range []string{long, short} {
			if flag == "" {
				continue
			}
			if arg == flag && i+1 < len(args) {
				return args[i+1]
			}
			if value, ok := strings.CutPrefix(arg, flag+"="); ok {
				return value
			}
		}
	}
	return ""
}

// cliRepoHost reads the host of "HOST/OWNER/REPO" or a repository URL; plain
// "OWNER/REPO" names no host.
func cliRepoHost(repo string) string {
	if strings.Contains(repo, "://") {
		if u, err := url.Parse(repo); err == nil {
			return cliHostScope("https://" + u.Host)
		}
		return ""
	}
	if parts := strings.Split(repo, "/"); len(parts) >= 3 && (strings.Contains(parts[0], ".") || strings.HasPrefix(parts[0], "[")) {
		return cliHostScope(parts[0])
	}
	return ""
}

// remoteHost is the API host of an HTTPS or SSH remote; gh and glab call the
// HTTPS API whichever protocol Git uses.
func remoteHost(remote string) string {
	if host, ok := gitwork.HTTPSRemoteHost(remote); ok {
		return host
	}
	if parsed, err := gitwork.ParseSSHRemote(remote); err == nil {
		return cliHostScope(parsed.Host)
	}
	return ""
}

func originRemote() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// cliShimEnv sets the token variables gh and glab read for host, replacing
// any inherited values.
func cliShimEnv(name, host, token string, env []string) []string {
	set := map[string]string{}
	switch {
	case name == "glab":
		set["GITLAB_TOKEN"] = token
		set["GITLAB_HOST"] = "https://" + host
	case host == "github.com":
		set["GH_TOKEN"] = token
	default:
		set["GH_ENTERPRISE_TOKEN"] = token
		set["GH_HOST"] = host
	}
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if _, replaced := set[key]; !replaced {
			out = append(out, kv)
		}
	}
	for key, value := range set {
		out = append(out, key+"="+value)
	}
	return out
}

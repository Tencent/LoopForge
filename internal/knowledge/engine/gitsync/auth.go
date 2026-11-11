package gitsync

import (
	"agentflow/internal/knowledge/gitremote"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"agentflow/internal/knowledge"
)

type credentialContext struct{}
type authEnvironment struct{}

func WithCredential(ctx context.Context, c *knowledge.GitCredential) context.Context {
	return context.WithValue(ctx, credentialContext{}, c)
}

// syncAuthenticated keeps secrets in short-lived owner-only files. Child argv
// and environment contain paths only. Neither credential helper nor SSH agent
// can fall back to a platform user's personal credentials.
func syncAuthenticated(ctx context.Context, dir, remote, branch string, old map[string]string, options []string, c *knowledge.GitCredential, sshIP string) (string, string, []FileInfo, []string, []string, error) {
	ctx = context.WithValue(ctx, publicGitOptions{}, options)
	// libcurl may consult .netrc via the process home even when Git helpers
	// are disabled. Give this child an empty, temporary configuration home.
	temp, err := os.MkdirTemp("", "knowledge-git-auth-")
	if err != nil {
		return "", "", nil, nil, nil, errors.New("无法准备来源凭据")
	}
	defer os.RemoveAll(temp)
	env := []string{"GIT_ASKPASS=/bin/false", "SSH_ASKPASS=/bin/false", "HOME=" + temp, "CURL_HOME=" + temp, "XDG_CONFIG_HOME=" + temp}
	if c != nil {
		write := func(name, content string) error {
			return os.WriteFile(filepath.Join(temp, name), []byte(content), 0600)
		}
		switch c.Kind {
		case "https":
			if !strings.HasPrefix(remote, "https://") {
				return "", "", nil, nil, nil, errors.New("credential protocol mismatch")
			}
			if err := write("username", c.Username); err != nil {
				return "", "", nil, nil, nil, errors.New("无法准备来源凭据")
			}
			if err := write("token", c.Token); err != nil {
				return "", "", nil, nil, nil, errors.New("无法准备来源凭据")
			}
			script := "#!/bin/sh\ncase \"$1\" in\n *Username*) cat " + shellQuote(filepath.Join(temp, "username")) + ";;\n *Password*) cat " + shellQuote(filepath.Join(temp, "token")) + ";;\n *) exit 1;;\nesac\n"
			askpass := filepath.Join(temp, "askpass")
			if err := os.WriteFile(askpass, []byte(script), 0700); err != nil {
				return "", "", nil, nil, nil, errors.New("无法准备来源凭据")
			}
			env = append(env, "GIT_ASKPASS="+askpass)
		case "ssh":
			u, err := gitremote.Parse(remote)
			if err != nil || u.Scheme != "ssh" || sshIP == "" {
				return "", "", nil, nil, nil, errors.New("credential protocol mismatch")
			}
			if err := write("key", c.PrivateKey); err != nil {
				return "", "", nil, nil, nil, errors.New("无法准备来源凭据")
			}
			if err := write("known_hosts", c.KnownHosts); err != nil {
				return "", "", nil, nil, nil, errors.New("无法准备来源凭据")
			}
			args := []string{"ssh", "-F", "/dev/null", "-i", filepath.Join(temp, "key"), "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + filepath.Join(temp, "known_hosts"), "-o", "GlobalKnownHostsFile=/dev/null", "-o", "HostName=" + sshIP, "-o", "HostKeyAlias=" + u.Hostname(), "-o", "ProxyCommand=none", "-o", "ProxyJump=none", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "-o", "ConnectTimeout=20"}
			quoted := make([]string, len(args))
			for i, arg := range args {
				quoted[i] = shellQuote(arg)
			}
			env = append(env, "GIT_SSH_COMMAND="+strings.Join(quoted, " "), "GIT_SSH_VARIANT=ssh")
		default:
			return "", "", nil, nil, nil, errors.New("invalid source credential")
		}
	} else if !strings.HasPrefix(remote, "https://") {
		return "", "", nil, nil, nil, errors.New("authentication failed: SSH credential required")
	}
	ctx = context.WithValue(ctx, authEnvironment{}, env)
	return Sync(ctx, dir, remote, branch, old)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// Remote stderr is untrusted and may echo a password or private key. Preserve
// only recognized categories, never raw output, arguments or environment.
func safeGitError(output string) error {
	raw := strings.ToLower(output)
	for _, group := range []struct {
		message string
		terms   []string
	}{
		{"host key verification failed", []string{"host key verification failed", "remote host identification has changed"}},
		{"authentication failed", []string{"authentication failed", "permission denied", "could not read username", "terminal prompts disabled", "error: 401", "error: 403"}},
		{"remote branch missing", []string{"remote branch", "couldn't find remote ref", "could not find remote ref"}},
		{"repository not found", []string{"repository not found", "does not exist", "requested url returned error: 404"}},
		{"failed to connect", []string{"unable to access", "could not resolve", "failed to connect", "connection refused", "timed out", "network is unreachable", "certificate"}},
	} {
		for _, term := range group.terms {
			if strings.Contains(raw, term) {
				return errors.New(group.message)
			}
		}
	}
	return errors.New("Git operation failed")
}

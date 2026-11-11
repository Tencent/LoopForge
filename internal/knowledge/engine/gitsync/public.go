package gitsync

import (
	"agentflow/internal/knowledge"
	"agentflow/internal/knowledge/gitremote"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type publicGitOptions struct{}

// SyncRemote pins DNS to validated public addresses and disables redirects
// and proxies, so Git cannot re-resolve a checked hostname to a private target.
func SyncRemote(ctx context.Context, dir, remote, branch string, old map[string]string) (string, string, []FileInfo, []string, []string, error) {
	options, sshIP, err := remoteGitOptions(ctx, remote)
	if err != nil {
		return "", "", nil, nil, nil, err
	}
	credential, _ := ctx.Value(credentialContext{}).(*knowledge.GitCredential)
	return syncAuthenticated(ctx, dir, strings.TrimSpace(remote), branch, old, options, credential, sshIP)
}

// CommitAndPushRemote is the authenticated write path: validate the public
// address exactly like a sync, then commit one reviewed file on a new branch
// and push that branch. Credentials still live only in the short-lived child
// environment prepared by authenticatedGitContext.
func CommitAndPushRemote(ctx context.Context, dir, remote, baseBranch string, opts WriteOptions) (WriteResult, error) {
	options, sshIP, err := remoteGitOptions(ctx, remote)
	if err != nil {
		return WriteResult{}, err
	}
	credential, _ := ctx.Value(credentialContext{}).(*knowledge.GitCredential)
	authCtx, cleanup, err := authenticatedGitContext(ctx, strings.TrimSpace(remote), options, credential, sshIP)
	if err != nil {
		return WriteResult{}, err
	}
	defer cleanup()
	if err := privateCacheDir(dir); err != nil {
		return WriteResult{}, err
	}
	return CommitAndPush(authCtx, dir, strings.TrimSpace(remote), baseBranch, opts)
}

// RemoteContainsCommitRemote refreshes the tracked branch and reports whether
// commit is already an ancestor of it. This is the platform-independent merge
// probe used when the hosting platform cannot report merge state.
func RemoteContainsCommitRemote(ctx context.Context, dir, remote, baseBranch, commit string) (bool, error) {
	options, sshIP, err := remoteGitOptions(ctx, remote)
	if err != nil {
		return false, err
	}
	credential, _ := ctx.Value(credentialContext{}).(*knowledge.GitCredential)
	authCtx, cleanup, err := authenticatedGitContext(ctx, strings.TrimSpace(remote), options, credential, sshIP)
	if err != nil {
		return false, err
	}
	defer cleanup()
	if err := privateCacheDir(dir); err != nil {
		return false, err
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr != nil {
		if _, _, _, _, _, err := Sync(authCtx, dir, strings.TrimSpace(remote), baseBranch, nil); err != nil {
			return false, err
		}
	}
	return remoteContainsCommit(authCtx, dir, baseBranch, commit)
}

func remoteGitOptions(ctx context.Context, remote string) ([]string, string, error) {
	u, err := gitremote.Parse(remote)
	if err != nil {
		return nil, "", errors.New("public Git URL required")
	}
	options, err := publicOptions(ctx, u.String(), net.DefaultResolver.LookupIPAddr)
	if err != nil {
		return nil, "", err
	}
	sshIP := ""
	if u.Scheme == "https" {
		command := exec.CommandContext(ctx, "git", "help", "--config")
		command.Env = protectedGitEnv()
		capabilities, err := command.Output()
		if err != nil || !strings.Contains(strings.ToLower(string(capabilities)), "http.curloptresolve") {
			return nil, "", errors.New("Git with http.curloptResolve support is required")
		}
	} else {
		// publicOptions carries the validated address. HostName prevents a second
		// DNS resolution inside SSH after validation.
		sshIP = strings.TrimPrefix(options[len(options)-1], "ssh.pinnedIP=")
		options = options[:len(options)-1]
	}
	return options, sshIP, nil
}

func publicOptions(ctx context.Context, remote string, lookup func(context.Context, string) ([]net.IPAddr, error)) ([]string, error) {
	u, err := gitremote.Parse(remote)
	if err != nil {
		return nil, errors.New("public Git URL required")
	}
	addresses, err := gitremote.PublicAddresses(ctx, u.Hostname(), lookup)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("cannot resolve public Git host")
	}
	ips := []string{}
	for _, a := range addresses {
		ip := a.IP
		if !gitremote.IsPublicIP(ip) {
			return nil, errors.New("Git host must resolve only to public addresses")
		}
		v := ip.String()
		if strings.Contains(v, ":") {
			v = "[" + v + "]"
		}
		ips = append(ips, v)
	}
	if u.Scheme == "ssh" {
		return []string{"protocol.allow=never", "protocol.ssh.allow=always", "ssh.pinnedIP=" + addresses[0].IP.String()}, nil
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return []string{"http.followRedirects=false", "http.proxy=", "http.sslVerify=true", "protocol.allow=never", "protocol.https.allow=always", "http.curloptResolve=" + u.Hostname() + ":" + port + ":" + strings.Join(ips, ",")}, nil
}

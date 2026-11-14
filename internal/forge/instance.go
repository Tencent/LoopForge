// Package forge implements the Forgejo API.
package forge

import (
	"errors"
	"net/url"
	"os"
	"slices"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

const InstancesEnv = "AGENTFLOW_FORGE_INSTANCES"

var ErrInstanceNotAllowed = errors.New("此 Forgejo 实例尚未由管理员启用")

// Instances lists the deployment's trusted HTTPS roots, including any mount
// path. An empty setting enables no outbound Forge API requests.
func Instances() []string {
	out := []string{}
	for _, raw := range strings.Split(os.Getenv(InstancesEnv), ",") {
		if value, ok := domain.NormalizeForgeInstance(raw); ok && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

func Allows(instance string) bool { return slices.Contains(Instances(), instance) }

// BindRepository pairs an SSH clone with the HTTPS repository on the same
// host. The SSH port may differ from the HTTPS port.
func BindRepository(webURL, cloneURL string) (domain.ForgeRepository, gitwork.SSHRemote, error) {
	r, ok := domain.ParseForgeRepositoryURL(webURL)
	if !ok {
		return r, gitwork.SSHRemote{}, errors.New("请输入无凭据的 Forgejo HTTPS 仓库地址")
	}
	if !Allows(r.Instance) {
		return r, gitwork.SSHRemote{}, ErrInstanceNotAllowed
	}
	remote, err := gitwork.ParseSSHRemote(cloneURL)
	if err != nil {
		return r, remote, err
	}
	u, _ := url.Parse(r.Instance)
	if !strings.EqualFold(u.Hostname(), remote.Host) ||
		!strings.EqualFold(r.Path(), strings.TrimSuffix(strings.TrimPrefix(remote.Path, "/"), ".git")) {
		return r, remote, errors.New("Forgejo 的 SSH 地址与 HTTPS 仓库地址必须指向同一主机和仓库")
	}
	return r, remote, nil
}

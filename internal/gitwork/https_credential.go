package gitwork

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
)

const HTTPSCredentialTokenEnv = "AGENTFLOW_SCOPED_GIT_TOKEN"

type HTTPSCredential struct {
	BaseURL string
	Token   string
	resolve string
}

func (c HTTPSCredential) Check(remote string) error {
	if !c.Matches(remote) {
		return fmt.Errorf("仓库地址与当前执行账号配置的 GitLab 实例不匹配")
	}
	if c.Token == "" {
		return fmt.Errorf("当前执行账号未配置 GitLab PAT，请在个人配置中添加")
	}
	return nil
}

// Matches scopes a credential to one HTTPS origin and, for a GitLab instance
// installed under a relative URL, to its path prefix.
func (c HTTPSCredential) Matches(remote string) bool {
	base, err := url.Parse(c.BaseURL)
	if err != nil || !validHTTPSCredentialURL(base) {
		return false
	}
	u, err := url.Parse(remote)
	return err == nil && validHTTPSCredentialURL(u) && strings.EqualFold(u.Hostname(), base.Hostname()) && httpsPort(u) == httpsPort(base) &&
		strings.HasPrefix(u.Path, strings.TrimRight(base.Path, "/")+"/")
}

func httpsPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	return "443"
}

func validHTTPSCredentialURL(u *url.URL) bool {
	return u.Scheme == "https" && u.Hostname() != "" && !strings.ContainsAny(u.Host, "*?%") && !strings.HasSuffix(u.Host, ":") && u.User == nil &&
		!u.ForceQuery && u.RawQuery == "" && u.Fragment == "" && u.RawPath == "" &&
		(u.Path == "" || u.Path == "/" || path.Clean(u.Path) == strings.TrimSuffix(u.Path, "/"))
}

type httpsCredentialKey struct{}

func WithHTTPSCredential(ctx context.Context, credential HTTPSCredential) context.Context {
	return context.WithValue(ctx, httpsCredentialKey{}, credential)
}

func WithoutHTTPSCredential(ctx context.Context) context.Context {
	return context.WithValue(ctx, httpsCredentialKey{}, false)
}

func HTTPSCredentialFrom(ctx context.Context) (HTTPSCredential, bool) {
	if ctx == nil {
		return HTTPSCredential{}, false
	}
	credential, ok := ctx.Value(httpsCredentialKey{}).(HTTPSCredential)
	return credential, ok
}

func pinHTTPSGitDestination(ctx context.Context, args []string) (context.Context, error) {
	c, ok := HTTPSCredentialFrom(ctx)
	if !ok || c.Token == "" {
		return ctx, nil
	}
	if len(args) >= 2 && args[0] == "-C" {
		args = args[2:]
	}
	if len(args) == 0 {
		return ctx, nil
	}
	switch args[0] {
	case "clone", "fetch", "pull", "push", "ls-remote":
	default:
		return ctx, nil
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || !validHTTPSCredentialURL(u) {
		return nil, fmt.Errorf("GitLab 实例地址无效")
	}
	host := u.Hostname()
	ips, err := ResolveHTTPSDestination(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("无法连接 GitLab: %w", err)
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return ctx, nil // A literal address has already been validated.
	}
	addresses := make([]string, 0, len(ips))
	for _, ip := range ips {
		ip = ip.Unmap()
		if ip.Is6() {
			addresses = append(addresses, "["+ip.String()+"]")
		} else {
			addresses = append(addresses, ip.String())
		}
	}
	c.resolve = host + ":" + httpsPort(u) + ":" + strings.Join(addresses, ",")
	return WithHTTPSCredential(ctx, c), nil
}

// BindHTTPSCredential uses Git's URL-scoped configuration and only serves
// credentials on 'get'. The PAT stays in the child environment and out of
// Git's command-line arguments, remotes and credential stores.
func BindHTTPSCredential(env map[string]string, c HTTPSCredential) {
	base := strings.TrimRight(c.BaseURL, "/")
	if c.Token == "" || !c.Matches(base+"/") {
		return
	}
	env[HTTPSCredentialTokenEnv] = c.Token
	values := [][2]string{
		{"credential." + base + ".helper", ""},
		{"credential." + base + ".helper", `!f() { test "$1" = get || exit 0; printf 'username=private\npassword=%s\n' "$AGENTFLOW_SCOPED_GIT_TOKEN"; }; f`},
		{"http." + base + ".followRedirects", "false"},
		{"http." + base + ".proxy", ""},
	}
	if c.resolve != "" {
		values = append(values, [2]string{"http.curloptResolve", c.resolve})
	}
	count, _ := strconv.Atoi(env["GIT_CONFIG_COUNT"])
	for _, entry := range values {
		env[fmt.Sprintf("GIT_CONFIG_KEY_%d", count)] = entry[0]
		env[fmt.Sprintf("GIT_CONFIG_VALUE_%d", count)] = entry[1]
		count++
	}
	env["GIT_CONFIG_COUNT"] = strconv.Itoa(count)
}

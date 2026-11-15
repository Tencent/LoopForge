package gitwork

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// CNBHTTPUser is the Basic-auth user for CNB access tokens.
const CNBHTTPUser = "cnb"

// cnbBaseURL is a variable so tests can point it at a local server.
var cnbBaseURL = "https://cnb.cool"

// HTTPSCredential is one host-scoped Basic-auth pair. It is only ever offered
// to the host named by BaseURL.
type HTTPSCredential struct {
	BaseURL  string
	Username string
	Token    string
}

// CNBCredential is the credential for cnb.cool.
func CNBCredential(token string) HTTPSCredential {
	return HTTPSCredential{BaseURL: cnbBaseURL, Username: CNBHTTPUser, Token: strings.TrimSpace(token)}
}

const (
	scopedTokenEnvPrefix = "AGENTFLOW_SCOPED_GIT_TOKEN_"
	scopedUserEnvPrefix  = "AGENTFLOW_SCOPED_GIT_USER_"
)

type httpsCredentialsCtxKey struct{}

// WithHTTPSCredentials attaches the host-scoped credentials Run offers.
func WithHTTPSCredentials(ctx context.Context, credentials []HTTPSCredential) context.Context {
	if len(credentials) == 0 {
		return ctx
	}
	return context.WithValue(ctx, httpsCredentialsCtxKey{}, credentials)
}

// HTTPSCredentialsFrom returns the credentials attached with WithHTTPSCredentials.
func HTTPSCredentialsFrom(ctx context.Context) []HTTPSCredential {
	if ctx == nil {
		return nil
	}
	credentials, _ := ctx.Value(httpsCredentialsCtxKey{}).([]HTTPSCredential)
	return credentials
}

func redactionTokens(ctx context.Context) []string {
	tokens := []string{TokenFrom(ctx)}
	for _, credential := range HTTPSCredentialsFrom(ctx) {
		tokens = append(tokens, credential.Token)
	}
	return tokens
}

// managedHostBases are the hosts that only ever receive their own scoped
// credential. Every git command carries their config block whether or not a
// credential exists, so a missing token fails closed instead of falling back
// to the global git token or an inherited helper.
func managedHostBases() []string { return []string{cnbBaseURL} }

func managedHost() string {
	u, err := url.Parse(cnbBaseURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// IsManagedRemote reports whether remote lives on a host with scoped credentials.
func IsManagedRemote(remote string) bool {
	remote = strings.TrimSpace(remote)
	if strings.HasPrefix(remote, "git@") {
		host, _, _ := strings.Cut(strings.TrimPrefix(remote, "git@"), ":")
		return strings.EqualFold(host, managedHost())
	}
	u, err := url.Parse(remote)
	return err == nil && strings.EqualFold(u.Hostname(), managedHost())
}

// ManagedHostPromptGlob matches a git credential prompt for a managed host.
func ManagedHostPromptGlob() string { return "*" + managedHost() + "*" }

func scopedHelper(index int) string {
	token := scopedTokenEnvPrefix + strconv.Itoa(index)
	user := scopedUserEnvPrefix + strconv.Itoa(index)
	return `!f() { test "$1" = get || exit 0; if [ -n "$` + token + `" ]; then printf 'username=%s\npassword=%s\n' "$` + user + `" "$` + token + `"; else printf 'quit=1\n'; fi; }; f`
}

// managedHostConfig is the git config of every managed host as ordered
// key/value pairs. The empty helper clears every helper accumulated before it
// (system, global, repository and earlier -c), so it must come after them.
func managedHostConfig() [][2]string {
	var entries [][2]string
	for i, base := range managedHostBases() {
		entries = append(entries,
			[2]string{"credential." + base + ".helper", ""},
			[2]string{"credential." + base + ".helper", scopedHelper(i)},
			[2]string{"http." + base + ".followRedirects", "false"},
		)
	}
	return entries
}

// ManagedHostArgs are `git -c` arguments for managedHostConfig. They must be
// placed after AuthConfigArgs so they clear the global helper it registers.
func ManagedHostArgs() []string {
	var args []string
	for _, entry := range managedHostConfig() {
		args = append(args, "-c", entry[0]+"="+entry[1])
	}
	return args
}

// ManagedHostEnv carries the scoped tokens for one git process. The tokens
// stay out of helper text, argv, remotes and any credential store.
func ManagedHostEnv(credentials []HTTPSCredential) map[string]string {
	env := map[string]string{}
	for i, base := range managedHostBases() {
		for _, credential := range credentials {
			if credential.BaseURL == base && strings.TrimSpace(credential.Token) != "" {
				env[scopedTokenEnvPrefix+strconv.Itoa(i)] = strings.TrimSpace(credential.Token)
				env[scopedUserEnvPrefix+strconv.Itoa(i)] = credential.Username
				break
			}
		}
	}
	return env
}

// BindManagedHosts adds managedHostConfig to environment through
// GIT_CONFIG_COUNT, after any entries already there, plus the scoped tokens.
func BindManagedHosts(environment map[string]string, credentials []HTTPSCredential) {
	count, _ := strconv.Atoi(environment["GIT_CONFIG_COUNT"])
	for _, entry := range managedHostConfig() {
		environment[fmt.Sprintf("GIT_CONFIG_KEY_%d", count)] = entry[0]
		environment[fmt.Sprintf("GIT_CONFIG_VALUE_%d", count)] = entry[1]
		count++
	}
	environment["GIT_CONFIG_COUNT"] = strconv.Itoa(count)
	for key, value := range ManagedHostEnv(credentials) {
		environment[key] = value
	}
}

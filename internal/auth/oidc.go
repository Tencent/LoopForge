package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	OIDCStartPath    = "/api/auth/oidc/start"
	OIDCCallbackPath = "/api/auth/oidc/callback"
)

type OIDCClient struct {
	issuer     string
	redirect   *url.URL
	oauth      oauth2.Config
	authStyle  oauth2.AuthStyle
	httpClient *http.Client
	mu         sync.Mutex
	endpoints  *oidcEndpoints
}

type oidcEndpoints struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewOIDCClientFromEnv leaves enterprise login disabled when no OIDC settings are present.
func NewOIDCClientFromEnv() (*OIDCClient, error) {
	issuer := strings.TrimSpace(os.Getenv("AGENTFLOW_OIDC_ISSUER"))
	clientID := strings.TrimSpace(os.Getenv("AGENTFLOW_OIDC_CLIENT_ID"))
	secret := os.Getenv("AGENTFLOW_OIDC_CLIENT_SECRET")
	callback := strings.TrimSpace(os.Getenv("AGENTFLOW_OIDC_REDIRECT_URL"))
	method := strings.TrimSpace(os.Getenv("AGENTFLOW_OIDC_TOKEN_AUTH_METHOD"))
	if issuer == "" && clientID == "" && secret == "" && callback == "" && method == "" {
		return nil, nil
	}
	if !validOIDCURL(issuer) || len(issuer) > 512 || clientID == "" || secret == "" || !validOIDCURL(callback) {
		return nil, errors.New("OIDC requires a valid issuer, client ID, client secret and redirect URL")
	}
	redirect, _ := url.Parse(callback)
	if redirect.EscapedPath() != OIDCCallbackPath {
		return nil, errors.New("OIDC redirect URL must end in " + OIDCCallbackPath)
	}
	style := oauth2.AuthStyleInHeader
	switch method {
	case "", "client_secret_basic":
	case "client_secret_post":
		style = oauth2.AuthStyleInParams
	default:
		return nil, errors.New("unsupported OIDC token authentication method")
	}
	return &OIDCClient{
		issuer: issuer, redirect: redirect, authStyle: style,
		oauth: oauth2.Config{ClientID: clientID, ClientSecret: secret, RedirectURL: callback, Scopes: []string{oidc.ScopeOpenID}},
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func validOIDCURL(raw string) bool {
	return !strings.ContainsAny(raw, "?#") && validOIDCEndpoint(raw, true)
}

func validOIDCEndpoint(raw string, allowLoopbackHTTP bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || strings.Contains(raw, "#") {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return allowLoopbackHTTP && u.Scheme == "http" && (strings.EqualFold(u.Hostname(), "localhost") || ip.IsLoopback())
}

func (c *OIDCClient) Origin() string {
	return c.redirect.Scheme + "://" + c.redirect.Host
}

func (c *OIDCClient) MatchesHost(host string) bool {
	return c != nil && strings.EqualFold(c.redirect.Host, host)
}

func (c *OIDCClient) Secure() bool {
	return c.redirect.Scheme == "https"
}

func (c *OIDCClient) discover(ctx context.Context) (*oidcEndpoints, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.endpoints != nil {
		return c.endpoints, nil
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, c.httpClient), c.issuer)
	if err != nil {
		return nil, err
	}
	config := c.oauth
	config.Endpoint = provider.Endpoint()
	config.Endpoint.AuthStyle = c.authStyle
	var metadata struct {
		JWKSURL string `json:"jwks_uri"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return nil, err
	}
	for _, endpoint := range []string{config.Endpoint.AuthURL, config.Endpoint.TokenURL, metadata.JWKSURL} {
		if !validOIDCEndpoint(endpoint, strings.HasPrefix(c.issuer, "http://")) {
			return nil, errors.New("OIDC discovery returned an invalid endpoint")
		}
	}
	c.endpoints = &oidcEndpoints{oauth: config, verifier: provider.Verifier(&oidc.Config{ClientID: config.ClientID})}
	return c.endpoints, nil
}

func (c *OIDCClient) AuthorizationURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	endpoints, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	return endpoints.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

func (c *OIDCClient) Exchange(ctx context.Context, code, nonce, verifier string) (issuer, subject string, err error) {
	endpoints, err := c.discover(ctx)
	if err != nil {
		return "", "", err
	}
	token, err := endpoints.oauth.Exchange(oidc.ClientContext(ctx, c.httpClient), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", "", err
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		return "", "", errors.New("OIDC response did not include an ID token")
	}
	idToken, err := endpoints.verifier.Verify(ctx, raw)
	if err != nil {
		return "", "", err
	}
	var claims struct {
		AuthorizedParty string `json:"azp"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return "", "", err
	}
	if idToken.Issuer != c.issuer || idToken.Nonce != nonce || idToken.Subject == "" || len(idToken.Subject) > 255 ||
		len(idToken.Audience) != 1 || (claims.AuthorizedParty != "" && claims.AuthorizedParty != c.oauth.ClientID) {
		return "", "", errors.New("OIDC ID token identity or login transaction did not match")
	}
	return idToken.Issuer, idToken.Subject, nil
}

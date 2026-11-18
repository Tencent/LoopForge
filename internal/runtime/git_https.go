package runtime

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// GitHTTPSToWire converts credentials for a runner task payload.
func GitHTTPSToWire(credentials []gitwork.HTTPSCredential) []domain.GitHTTPSCredential {
	var wire []domain.GitHTTPSCredential
	for _, c := range credentials {
		wire = append(wire, domain.GitHTTPSCredential{BaseURL: c.BaseURL, Username: c.Username, Token: c.Token})
	}
	return wire
}

// GitHTTPSFromWire is the inverse of GitHTTPSToWire.
func GitHTTPSFromWire(wire []domain.GitHTTPSCredential) []gitwork.HTTPSCredential {
	var credentials []gitwork.HTTPSCredential
	for _, c := range wire {
		credentials = append(credentials, gitwork.HTTPSCredential{BaseURL: c.BaseURL, Username: c.Username, Token: c.Token})
	}
	return credentials
}

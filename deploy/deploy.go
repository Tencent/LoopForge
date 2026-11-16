// Package deploy embeds machine-facing deployment assets so the server can
// serve them directly, e.g. curl -fsSL https://host/runners/install.sh.
package deploy

import (
	"bytes"
	"embed"
)

//go:embed bind-cli.sh
var Files embed.FS

// strictHTTPSAssignment is the single line the platform rewrites before serving
// the installer. Keep it identical to deploy/bind-cli.sh.
const (
	strictHTTPSOff = "STRICT_HTTPS=0\n"
	strictHTTPSOn  = "STRICT_HTTPS=1\n"
)

// BindCLIScript returns the bind-cli.sh installer content.
func BindCLIScript() []byte {
	data, err := Files.ReadFile("bind-cli.sh")
	if err != nil {
		return nil
	}
	return data
}

// BindCLIScriptFor returns the installer with the strict-HTTPS default set for
// the scheme the request arrived on: a script fetched over HTTPS must never
// fall back to plain HTTP, while an intranet install fetched over a bare IP
// still may. bind-cli.sh keeps --allow-http so such an installation can opt
// out of the strict default.
func BindCLIScriptFor(strictHTTPS bool) []byte {
	data := BindCLIScript()
	if !strictHTTPS || len(data) == 0 {
		return data
	}
	if !bytes.Contains(data, []byte(strictHTTPSOff)) {
		return data
	}
	return bytes.Replace(data, []byte(strictHTTPSOff), []byte(strictHTTPSOn), 1)
}

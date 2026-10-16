// Package deploy embeds machine-facing deployment assets so the server can
// serve them directly, e.g. curl -fsSL https://host/runners/install.sh.
package deploy

import "embed"

//go:embed bind-cli.sh
var Files embed.FS

// BindCLIScript returns the bind-cli.sh installer content.
func BindCLIScript() []byte {
	data, err := Files.ReadFile("bind-cli.sh")
	if err != nil {
		return nil
	}
	return data
}

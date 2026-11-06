package runtime

import _ "embed"

// IsolatedDockerfile is the public generic-backend recipe. scripts/install.sh
// prebuilds the same file under the runtime cache tag before the platform
// starts, and migrations/seed/001_public_catalog.sql stores the same text.
//
//go:embed sandbox.Dockerfile
var IsolatedDockerfile string

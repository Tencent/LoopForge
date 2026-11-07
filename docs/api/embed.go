package apidocs

import _ "embed"

// OpenAPI is the public AgentFlow OpenAPI 3.1 contract.
//
//go:embed openapi.yaml
var OpenAPI []byte

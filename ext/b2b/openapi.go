package b2b

import _ "embed"

//go:embed openapi.json
var openapiFragment []byte

// OpenAPI contributes this module's routes to the store's contract.
func (m *Module) OpenAPI() []byte { return openapiFragment }

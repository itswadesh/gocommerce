package mcp

import _ "embed"

// The module's slice of the API contract, merged into the served document at
// startup so /doc describes every route this store actually answers — and so
// doctor's contract check stays quiet when mcp is installed.
//
//go:embed openapi.json
var openapiFragment []byte

// OpenAPI implements gocommerce.OpenAPIContributor.
func (m *Module) OpenAPI() []byte { return openapiFragment }

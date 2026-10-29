package catalogai

import _ "embed"

//go:embed openapi.json
var openapiFragment []byte

// OpenAPI implements gocommerce.OpenAPIContributor.
func (m *Module) OpenAPI() []byte { return openapiFragment }

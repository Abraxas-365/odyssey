// Package api holds Odyssey's HTTP contract (openapi.yaml, OpenAPI 3.1,
// written by hand). The contract test in internal/bootstrap checks the
// server against it; the console's TypeScript types are generated from it.
package api

import _ "embed"

// OpenAPI is the raw document.
//
//go:embed openapi.yaml
var OpenAPI []byte

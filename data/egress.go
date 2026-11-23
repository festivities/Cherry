package data

import _ "embed"

// EgressJSON is the static redirect switch (internal/assets/egress.go): off by default, it answers streamed original
// files with a redirect to the public copy of the R2 bucket instead of sending the bytes.
//
//go:embed egress.json
var EgressJSON []byte

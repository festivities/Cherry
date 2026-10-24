// Package data embeds Cherry's own committed data files (go:embed cannot reach
// outside the package directory, so the embedding lives next to the data).
package data

import _ "embed"

// FaceCodes is the Face Shop catalog: whitespace-separated face item codes.
//
//go:embed face_codes.txt
var FaceCodes string

// AtelierJSON is the default Garden Atelier recipe file (economy/atelier.go). An operator file in
// %LOCALAPPDATA%\Cherry\config\atelier.json replaces it entirely.
//
//go:embed atelier.json
var AtelierJSON []byte

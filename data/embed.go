// Package data embeds Cherry's own committed data files (go:embed cannot reach
// outside the package directory, so the embedding lives next to the data).
package data

import _ "embed"

// FaceCodes is the Face Shop catalog: whitespace-separated face item codes.
//
//go:embed face_codes.txt
var FaceCodes string

// The operator-editable defaults (AGENTS "Operator-editable fabricated content"). Each is read by
// the economy package (or, for profiles, the web package) through internal/opdata. An operator file
// with the same name in %LOCALAPPDATA%\Cherry\config replaces the default entirely.

// AtelierJSON is the Garden Atelier recipe file (economy/atelier.go).
//
//go:embed atelier.json
var AtelierJSON []byte

// PricesJSON is the shop price rules, sell-back and deals, Face Shop tickets and item names.
//
//go:embed prices.json
var PricesJSON []byte

// GachaJSON is the gacha: price, odds, pool and duplicate refunds.
//
//go:embed gacha.json
var GachaJSON []byte

// GatheringJSON is the Garden gathering: drops, caps and rewards.
//
//go:embed gathering.json
var GatheringJSON []byte

// FishingJSON is the Square fishing: fish book, daily cap, rare chance and fish shop.
//
//go:embed fishing.json
var FishingJSON []byte

// CalendarsJSON is the normal 7-day login calendar and the seasonal calendars.
//
//go:embed calendars.json
var CalendarsJSON []byte

// RewardsJSON is the welcome gift, daily quests, hearts reward and mailbox rules.
//
//go:embed rewards.json
var RewardsJSON []byte

// ProfilesJSON is the synthetic clone-avatar profiles the create flow asks for.
//
//go:embed profiles.json
var ProfilesJSON []byte

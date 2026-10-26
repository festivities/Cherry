// Package data embeds Cherry's own committed data files (go:embed cannot reach
// outside the package directory, so the embedding lives next to the data).
package data

import "embed"

// FaceCodes is the Face Shop catalog: whitespace-separated face item codes.
//
//go:embed face_codes.txt
var FaceCodes string

// The operator-editable defaults (AGENTS "Operator-editable fabricated content"). Each is read by
// the economy package (or, for profiles, the web package) through internal/opdata. An operator file
// with the same name in %LOCALAPPDATA%\Cherry\config replaces the default entirely.

// RacingJSON is the Square Riding Pet Race: tickets, timings, track, grades and rewards (economy/racing.go).
//
//go:embed racing.json
var RacingJSON []byte

// RacingExchangeJSON is the Square Ride Point exchange shop: rows and the sale period (economy/racing_exchange.go).
//
//go:embed racing_exchange.json
var RacingExchangeJSON []byte

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

// FishbossJSON is the Square boss fishing: tries, reset cost, chart, boss and rewards (economy/fishboss.go).
//
//go:embed fishboss.json
var FishbossJSON []byte

// CalendarsJSON is the normal 7-day login calendar and the seasonal calendars.
//
//go:embed calendars.json
var CalendarsJSON []byte

// ExchangeJSON is the Square Boss Coin exchange office: standing rows and seasonal event rows (economy/exchange.go).
//
//go:embed exchange.json
var ExchangeJSON []byte

// RewardsJSON is the welcome gift, daily quests, hearts reward and mailbox rules.
//
//go:embed rewards.json
var RewardsJSON []byte

// ProfilesJSON is the synthetic friend and clone-avatar profiles, and the lab grant list (store/profiles.go).
//
//go:embed profiles.json
var ProfilesJSON []byte

// StarterJSON is the new-account room starter codes and the default pet name (room/starter.go).
//
//go:embed starter.json
var StarterJSON []byte

// DressupJSON is the creation dress-up look sets and the item lists each create tab shows (web/dressup.go).
//
//go:embed dressup.json
var DressupJSON []byte

// MenuJSON is the home menu labels (web/http.go).
//
//go:embed menu.json
var MenuJSON []byte

// SquaresJSON is the Square list: each Square's theme, names, map and spawn (lpn/squares.go).
//
//go:embed squares.json
var SquaresJSON []byte

// GardensJSON is the Garden map list: the default map and the maps an owner may choose (economy/gardens.go).
//
//go:embed gardens.json
var GardensJSON []byte

// MenuIcons is the home menu picture set: one PNG per icon, named by the "icons" map of menu.json and
// served at /cherry/menu/ (web/http.go). Rendered by scripts/render-menu-icons.py.
//
//go:embed menuicons/*.png
var MenuIcons embed.FS

// StreamingJSON is the on-demand asset streaming switch (internal/assets). Streaming also needs
// %LOCALAPPDATA%\Cherry\cloudflare.json; both are off by default.
//
//go:embed streaming.json
var StreamingJSON []byte

// ItemconvJSON is the operator switch for the converted iOS-only items (assets/items.go); default on.
//
//go:embed itemconv.json
var ItemconvJSON []byte

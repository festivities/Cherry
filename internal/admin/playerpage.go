package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// The player page (PLAN Roadmap 8, staff panel phase 5): one player in sections with anchor links (no script): Overview,
// Inventory, Garden bag, Butlers, Social, Moderation, Ledger and Grant. The writing and picture lists are the ones the
// moderation of phase 2b shows; the Garden and economy part is economy.PlayerBookOf, the social part the store's reads.

const (
	friendsPage   = 50 // friends listed on the page (the count is the whole)
	auditOnPlayer = 20 // audit rows of the player on the page
)

// grantKind is one choice of the grant form: a currency, or the Garden material choice ("material"), which takes the item below.
type grantKind struct{ Value, Label string }

// grantKinds are the choices of the grant form, in the order it lists them.
var grantKinds = []grantKind{
	{"gems", "Gems"},
	{"cash", "Cash"},
	{"faceTickets", "Face Shop tickets"},
	{"smiles", "Smiles"},
	{"bossCoins", "Boss Coins"},
	{"ridePoints", "Ride points"},
	{"raceTickets", "Race tickets back (today)"},
	{"material", "Garden material (the item list)"},
}

// ledgerWindow is one choice of the ledger's window: its query value, its span (0 = every line) and its name.
type ledgerWindow struct {
	Key  string
	Span time.Duration
	Name string
}

// ledgerWindows are the windows the player's ledger offers; the first is the default.
var ledgerWindows = []ledgerWindow{
	{"all", 0, "All lines"},
	{"24h", 24 * time.Hour, "24 hours"},
	{"7d", 7 * 24 * time.Hour, "7 days"},
	{"30d", 30 * 24 * time.Hour, "30 days"},
}

// playerSections are the anchors of the player page, in page order.
var playerSections = []link{
	{Href: "#overview", Label: "Overview"},
	{Href: "#inventory", Label: "Inventory"},
	{Href: "#garden-bag", Label: "Garden bag"},
	{Href: "#butlers", Label: "Butlers"},
	{Href: "#social", Label: "Social"},
	{Href: "#past-avatars", Label: "Past avatars"},
	{Href: "#moderation", Label: "Moderation"},
	{Href: "#ledger", Label: "Ledger"},
	{Href: "#grant", Label: "Grant"},
}

// playerPage is what the player page shows beyond the lists of the moderation part (view.Posts and the like).
type playerPage struct {
	Created, Seen string
	Online        *onlineRow // the live connection of the account, nil when it is not online now
	Friends       []link     // the accepted friends: a link to the player page when the friend has an account row
	FriendTotal   int
	Circle        *store.CircleMembership
	Book          economy.PlayerBook
	Sections      []link
	Windows       []link // the ledger's window choices
	WindowName    string
	Audit         []store.AuditRow
	Grants        []grantKind
	Materials     []economy.GatherDrop
	Past          []deletedRow // the deleted avatars of this identity (deleted.go), newest first
}

// ledgerWindowOf reads the ledger window query value. An absent value is the default; an unknown value is refused.
func ledgerWindowOf(key string) (ledgerWindow, bool) {
	if key == "" {
		return ledgerWindows[0], true
	}
	for _, w := range ledgerWindows {
		if w.Key == key {
			return w, true
		}
	}
	return ledgerWindow{}, false
}

// handlePlayer: GET /players/{id}, with ?window= for the ledger.
func (s *Server) handlePlayer(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := s.playerID(w, r)
	if !ok {
		return
	}
	p, found := store.PlayerByID(id)
	if !found {
		s.storeError(w, store.ErrNoPlayer)
		return
	}
	win, ok := ledgerWindowOf(r.URL.Query().Get("window"))
	if !ok {
		s.fail(w, http.StatusBadRequest, "The ledger window is all, 24h, 7d or 30d.")
		return
	}
	now := s.now()
	var since time.Time
	if win.Span > 0 {
		since = now.Add(-win.Span)
	}
	lines, err := store.PlayerLedgerSince(id, since, ledgerPage)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writing, err := store.PlayerWritingOf(id, writingPage)
	if err != nil {
		s.storeError(w, err)
		return
	}
	pictures, err := store.PlayerMedia(id, mediaPage)
	if err != nil {
		s.storeError(w, err)
		return
	}
	created, seen, err := store.PlayerTimes(id)
	if err != nil {
		s.storeError(w, err)
		return
	}
	audit, err := store.PlayerAudit(id, auditOnPlayer)
	if err != nil {
		s.storeError(w, err)
		return
	}
	book, ok := economy.PlayerBookOf(id, economy.NowFunc())
	if !ok {
		s.storeError(w, store.ErrNoPlayer)
		return
	}
	friends, total := store.PlayerFriends(p.Aid, friendsPage)
	named := store.PlayersOfAids(friends)
	circle, inCircle := store.PlayerCircle(p.Aid)
	past, err := store.DeletedAvatarsOfIdentity(p.Identity, pastAvatarsPage)
	if err != nil {
		s.storeError(w, err)
		return
	}

	page := playerPage{Created: rfcDate(created), Seen: rfcDate(seen), Book: book, Sections: playerSections, FriendTotal: total,
		Audit: audit, Grants: grantKinds, Materials: economy.GatherMaterials(), WindowName: win.Name}
	for _, d := range past {
		page.Past = append(page.Past, deletedRowOf(d))
	}
	if inCircle {
		page.Circle = &circle
	}
	if rows := s.onlineAt(now).Rows; p.Aid != "0" {
		for i := range rows {
			if rows[i].Aid == p.Aid {
				page.Online = &rows[i]
				break
			}
		}
	}
	for _, aid := range friends {
		l := link{Label: aid}
		if pl, ok := named[aid]; ok {
			l.Href, l.Label = playerPath(pl.ID), pl.Name
			if l.Label == "" {
				l.Label = aid
			}
		}
		page.Friends = append(page.Friends, l)
	}
	for _, c := range ledgerWindows {
		page.Windows = append(page.Windows, link{Href: fmt.Sprintf("%s?window=%s#ledger", playerPath(id), c.Key), Label: c.Name, Current: c.Key == win.Key})
	}

	v := s.base(r, sess, "Player #"+strconv.FormatInt(id, 10))
	v.Crumbs = []link{{Href: "/players", Label: "Players"}, {Label: "#" + strconv.FormatInt(id, 10)}}
	v.Player, v.Ledger = p, lines
	v.Posts, v.Entries, v.Comments = writingRows(writing)
	v.Media = mediaRows(pictures)
	v.Page = page
	s.render(w, http.StatusOK, "player", v)
}

// grantOf turns the grant form's choice into a grant of delta: the currency's name, or "material" with the Garden material's item id.
func grantOf(kind, item string, delta int64) (economy.StaffGrant, error) {
	var g economy.StaffGrant
	switch kind {
	case "gems":
		g.Gems = delta
	case "cash":
		g.Cash = delta
	case "faceTickets":
		g.FaceTickets = delta
	case "smiles":
		g.Smiles = delta
	case "bossCoins":
		g.BossCoins = delta
	case "ridePoints":
		g.RidePoints = delta
	case "raceTickets":
		g.RaceTickets = delta
	case "material":
		code, err := strconv.ParseInt(item, 10, 64)
		if err != nil {
			return g, fmt.Errorf("%w: pick a Garden material from the list", store.ErrBadInput)
		}
		g.Materials = map[int64]int64{code: delta}
	default:
		return g, fmt.Errorf("%w: the currency is one the grant form lists", store.ErrBadInput)
	}
	return g, nil
}

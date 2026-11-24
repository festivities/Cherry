package admin

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"time"

	"cherry/internal/lpn"
	"cherry/internal/metrics"
	"cherry/internal/store"
)

// The Online now page (PLAN Roadmap 8, staff "Insights"): the live game connections that hold a login (lpn.Online), the players
// per kind of room, and the Kick action. A kick closes the player's connections through lpn.CloseAid after its audit row is
// written (store.KickPlayer); it is not a ban.

// onlineTimeFormat is how a connection's start is shown: UTC, to the second.
const onlineTimeFormat = "2006-01-02 15:04:05"

// onlineRow is one connection on the page. PlayerID is the store row id the kick names (0 when no account has the avatar id);
// PlayerHref is the player's page. Rooms is the rooms the account is in now, "—" for none.
type onlineRow struct {
	PlayerID                 int64
	PlayerHref               string
	Name, Aid                string
	Listener, Remote         string
	CountryName, CountryCode string
	CountryByIP              bool // looked up from the network: the client sent no country
	Since, For               string
	Rooms                    string
}

// placeCount is one line of the "where they are" table: the distinct players in one kind of room.
type placeCount struct {
	Label string
	Count int
}

// onlinePage is what the Online page shows. Session and Gateway are the connection counters (they count the connections that
// have not logged in yet too); Players is the number of distinct players with a login, Total the number of connections with one.
type onlinePage struct {
	Players, Total   int
	Session, Gateway int64
	Places           []placeCount
	Rows             []onlineRow
}

// placeKinds are the kinds of room the page counts, in the order it lists them, with their labels.
var placeKinds = []struct{ kind, label string }{
	{"garden_own", "In own Garden"},
	{"garden_visit", "In a Garden visit"},
	{"square_public", "In a public Square"},
	{"square_private", "In a private Square"},
	{"circle", "In a circle room"},
	{"party", "In a Room Party"},
	{"lounge", "In a Lounge room"},
}

// placeKey maps a Place to its bucket in placeKinds for the player's aid, splitting own vs visited Gardens
// and public vs private Squares.
func placeKey(p lpn.Place, aid string) string {
	switch p.Kind {
	case "garden":
		if aid != "" && p.ID == aid {
			return "garden_own"
		}
		return "garden_visit"
	case "square":
		if p.Secret {
			return "square_private"
		}
		return "square_public"
	}
	return p.Kind
}

// placeText names a room without player context.
func placeText(p lpn.Place) string {
	return placeTextFor(p, "")
}

// placeTextFor names a room the way the page shows it for the player's aid.
func placeTextFor(p lpn.Place, aid string) string {
	switch p.Kind {
	case "garden":
		if aid != "" && p.ID == aid {
			return "Own Garden"
		}
		return "Garden of " + p.ID
	case "square":
		theme, _ := strconv.ParseUint(p.ID, 10, 64)
		name := lpn.SquareName(theme)
		if name == "" {
			name = "Square theme " + p.ID
		}
		if p.Secret {
			return name + " private room " + p.Room
		}
		return name + " room " + p.Room
	case "circle":
		return "Circle " + p.ID
	case "party":
		return "Room Party of " + p.ID
	case "lounge":
		return "Lounge room " + p.ID
	}
	return p.Kind + " " + p.ID
}

// onlineAt builds the page from the live connections at now, deduplicated to one row per player.
// The players are looked up in one pass of the accounts.
func (s *Server) onlineAt(now time.Time) onlinePage {
	conns := s.online()

	type aggConn struct {
		aid      uint64
		listener string
		remote   string
		fallback string
		since    time.Time
		places   []lpn.Place
		seen     map[lpn.Place]bool
	}
	byAid := make(map[uint64]*aggConn, len(conns))
	aggs := make([]*aggConn, 0, len(conns))
	for _, c := range conns {
		agg := byAid[c.Aid]
		if agg == nil {
			agg = &aggConn{
				aid:      c.Aid,
				listener: c.Listener,
				remote:   c.Remote,
				fallback: c.Remote,
				since:    c.Since,
				seen:     map[lpn.Place]bool{},
			}
			byAid[c.Aid] = agg
			aggs = append(aggs, agg)
		} else {
			if c.Since.Before(agg.since) {
				agg.since = c.Since
			}
			if agg.listener != "session" && c.Listener == "session" {
				agg.listener = c.Listener
				agg.remote = c.Remote
			}
			if c.Remote != "" && agg.fallback == "" {
				agg.fallback = c.Remote
			}
		}
		for _, pl := range c.Places {
			if !agg.seen[pl] {
				agg.seen[pl] = true
				agg.places = append(agg.places, pl)
			}
		}
	}
	slices.SortFunc(aggs, func(a, b *aggConn) int {
		if c := a.since.Compare(b.since); c != 0 {
			return c
		}
		return cmp.Compare(a.aid, b.aid)
	})

	aids := make([]string, 0, len(aggs))
	for _, agg := range aggs {
		aids = append(aids, strconv.FormatUint(agg.aid, 10))
	}
	players := store.PlayersOfAids(aids)

	page := onlinePage{
		Players: len(aggs),
		Total:   len(conns),
		Session: metrics.SessionConns.Load(),
		Gateway: metrics.GatewayConns.Load(),
	}
	roomed := map[uint64]bool{} // players with a room now
	inKind := map[string]map[uint64]bool{}
	for i, agg := range aggs {
		remote := agg.remote
		if remote == "" {
			remote = agg.fallback
		}
		row := onlineRow{
			Aid:      aids[i],
			Listener: agg.listener,
			Remote:   remote,
			Since:    agg.since.UTC().Format(onlineTimeFormat),
			For:      humanDuration(now.Sub(agg.since).Truncate(time.Second)),
		}
		if p, ok := players[row.Aid]; ok {
			row.PlayerID, row.PlayerHref, row.Name = p.ID, playerPath(p.ID), p.Name
			code, name, _ := store.CountryInfo(p.Country)
			row.CountryCode, row.CountryName, row.CountryByIP = code, name, p.CountryByIP
		}
		var names []string
		for _, pl := range agg.places {
			names = append(names, placeTextFor(pl, row.Aid))
			roomed[agg.aid] = true
			key := placeKey(pl, row.Aid)
			if inKind[key] == nil {
				inKind[key] = map[uint64]bool{}
			}
			inKind[key][agg.aid] = true
		}
		row.Rooms = joinOrDash(names)
		page.Rows = append(page.Rows, row)
	}
	for _, k := range placeKinds {
		page.Places = append(page.Places, placeCount{Label: k.label, Count: len(inKind[k.kind])})
	}
	page.Places = append(page.Places, placeCount{Label: "In none of those rooms", Count: page.Players - len(roomed)})
	return page
}

// joinOrDash joins the names with ", "; no name is "—".
func joinOrDash(names []string) string {
	if len(names) == 0 {
		return "—"
	}
	out := names[0]
	for _, n := range names[1:] {
		out += ", " + n
	}
	return out
}

// handleOnline: GET /online. The counts, the places and one row per connection.
func (s *Server) handleOnline(w http.ResponseWriter, r *http.Request, sess session) {
	v := s.base(r, sess, "Online now")
	v.Online = s.onlineAt(s.now())
	s.render(w, http.StatusOK, "online", v)
}

// handleKick: POST /players/{id}/kick (privileged). The form: reason. The store checks the reason and writes the audit row
// first; then the account's live connections close. The account is not banned.
func (s *Server) handleKick(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := s.playerID(w, r)
	if !ok {
		return
	}
	aid, err := store.KickPlayer(id, r.PostFormValue("reason"), sess.identity)
	if err != nil {
		s.storeError(w, err)
		return
	}
	s.closeAid(aid)
	redirect(w, http.StatusSeeOther, "/online?notice=kicked")
}

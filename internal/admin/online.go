package admin

import (
	"net/http"
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
	PlayerID         int64
	PlayerHref       string
	Name, Aid        string
	Listener, Remote string
	Since, For       string
	Rooms            string
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
	{"garden", "In a Garden visit"},
	{"square", "In a Square"},
	{"circle", "In a circle room"},
	{"party", "In a Room Party"},
	{"lounge", "In a Lounge room"},
}

// placeText names a room the way the page shows it.
func placeText(p lpn.Place) string {
	switch p.Kind {
	case "garden":
		return "Garden of " + p.ID
	case "square":
		return "Square theme " + p.ID
	case "circle":
		return "Circle " + p.ID
	case "party":
		return "Room Party of " + p.ID
	case "lounge":
		return "Lounge room " + p.ID
	}
	return p.Kind + " " + p.ID
}

// onlineAt builds the page from the live connections at now. The players are looked up in one pass of the accounts.
func (s *Server) onlineAt(now time.Time) onlinePage {
	conns := s.online()
	aids := make([]string, 0, len(conns))
	for _, c := range conns {
		aids = append(aids, strconv.FormatUint(c.Aid, 10))
	}
	players := store.PlayersOfAids(aids)

	page := onlinePage{Total: len(conns), Session: metrics.SessionConns.Load(), Gateway: metrics.GatewayConns.Load()}
	everyone := map[uint64]bool{} // distinct players
	roomed := map[uint64]bool{}   // players with a room now
	inKind := map[string]map[uint64]bool{}
	for _, c := range conns {
		everyone[c.Aid] = true
		row := onlineRow{Aid: strconv.FormatUint(c.Aid, 10), Listener: c.Listener, Remote: c.Remote,
			Since: c.Since.UTC().Format(onlineTimeFormat), For: humanDuration(now.Sub(c.Since).Truncate(time.Second))}
		if p, ok := players[row.Aid]; ok {
			row.PlayerID, row.PlayerHref, row.Name = p.ID, playerPath(p.ID), p.Name
		}
		var names []string
		for _, pl := range c.Places {
			names = append(names, placeText(pl))
			roomed[c.Aid] = true
			if inKind[pl.Kind] == nil {
				inKind[pl.Kind] = map[uint64]bool{}
			}
			inKind[pl.Kind][c.Aid] = true
		}
		row.Rooms = joinOrDash(names)
		page.Rows = append(page.Rows, row)
	}
	page.Players = len(everyone)
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

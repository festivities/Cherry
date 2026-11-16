package lpn

import (
	"cmp"
	"net"
	"slices"
	"strconv"
	"time"

	"cherry/internal/httpx"
)

// The staff Online page (PLAN Roadmap 8, staff "Insights") reads the live game connections that hold a login. It is a
// read-only copy of the registries: each one is locked on its own and copied, and no lock is held while the rows are built.

// Place is a room a logged-in account is in now. Kind is "garden" (ID: the owner's avatar id), "square" (the Square's theme),
// "circle" (the circle id), "party" (the Room Party host's avatar id) or "lounge" (the Lounge room id).
type Place struct {
	Kind, ID string
}

// Connection is one live connection that holds a login: the avatar id, the listener ("session" for :10123, "gateway" for
// :10000), the client's network (httpx.TruncateAddr: its /24 or /48, never the full address), when it was accepted, and the
// rooms the account is in now. A connection that has not logged in has no avatar id and is not listed.
type Connection struct {
	Aid      uint64
	Listener string
	Remote   string
	Since    time.Time
	Places   []Place
}

// Online returns the connections that hold a login, oldest first.
func Online() []Connection {
	type bound struct {
		aid uint64
		c   *lockedConn
	}
	var binds []bound
	sessionConnsMu.Lock()
	for aid, c := range sessionConns {
		binds = append(binds, bound{aid, c})
	}
	sessionConnsMu.Unlock()
	gatewayLoginsMu.Lock()
	for c, aids := range gatewayLogins {
		for _, aid := range aids {
			binds = append(binds, bound{aid, c})
		}
	}
	gatewayLoginsMu.Unlock()

	places := placesByAid()
	out := make([]Connection, 0, len(binds))
	for _, b := range binds {
		// listener, remote and since never change after the connection is accepted, so they need no lock.
		out = append(out, Connection{Aid: b.aid, Listener: b.c.listener, Remote: remoteNetwork(b.c.remote), Since: b.c.since, Places: places[b.aid]})
	}
	slices.SortFunc(out, func(a, b Connection) int {
		if c := a.Since.Compare(b.Since); c != 0 {
			return c
		}
		return cmp.Compare(a.Aid, b.Aid)
	})
	return out
}

// placesByAid lists the rooms of every account that has one, from the room registries. Each registry is read under its own
// lock; a room's member list under the room's lock.
func placesByAid() map[uint64][]Place {
	out := map[uint64][]Place{}
	add := func(aid uint64, kind string, id string) {
		out[aid] = append(out[aid], Place{Kind: kind, ID: id})
	}

	gardensMu.Lock()
	for aid, v := range gardenByAid {
		add(aid, "garden", strconv.FormatUint(v.owner, 10))
	}
	gardensMu.Unlock()

	defaultSquares.mu.Lock()
	themes := make(map[uint64]*squareRoom, len(defaultSquares.rooms))
	for theme, r := range defaultSquares.rooms {
		themes[theme] = r
	}
	defaultSquares.mu.Unlock()
	for theme, r := range themes {
		r.mu.Lock()
		for _, p := range r.players {
			add(p.aid, "square", strconv.FormatUint(theme, 10))
		}
		r.mu.Unlock()
	}

	defaultCircleRooms.mu.Lock()
	ids := make([]int64, 0, len(defaultCircleRooms.rooms))
	for id := range defaultCircleRooms.rooms {
		ids = append(ids, id)
	}
	defaultCircleRooms.mu.Unlock()
	for _, id := range ids {
		for aid := range defaultCircleRooms.presence(id) {
			add(aid, "circle", strconv.FormatInt(id, 10))
		}
	}

	defaultParty.mu.Lock()
	for host, r := range defaultParty.rooms {
		for _, m := range r.members {
			add(m.aid, "party", strconv.FormatUint(host, 10))
		}
	}
	defaultParty.mu.Unlock()

	loungeMu.Lock()
	for aid, roomID := range loungeIn {
		add(aid, "lounge", strconv.FormatInt(roomID, 10))
	}
	loungeMu.Unlock()
	return out
}

// remoteNetwork is the client's address without its port, masked to its network (httpx.TruncateAddr). "" for an address
// that is not one (the test harness's "test").
func remoteNetwork(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	return httpx.TruncateAddr(host)
}

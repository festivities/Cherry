package lpn

import (
	"slices"
	"strconv"
	"sync"

	"cherry/internal/store"
)

// Gateway logins. Every agent's login request names a session key (protobuf field 1) and an aid (field 2). The stock
// client sends the key its createSession returned, the same value its sendInit names on :10123 (AGENTS, "Protocol
// stacks"), so a login is accepted only when the account with that aid holds that key. A verified login binds the
// agent's connection state to that aid, replacing an earlier one as sendInit does (a Delete Avatar followed by a new
// avatar on the same socket); a login that fails the check changes nothing, and no other message changes the aid.
// TLUP's ClientSession carries the same two fields and is checked the same way on every heart event.

// sessionKeyHolds reports whether the account with aid holds sessionKey. The compare is constant-time, and a migrated
// account's first derived-key login drops its legacy key (store.HoldsSessionKeyLocked).
func sessionKeyHolds(aid uint64, sessionKey []byte) bool {
	if aid == 0 || aid > (1<<63)-1 || len(sessionKey) == 0 {
		return false
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return store.HoldsSessionKeyLocked(store.AccountByAidLocked(strconv.FormatUint(aid, 10)), sessionKey)
}

// verifiedLogin is the check every gateway agent shares: the request is well formed with a non-empty session key and a
// positive aid (gardenLoginValid), and the account with that aid holds the key. It returns the aid to bind.
func verifiedLogin(data []byte) (uint64, bool) {
	if !gardenLoginValid(data) {
		return 0, false
	}
	aid, _ := protobufUintField(data, 2)
	key, _, _ := pbBytesField(data, 1)
	return aid, sessionKeyHolds(aid, key)
}

// gatewayLogins lists the aids each gateway connection verified a login for. Every gateway agent calls bindGatewayLogin
// after its verifiedLogin succeeds, and observeGatewayFrames unbinds the connection when it ends. CloseAid reads it.
var (
	gatewayLoginsMu sync.Mutex
	gatewayLogins   = map[*lockedConn][]uint64{}
)

// bindGatewayLogin records that conn verified a login for aid.
func bindGatewayLogin(conn *lockedConn, aid uint64) {
	gatewayLoginsMu.Lock()
	defer gatewayLoginsMu.Unlock()
	if !slices.Contains(gatewayLogins[conn], aid) {
		gatewayLogins[conn] = append(gatewayLogins[conn], aid)
	}
}

// unbindGatewayLogins forgets conn when its gateway connection ends.
func unbindGatewayLogins(conn *lockedConn) {
	gatewayLoginsMu.Lock()
	defer gatewayLoginsMu.Unlock()
	delete(gatewayLogins, conn)
}

// CloseAid ends every live connection of the account with avatar id aid: its :10123 session and each gateway
// connection that verified a login for it. Closing a connection makes its own goroutine run the usual teardown (unbind,
// leave); this function does not wait for that and holds no lock while it closes. It is the store's OnRevoke hook (cmd/cherry)
// and the staff Kick (the account is not banned: it can sign in again at once).
func CloseAid(aid string) {
	n, err := strconv.ParseUint(aid, 10, 64)
	if err != nil || n == 0 {
		return
	}
	var conns []*lockedConn
	sessionConnsMu.Lock()
	if c := sessionConns[n]; c != nil {
		conns = append(conns, c)
	}
	sessionConnsMu.Unlock()
	gatewayLoginsMu.Lock()
	for c, aids := range gatewayLogins {
		if slices.Contains(aids, n) {
			conns = append(conns, c)
		}
	}
	gatewayLoginsMu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// kickPayload is the 0x1F frame's JSON. parseBan reads these keys without requiring any; the client shows nothing of them.
const kickPayload = `{"message":"","penaltyCode":"","penaltyStartDate":"0","penaltyEndDate":"0","penaltyDay":0}`

// KickAid tells the account's older device that it signed in elsewhere, then ends its live connections (CloseAid). The
// 0x1F push makes the stock client drop its session socket and queue error 2002, whose popup is string 10066 ("The same
// account has been used on another device"). It is the store's OnTakeover hook (cmd/cherry).
// The static reading said 10066 also needed a maintenance-type push to become visible (duplogin-spec.md section 1); the
// owner saw the popup on both Android and iOS with 0x1F alone (2026-11-22), so none is sent.
func KickAid(aid string) {
	if n, err := strconv.ParseUint(aid, 10, 64); err == nil && n != 0 {
		_, _ = sessionPush(n, loOpKick, []byte(kickPayload))
	}
	CloseAid(aid)
}

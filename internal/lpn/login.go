package lpn

import (
	"crypto/subtle"
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

// sessionKeyHolds reports whether the account with aid holds sessionKey. The compare is constant-time.
func sessionKeyHolds(aid uint64, sessionKey []byte) bool {
	if aid == 0 || aid > (1<<63)-1 || len(sessionKey) == 0 {
		return false
	}
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked(strconv.FormatUint(aid, 10))
	var held []byte
	if acc != nil {
		held = []byte(acc.SessionKey)
	}
	store.AccountsMu.Unlock()
	return acc != nil && subtle.ConstantTimeCompare(held, sessionKey) == 1
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
// after its verifiedLogin succeeds, and observeGatewayFrames unbinds the connection when it ends. CloseBannedAid reads it.
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

// CloseBannedAid ends every live connection of the account with avatar id aid: its :10123 session and each gateway
// connection that verified a login for it. Closing a connection makes its own goroutine run the usual teardown (unbind,
// leave); this function does not wait for that and holds no lock while it closes. It is the store's OnBan hook (cmd/cherry).
func CloseBannedAid(aid string) {
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

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// hostHeader is the Host the real client sends with its guest login (fapi.play.naver.jp).
const hostHeader = "fapi.play.naver.jp"

// sessionBanner is the length of the server's :10123 banner; sessionHello is the client's handshake (internal/lpn transport.go).
const sessionBanner = 15

var sessionHello = []byte{0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x01, 0x0A, 0x35, 0xED, 0x6A}

// client is the HTTP and session side of one run. The HTTPS listener is the lab's own self-signed one, so the client does
// not verify it (the real client does not either, for the lab hosts).
type client struct {
	base    string // https://127.0.0.1:<port>
	session string // 127.0.0.1:<port> of the :10123 session listener
	gateway string // 127.0.0.1:<port> of the :10000 gateway; "" = no gateway players
	http    *http.Client
	st      *stats
	postsN  int      // seeded diary posts: number n belongs to account 4(n-1) (seed seedSocial)
	aids    []string // seeded avatar ids (seededAids); the owner of diary n is aids[4(n-1)]
}

func newClient(base, session string, postsN int, st *stats) *client {
	tr := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, // lab certificate, see above
		MaxIdleConns:        4096,
		MaxIdleConnsPerHost: 4096,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &client{base: base, session: session, http: &http.Client{Transport: tr, Timeout: 30 * time.Second}, st: st, postsN: postsN}
}

// player is one synthetic device: seeded account idx, its credentials and what it still has to claim.
type player struct {
	idx     int
	code    string
	token   string // AV_AUTH, rotated by every response that sets it
	aid     string
	sessKey string
	mail    []int64 // unclaimed mail sequence numbers of the seeded account
	garden  bool    // also enters its Garden over the gateway
	rng     *rand.Rand
}

// rotate follows the AV_AUTH rotation the server sends (checkSession, createSession mint a new alias).
func (p *player) rotate(setCookie []string) {
	for _, sc := range setCookie {
		if !strings.HasPrefix(sc, "AV_AUTH=") {
			continue
		}
		v := strings.TrimPrefix(sc, "AV_AUTH=")
		if end := strings.IndexByte(v, ';'); end >= 0 {
			v = v[:end]
		}
		p.token = strings.Trim(v, `"`)
	}
}

// call sends one request as p (p may be nil) and records it under kind. It returns the status (0 on a transport error)
// and the body.
func (c *client) call(p *player, kind, method, path, body string, host string) (int, []byte) {
	var rd io.Reader
	if body != "" {
		rd = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.st.add(kind, 0, 0, err)
		return 0, nil
	}
	if host != "" {
		req.Host = host
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.send(req, p, kind)
}

// send runs a built request as p (nil for none), records it under kind and follows the AV_AUTH rotation of the reply.
func (c *client) send(req *http.Request, p *player, kind string) (int, []byte) {
	if p != nil && p.token != "" {
		req.Header.Set("Cookie", `AV_AUTH="`+p.token+`"`)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.st.add(kind, 0, time.Since(start), err)
		return 0, nil
	}
	raw, rerr := io.ReadAll(resp.Body)
	resp.Body.Close()
	c.st.add(kind, resp.StatusCode, time.Since(start), rerr)
	if p != nil {
		p.rotate(resp.Header.Values("Set-Cookie"))
	}
	return resp.StatusCode, raw
}

// login is the first part of a player's life: guest login, createSession and the preload reads of the client.
func (c *client) login(p *player) error {
	body, _ := json.Marshal(map[string]string{"uniqueCode": p.code})
	req, err := http.NewRequest(http.MethodPost, c.base+"/v4/account/guest/generate", strings.NewReader(string(body)+"\x00"))
	if err != nil {
		return err
	}
	req.Host = hostHeader
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LINEPLAY-ACNT", guestHeader(p.code))
	status, raw := c.send(req, nil, "login")
	if status != http.StatusOK {
		return fmt.Errorf("guest login status %d", status)
	}
	var lr struct {
		Result struct {
			AccessToken string `json:"accessToken"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &lr); err != nil || lr.Result.AccessToken == "" {
		return fmt.Errorf("guest login body has no token")
	}
	p.token = lr.Result.AccessToken
	status, raw = c.call(p, "create_session", http.MethodGet, "/v4/createSession", "", "")
	if status != http.StatusOK {
		return fmt.Errorf("createSession status %d", status)
	}
	var sr struct {
		Result struct {
			SessionKey string `json:"sessionKey"`
			Aid        string `json:"aid"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &sr); err != nil || sr.Result.SessionKey == "" {
		return fmt.Errorf("createSession body has no session key")
	}
	p.sessKey, p.aid = sr.Result.SessionKey, sr.Result.Aid
	for _, q := range [][2]string{
		{http.MethodGet, "/v4/setting/all"}, {http.MethodGet, "/v4/inven/counts"}, {http.MethodGet, "/v4/storage/display"},
		{http.MethodGet, "/v4/badge/infos/"}, {http.MethodGet, "/v4/sync/friends/0"}, {http.MethodGet, "/v4/quest/status"},
	} {
		c.call(p, "preload", q[0], q[1], "", "")
	}
	c.call(p, "preload", http.MethodPost, "/v4/inven/closet/items/all", "{}\x00", "")
	return nil
}

// Action weights: reads are about 80 % of the requests, writes the rest (PLAN P9: the synthetic mix of the brief).
var actionWeights = []struct {
	kind   string
	weight int
}{
	{"closet", 10}, {"counts", 10}, {"badge", 8}, {"friends", 8}, {"diary_list", 6}, {"shop", 6}, {"quest", 4}, {"storage", 4},
	{"buy", 3}, {"recycle", 2}, {"mail", 2}, {"like", 3}, {"comment", 3}, {"diary_post", 1}, {"profile", 1},
}

func pickAction(rng *rand.Rand) string {
	total := 0
	for _, a := range actionWeights {
		total += a.weight
	}
	n := rng.Intn(total)
	for _, a := range actionWeights {
		if n < a.weight {
			return a.kind
		}
		n -= a.weight
	}
	return "counts"
}

// diaryOwner is the avatar id of the seeded diary post number n (the path of the like and comment routes names it).
func (c *client) diaryOwner(n int) string { return c.aids[4*(n-1)] }

// act performs one action of kind as p. A mail action with nothing left to claim reads the counts instead.
func (c *client) act(p *player, kind string) {
	switch kind {
	case "closet":
		c.call(p, kind, http.MethodPost, "/v4/inven/closet/items/all", "{}\x00", "")
	case "counts":
		c.call(p, kind, http.MethodGet, "/v4/inven/counts", "", "")
	case "badge":
		c.call(p, kind, http.MethodGet, "/v4/badge/infos/", "", "")
	case "friends":
		c.call(p, kind, http.MethodGet, "/v4/sync/friends/0", "", "")
	case "diary_list":
		c.call(p, kind, http.MethodGet, "/v4/diary2/ext/unfold/"+p.aid+"/?lastSeq=-1&size=10", "", "")
	case "shop":
		c.call(p, kind, http.MethodGet, "/v4/shop/fashion/itemlist/MDRESS", "", "")
	case "quest":
		c.call(p, kind, http.MethodGet, "/v4/quest/status", "", "")
	case "storage":
		c.call(p, kind, http.MethodGet, "/v4/storage/display", "", "")
	case "buy":
		// A clothing row of the AE category (100 Gems in prices.json); the seeded balance covers it.
		code := fmt.Sprintf("CUAE%05d", 1+p.rng.Intn(200))
		c.call(p, kind, http.MethodPost, "/v4/purchase/dress/buy",
			`{"deviceType":"android","itemType":"DRESS","coinUnit":"GEM","items":[{"itemCode":"`+code+`","price":100}],"brandShopId":"","brandShopNo":"","cpId":"avtplay","language":"en"}`+"\x00", "")
	case "recycle":
		seq := 1 + p.rng.Intn(8)
		c.call(p, kind, http.MethodPost, "/v4/inven/recycle/closet/v2",
			fmt.Sprintf(`{"usePurchasePrice":true,"appliedRecycleMultiple":1.0,"recycleItems":[{"invenSeq":%d,"itemCode":"CUAE%05d","count":1,"exepectedPrice":"100","price":"100"}]}`, seq, seq)+"\x00", "")
	case "mail":
		if len(p.mail) == 0 {
			c.act(p, "counts")
			return
		}
		seq := p.mail[0]
		p.mail = p.mail[1:]
		c.call(p, kind, http.MethodPost, fmt.Sprintf("/v4/postbox/receive/%d", seq), "", "")
	case "like":
		no := 1 + p.rng.Intn(c.postsN)
		c.call(p, kind, http.MethodPost, "/v4/diary2/like/save/"+c.diaryOwner(no),
			fmt.Sprintf(`{"diaryNo":%d,"likeAvatarId":"%s","likeType":1}`, no, p.aid)+"\x00", "")
	case "comment":
		no := 1 + p.rng.Intn(c.postsN)
		c.call(p, kind, http.MethodPost, "/v4/diary2/comment/write/"+c.diaryOwner(no),
			fmt.Sprintf(`{"diaryNo":%d,"avatarID":"%s","content":"load test"}`, no, p.aid)+"\x00", "")
	case "diary_post":
		c.call(p, kind, http.MethodPost, "/v4/diary2/ext/write",
			fmt.Sprintf(`{"avatarId":"%s","title":"load","content":"load test post","openState":"public","images":[{"imageUrl":""}]}`, p.aid)+"\x00", "")
	case "profile":
		c.call(p, kind, http.MethodPost, "/v4/profile/intro/",
			`{"location":"Kyoto","locationSecret":false,"sex":"male","sexSecret":true,"birthDay":"1104","birthDaySecret":false,"bloodType":"O","bloodTypeSecret":true,"job":"designer","jobSecret":false}`+"\x00", "")
	case "check_session":
		c.call(p, kind, http.MethodGet, "/v4/checkSession", "", "")
	}
}

// session holds one player's :10123 connection: the LO sendInit, then keepalive pings that the server echoes.
type session struct {
	conn    net.Conn
	mu      sync.Mutex
	pending []time.Time // send times of the pings not yet echoed, oldest first
}

// openSession connects the player's session socket and runs it until ctx ends. Each ping's echo is a latency sample.
func (c *client) openSession(ctx context.Context, p *player, ping time.Duration) {
	conn, err := net.DialTimeout("tcp", c.session, 5*time.Second)
	if err != nil {
		c.st.add("session_init", 0, 0, err)
		return
	}
	defer conn.Close()
	s := &session{conn: conn}
	start := time.Now()
	if err := s.handshake(p); err != nil {
		c.st.add("session_init", 0, time.Since(start), err)
		return
	}
	c.st.add("session_init", 200, time.Since(start), nil)
	go func() { // the reader: every frame the server sends, pings echoed back timed
		for {
			var prefix [4]byte
			if _, err := io.ReadFull(conn, prefix[:]); err != nil {
				return
			}
			payload := make([]byte, binary.BigEndian.Uint32(prefix[:]))
			if _, err := io.ReadFull(conn, payload); err != nil {
				return
			}
			if len(payload) == 2 && payload[0] == 'M' {
				s.mu.Lock()
				var sent time.Time
				if len(s.pending) > 0 {
					sent, s.pending = s.pending[0], s.pending[1:]
				}
				s.mu.Unlock()
				if !sent.IsZero() {
					c.st.add("session_ping", 200, time.Since(sent), nil)
				}
				continue
			}
			c.st.add("session_frame", 200, 0, nil)
		}
	}()
	t := time.NewTicker(ping)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			s.pending = append(s.pending, time.Now())
			s.mu.Unlock()
			if _, err := conn.Write([]byte{0, 0, 0, 2, 'M', 'Q'}); err != nil {
				c.st.add("session_ping", 0, 0, err)
				return
			}
		}
	}
}

// handshake sends the client's hello and its LO sendInit (the body names the session key) and waits for the
// configure reply the server sends back.
func (s *session) handshake(p *player) error {
	_ = s.conn.SetDeadline(time.Now().Add(10 * time.Second))
	banner := make([]byte, sessionBanner)
	if _, err := io.ReadFull(s.conn, banner); err != nil {
		return fmt.Errorf("banner: %w", err)
	}
	if _, err := s.conn.Write(sessionHello); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	body := []byte(`{"sessionKey":"` + p.sessKey + `"}` + "\x00")
	payload := []byte{'L', 'O', 0x10, 0x00}
	payload = binary.BigEndian.AppendUint32(payload, uint32(len(body)))
	payload = binary.BigEndian.AppendUint64(payload, 0)
	payload = append(payload, body...)
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
	if _, err := s.conn.Write(append(frame, payload...)); err != nil {
		return fmt.Errorf("sendInit: %w", err)
	}
	var prefix [4]byte
	if _, err := io.ReadFull(s.conn, prefix[:]); err != nil {
		return fmt.Errorf("configure: %w", err)
	}
	reply := make([]byte, binary.BigEndian.Uint32(prefix[:]))
	if _, err := io.ReadFull(s.conn, reply); err != nil {
		return fmt.Errorf("configure body: %w", err)
	}
	_ = s.conn.SetDeadline(time.Time{})
	if len(reply) < 4 || reply[0] != 'L' || reply[1] != 'O' {
		return fmt.Errorf("configure reply is not an LO frame")
	}
	return nil
}

// openGateway puts the player into its Garden over the :10000 gateway (agent 10, the Garden): the open frame, the login
// (msgid 0: the session key and avatar id as protobuf) and the room enter (msgid 1: the owner's avatar id, the player's
// own Garden), then keepalive pings until ctx ends. The stock client sends no walk or chat on the Garden, so none is
// driven here; the server moves its viewers itself.
func (c *client) openGateway(ctx context.Context, p *player, ping time.Duration) {
	conn, err := net.DialTimeout("tcp", c.gateway, 5*time.Second)
	if err != nil {
		c.st.add("gw_open", 0, 0, err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, make([]byte, sessionBanner)); err != nil {
		c.st.add("gw_open", 0, 0, err)
		return
	}
	if _, err := conn.Write(sessionHello); err != nil {
		c.st.add("gw_open", 0, 0, err)
		return
	}
	sid := make([]byte, 8)
	for i := range sid {
		sid[i] = byte(p.rng.Intn(256))
	}
	const agentGarden = 10
	open := append([]byte{'G', byte(agentGarden-1)<<3 | 1}, sid...) // subtype 1: open
	start := time.Now()
	if err := gwWrite(conn, open); err != nil {
		c.st.add("gw_open", 0, 0, err)
		return
	}
	if _, err := gwRead(conn); err != nil { // the control reply
		c.st.add("gw_open", 0, time.Since(start), err)
		return
	}
	c.st.add("gw_open", 200, time.Since(start), nil)

	gwCall := func(kind string, msgid uint16, body []byte) bool {
		frame := append([]byte{'G', byte(agentGarden-1) << 3}, sid...) // subtype 0: data
		frame = binary.BigEndian.AppendUint16(frame, msgid)
		frame = append(frame, body...)
		t := time.Now()
		if err := gwWrite(conn, frame); err != nil {
			c.st.add(kind, 0, 0, err)
			return false
		}
		if _, err := gwRead(conn); err != nil {
			c.st.add(kind, 0, time.Since(t), err)
			return false
		}
		c.st.add(kind, 200, time.Since(t), nil)
		return true
	}
	// Login: field 1 the session key (bytes), field 2 the avatar id (varint); the observer checks both are present.
	login := append([]byte{0x0a, byte(len(p.sessKey))}, p.sessKey...)
	login = append(login, 0x10)
	login = binary.AppendUvarint(login, mustUint(p.aid))
	if !gwCall("gw_login", 0, login) {
		return
	}
	// Room enter: field 1 the owner's avatar id, the player's own Garden.
	if !gwCall("gw_room_enter", 1, append([]byte{0x08}, binary.AppendUvarint(nil, mustUint(p.aid))...)) {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	var mu sync.Mutex
	var pending []time.Time
	go func() { // every frame the gateway sends: pings echoed back are timed, the rest counted
		for {
			payload, err := gwRead(conn)
			if err != nil {
				return
			}
			if len(payload) == 2 && payload[0] == 'M' {
				mu.Lock()
				var sent time.Time
				if len(pending) > 0 {
					sent, pending = pending[0], pending[1:]
				}
				mu.Unlock()
				if !sent.IsZero() {
					c.st.add("gw_ping", 200, time.Since(sent), nil)
				}
				continue
			}
			c.st.add("gw_frame", 200, 0, nil)
		}
	}()
	t := time.NewTicker(ping)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			mu.Lock()
			pending = append(pending, time.Now())
			mu.Unlock()
			if err := gwWrite(conn, []byte{'M', 'Q'}); err != nil {
				c.st.add("gw_ping", 0, 0, err)
				return
			}
		}
	}
}

// gwWrite sends one gateway frame: a 4-byte length, then the payload.
func gwWrite(conn net.Conn, payload []byte) error {
	_, err := conn.Write(append(binary.BigEndian.AppendUint32(nil, uint32(len(payload))), payload...))
	return err
}

// gwRead reads one gateway frame's payload.
func gwRead(conn net.Conn) ([]byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		return nil, err
	}
	payload := make([]byte, binary.BigEndian.Uint32(prefix[:]))
	_, err := io.ReadFull(conn, payload)
	return payload, err
}

// mustUint is the avatar id as a number (the seed writes numeric ids only).
func mustUint(aid string) uint64 {
	n, _ := strconv.ParseUint(aid, 10, 64)
	return n
}

// run is one player's life until ctx ends: login, the session (and the Garden for a share of the players), then the
// think-and-act loop.
func (c *client) run(ctx context.Context, p *player, think, ping time.Duration) {
	if err := c.login(p); err != nil {
		c.st.add("login_failed", 0, 0, err)
		return
	}
	sessionDone := make(chan struct{})
	go func() {
		defer close(sessionDone)
		c.openSession(ctx, p, ping)
	}()
	defer func() { <-sessionDone }() // the session closes with the run, so the server's end state has no open sessions of this player
	if p.garden && c.gateway != "" {
		gardenDone := make(chan struct{})
		go func() {
			defer close(gardenDone)
			c.openGateway(ctx, p, ping)
		}()
		defer func() { <-gardenDone }()
	}
	lastCheck := time.Now()
	for {
		wait := time.Duration(float64(think) * (0.5 + p.rng.Float64())) // uniform, mean = think
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if time.Since(lastCheck) > time.Minute {
			c.act(p, "check_session")
			lastCheck = time.Now()
			continue
		}
		c.act(p, pickAction(p.rng))
	}
}

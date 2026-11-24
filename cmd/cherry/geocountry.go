package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"cherry/internal/lpn"
	"cherry/internal/store"
)

// The staff pages' country of an avatar whose client sent none (store/geocountry.go). Each maintenance round takes the online
// players without a country, and posts their networks (lpn.Online: the /24 or /48 network address, never the player's own
// address) to the country.is batch API (geo.country-url) in batches of at most geoBatch. A network's answer is kept for the
// process: a network with no answer is not asked again until a restart. Private, loopback and other non-public networks are
// never sent. The game itself never sees these countries.

const (
	geoBatch      = 100 // country.is takes at most 100 addresses a request
	geoMaxBatches = 5   // requests a round: far below its 10 a second per caller
)

// geoLookup is the country lookup of one process: the endpoint, the client and the answers so far (network -> ISO code, ""
// when the service had none).
type geoLookup struct {
	url    string
	client *http.Client
	known  map[string]string
}

// newGeoLookup returns the lookup for url, nil when url is empty (the lookup is off).
func newGeoLookup(url string) *geoLookup {
	if url == "" {
		return nil
	}
	return &geoLookup{url: url, client: &http.Client{Timeout: 10 * time.Second}, known: map[string]string{}}
}

// publicNetwork is the network address of a Connection.Remote ("203.0.113.0/24"), false when it is not a public network.
func publicNetwork(remote string) (string, bool) {
	p, err := netip.ParsePrefix(remote)
	if err != nil {
		return "", false
	}
	a := p.Addr()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() {
		return "", false
	}
	return a.String(), true
}

// round looks up the online players that have no country yet and keeps what it finds; it returns how many it placed.
func (g *geoLookup) round(ctx context.Context, conns []lpn.Connection, now time.Time) (int, error) {
	netOf := map[string]string{} // avatar id -> network address
	var aids []string
	for _, c := range conns {
		aid := strconv.FormatUint(c.Aid, 10)
		if _, ok := netOf[aid]; ok {
			continue
		}
		if n, ok := publicNetwork(c.Remote); ok {
			netOf[aid] = n
			aids = append(aids, aid)
		}
	}
	unknown := store.CountryUnknownAids(aids)
	var ask []string
	asking := map[string]bool{}
	for _, aid := range unknown {
		n := netOf[aid]
		if _, done := g.known[n]; !done && !asking[n] {
			asking[n] = true
			ask = append(ask, n)
		}
	}
	for i := 0; i < len(ask) && i < geoBatch*geoMaxBatches; i += geoBatch {
		batch := ask[i:min(i+geoBatch, len(ask))]
		got, err := g.post(ctx, batch)
		if err != nil {
			return 0, err
		}
		for _, n := range batch {
			g.known[n] = got[n]
		}
	}
	found := map[string]string{}
	for _, aid := range unknown {
		if code := g.known[netOf[aid]]; code != "" {
			found[aid] = code
		}
	}
	return len(found), store.SetGeoCountries(found, now)
}

// post asks the batch API for the countries of addrs and returns the ones it answered (address -> ISO code). An address
// without an answer is missing from the reply (the API leaves it out).
func (g *geoLookup) post(ctx context.Context, addrs []string) (map[string]string, error) {
	body, err := json.Marshal(addrs)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cherry-server")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("country lookup: HTTP %d", resp.StatusCode)
	}
	var rows []struct {
		IP      string  `json:"ip"`
		Country *string `json:"country"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rows); err != nil {
		return nil, fmt.Errorf("country lookup: %w", err)
	}
	out := map[string]string{}
	for _, r := range rows {
		a, err := netip.ParseAddr(r.IP)
		if err != nil || r.Country == nil {
			continue
		}
		if code := strings.ToUpper(*r.Country); len(code) == 2 && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z' {
			out[a.String()] = code
		}
	}
	return out, nil
}

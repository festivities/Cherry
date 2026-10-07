package netsvc

import (
	"bytes"
	"encoding/binary"
	"net"
	"strings"
	"testing"
)

func dnsQuery(name string, qtype uint16) []byte {
	b := make([]byte, 0, 64)
	b = binary.BigEndian.AppendUint16(b, 0x1234)
	b = binary.BigEndian.AppendUint16(b, 0x0100)
	b = binary.BigEndian.AppendUint16(b, 1)
	b = append(b, 0, 0, 0, 0, 0, 0)
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			continue
		}
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0)
	b = binary.BigEndian.AppendUint16(b, qtype)
	b = binary.BigEndian.AppendUint16(b, 1)
	return b
}

func TestDNSParseQuery(t *testing.T) {
	name, qtype, qend, ok := parseQuery(dnsQuery("LAN3RD.LINE.ME.", 1))
	if !ok {
		t.Fatal("parseQuery failed")
	}
	if name != "lan3rd.line.me" {
		t.Errorf("name = %q, want %q", name, "lan3rd.line.me")
	}
	if qtype != 1 {
		t.Errorf("qtype = %d, want 1", qtype)
	}
	if q := dnsQuery("LAN3RD.LINE.ME.", 1); qend != len(q) {
		t.Errorf("qend = %d, want %d", qend, len(q))
	}
	for _, bad := range [][]byte{
		{},
		make([]byte, 12),
		append(dnsQuery("a.com", 1)[:12], 0xC0, 0x0C),
		append(dnsQuery("a.com", 1)[:12], 7, 'a'),
		dnsQuery("a.com", 1)[:len(dnsQuery("a.com", 1))-3],
	} {
		if _, _, _, ok := parseQuery(bad); ok {
			t.Errorf("parseQuery(%x) = ok, want failure", bad)
		}
	}
}

func TestDNSSinkAddresses(t *testing.T) {
	for _, name := range []string{
		"lan3rd.line.me", "LAN3RD.LINE.ME", "lan3rd.line.me.",
		"ads.play.naver.jp", "fapi.play.naver.jp", "play.naver.jp",
		"example.play.naver.net", "play-static.line-scdn.net",
		"obs.line-apps.com", "session.play.naver.jp",
	} {
		query := dnsQuery(name, 1)
		parsed, qtype, qend, ok := parseQuery(query)
		if !ok {
			t.Fatalf("%s: parseQuery failed", name)
		}
		if !shouldSink(parsed) {
			t.Fatalf("%s: shouldSink = false, want true", name)
		}
		resp := sinkResponse(query, qend, qtype, sinkIP(parsed))
		if got := binary.BigEndian.Uint16(resp[0:2]); got != 0x1234 {
			t.Errorf("%s: txid = %#x, want 0x1234", name, got)
		}
		if got := binary.BigEndian.Uint16(resp[2:4]); got != 0x8180 {
			t.Errorf("%s: flags = %#x, want 0x8180", name, got)
		}
		if got := binary.BigEndian.Uint16(resp[4:6]); got != 1 {
			t.Errorf("%s: qdcount = %d, want 1", name, got)
		}
		if got := binary.BigEndian.Uint16(resp[6:8]); got != 1 {
			t.Errorf("%s: ancount = %d, want 1", name, got)
		}
		if binary.BigEndian.Uint16(resp[8:10]) != 0 || binary.BigEndian.Uint16(resp[10:12]) != 0 {
			t.Errorf("%s: nscount/arcount = %d/%d, want 0/0", name, binary.BigEndian.Uint16(resp[8:10]), binary.BigEndian.Uint16(resp[10:12]))
		}
		if !bytes.Equal(resp[12:qend], query[12:qend]) {
			t.Errorf("%s: question echo mismatch", name)
		}
		if got := resp[len(resp)-4:]; !bytes.Equal(got, []byte{10, 0, 2, 2}) {
			t.Errorf("%s: A = %v, want 10.0.2.2", name, got)
		}
	}
	query := dnsQuery("lan3rd.line.me", 1)
	binary.BigEndian.PutUint16(query[10:], 1)
	_, _, qend, ok := parseQuery(query)
	if !ok {
		t.Fatal("edns parseQuery failed")
	}
	resp := sinkResponse(query, qend, 1, sinkIP("lan3rd.line.me"))
	if binary.BigEndian.Uint16(resp[10:12]) != 0 {
		t.Errorf("edns arcount = %d, want 0", binary.BigEndian.Uint16(resp[10:12]))
	}
}

func TestDNSAAAAEmpty(t *testing.T) {
	query := dnsQuery("fapi.play.naver.jp", 28)
	name, qtype, qend, ok := parseQuery(query)
	if !ok || name != "fapi.play.naver.jp" || qtype != 28 {
		t.Fatalf("parseQuery = %q %d %v", name, qtype, ok)
	}
	resp := sinkResponse(query, qend, qtype, sinkIP(name))
	if got := binary.BigEndian.Uint16(resp[2:4]); got != 0x8180 {
		t.Errorf("flags = %#x, want 0x8180", got)
	}
	if got := binary.BigEndian.Uint16(resp[6:8]); got != 0 {
		t.Errorf("ancount = %d, want 0", got)
	}
	if len(resp) != qend {
		t.Errorf("len = %d, want %d (no answer records)", len(resp), qend)
	}
}

func TestDNSNoSink(t *testing.T) {
	for _, name := range []string{
		"example.com", "line.me", "naver.jp", "evilplay.naver.jp",
		"lan3rd.line.me.evil.com", "line-scdn.net.evil.com", "",
	} {
		if shouldSink(name) {
			t.Errorf("shouldSink(%q) = true, want false", name)
		}
	}
	for _, name := range []string{"example.play.naver.jp", "lan3rd.line.me"} {
		if !shouldSink(name) {
			t.Errorf("shouldSink(%q) = false, want true", name)
		}
	}
}

func TestDNSGatewayRoute(t *testing.T) {
	if dnsGatewayA == nil {
		t.Fatal("invalid gateway LAN IP")
	}
	for name, want := range map[string][]byte{
		"gws.play.naver.jp":         {192, 168, 1, 12},
		"GWS.PLAY.NAVER.JP.":        {192, 168, 1, 12},
		"fapi.play.naver.jp":        {10, 0, 2, 2},
		"session.play.naver.jp":     {10, 0, 2, 2},
		"play-static.line-scdn.net": {10, 0, 2, 2},
		"lan3rd.line.me":            {10, 0, 2, 2},
	} {
		query := dnsQuery(name, 1)
		parsed, qtype, qend, ok := parseQuery(query)
		if !ok || !shouldSink(parsed) {
			t.Fatalf("%s: not a valid sunk query", name)
		}
		resp := sinkResponse(query, qend, qtype, sinkIP(parsed))
		if got := resp[len(resp)-4:]; !bytes.Equal(got, want) {
			t.Errorf("%s: A = %v, want %v", name, got, want)
		}
	}
	query := dnsQuery("gws.play.naver.jp", 28)
	name, qtype, qend, ok := parseQuery(query)
	if !ok {
		t.Fatal("invalid gateway AAAA query")
	}
	resp := sinkResponse(query, qend, qtype, sinkIP(name))
	if len(resp) != qend || binary.BigEndian.Uint16(resp[6:8]) != 0 {
		t.Fatalf("gateway AAAA = %x, want empty answer", resp)
	}
}

func TestDNSPhysicalClientGetsGateway(t *testing.T) {
	phone := net.ParseIP("192.0.2.50") // TEST-NET-1, never a local address.
	for _, name := range []string{"fapi.play.naver.jp", "session.play.naver.jp", "play-static.line-scdn.net", "gws.play.naver.jp"} {
		if got := sinkIPFor(phone, name); !got.Equal(dnsGatewayA) {
			t.Errorf("sinkIPFor(phone, %s) = %v, want gateway", name, got)
		}
	}
	// Emulator queries arrive from loopback or the host itself and keep 10.0.2.2.
	if got := sinkIPFor(net.ParseIP("127.0.0.1"), "fapi.play.naver.jp"); !got.Equal(dnsSinkA) {
		t.Errorf("sinkIPFor(loopback, fapi) = %v, want sink", got)
	}
	if got := sinkIPFor(net.ParseIP("::1"), "fapi.play.naver.jp"); !got.Equal(dnsSinkA) {
		t.Errorf("sinkIPFor(::1, fapi) = %v, want sink", got)
	}
}

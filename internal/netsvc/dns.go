// Package netsvc holds Cherry's network plumbing: the DNS sink/relay and the
// self-signed TLS certificates with SNI filtering.
package netsvc

import (
	"encoding/binary"
	"errors"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	dnsListenAddr   = ":53"
	dnsUpstreamAddr = "1.1.1.1:53"
	dnsSinkAddr     = "10.0.2.2"
	// ponytail: lab LAN IP; update alongside start-emulator.bat if DHCP changes.
	dnsGatewayAddr  = "192.168.1.12"
	dnsRelayTimeout = 4 * time.Second
)

var (
	dnsSinkSuffixes = []string{
		".play.naver.jp", ".play.naver.net",
		".line-scdn.net", ".line-apps.com",
		".line.naver.jp",
	}
	dnsSinkExact = map[string]bool{
		"ads.play.naver.jp": true,
		"lan3rd.line.me":    true,
	}
	dnsSinkA    = net.ParseIP(dnsSinkAddr).To4()
	dnsGatewayA = net.ParseIP(dnsGatewayAddr).To4()
)

// hostOwnIPs snapshots this machine's interface addresses once. Queries
// arriving from any other address come from a physical LAN device, which
// cannot reach the emulator-only 10.0.2.2 sink.
var hostOwnIPs = sync.OnceValue(func() map[string]bool {
	own := map[string]bool{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return own
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			own[ipnet.IP.String()] = true
		}
	}
	return own
})

// ListenDNS opens the UDP sink/relay socket. Failure is not fatal to Cherry:
// the caller logs it and keeps serving.
func ListenDNS(logger *log.Logger) (*net.UDPConn, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 53})
	if err != nil {
		return nil, err
	}
	go serveDNS(conn, logger)
	logger.Printf("cherry: dns listening addr=%s upstream=%s sink=%s", dnsListenAddr, dnsUpstreamAddr, dnsSinkAddr)
	return conn, nil
}

func serveDNS(conn *net.UDPConn, logger *log.Logger) {
	buf := make([]byte, 4096)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			logger.Printf("DNS read: %v", err)
			continue
		}
		query := append([]byte(nil), buf[:n]...)
		go handleDNS(conn, query, addr, logger)
	}
}

func handleDNS(conn *net.UDPConn, query []byte, addr *net.UDPAddr, logger *log.Logger) {
	name, qtype, qend, ok := parseQuery(query)
	if !ok {
		return
	}
	if shouldSink(name) && (qtype == 1 || qtype == 28) {
		ip := sinkIPFor(addr.IP, name)
		if _, err := conn.WriteToUDP(sinkResponse(query, qend, qtype, ip), addr); err != nil {
			logger.Printf("DNS sink %s qtype=%d: %v", name, qtype, err)
			return
		}
		logger.Printf("DNS sink %s qtype=%d -> %s remote=%s", name, qtype, ip, addr)
		return
	}
	reply, err := relayDNS(query)
	if err != nil {
		logger.Printf("DNS relay %s qtype=%d: %v", name, qtype, err)
		return
	}
	if _, err := conn.WriteToUDP(reply, addr); err != nil {
		logger.Printf("DNS relay write %s qtype=%d: %v", name, qtype, err)
	}
}

func relayDNS(query []byte) ([]byte, error) {
	up, err := net.DialTimeout("udp", dnsUpstreamAddr, dnsRelayTimeout)
	if err != nil {
		return nil, err
	}
	defer up.Close()
	if err := up.SetDeadline(time.Now().Add(dnsRelayTimeout)); err != nil {
		return nil, err
	}
	if _, err := up.Write(query); err != nil {
		return nil, err
	}
	reply := make([]byte, 4096)
	n, err := up.Read(reply)
	if err != nil {
		return nil, err
	}
	return reply[:n], nil
}

// parseQuery returns the lowercased qname (trailing dot stripped), qtype and
// the offset just past the question section.
func parseQuery(pkt []byte) (string, uint16, int, bool) {
	if len(pkt) < 12 {
		return "", 0, 0, false
	}
	i := 12
	var b strings.Builder
	for {
		if i >= len(pkt) {
			return "", 0, 0, false
		}
		ln := int(pkt[i])
		if ln == 0 {
			i++
			break
		}
		if ln&0xC0 != 0 || i+1+ln > len(pkt) {
			return "", 0, 0, false
		}
		i++
		b.Write(pkt[i : i+ln])
		b.WriteByte('.')
		i += ln
	}
	if i+4 > len(pkt) {
		return "", 0, 0, false
	}
	name := strings.TrimSuffix(strings.ToLower(b.String()), ".")
	return name, binary.BigEndian.Uint16(pkt[i:]), i + 4, true
}

func shouldSink(name string) bool {
	if dnsSinkExact[name] {
		return true
	}
	for _, suffix := range dnsSinkSuffixes {
		if name == suffix[1:] || strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func sinkIP(name string) net.IP {
	if name == "gws.play.naver.jp" {
		return dnsGatewayA
	}
	return dnsSinkA
}

// sinkIPFor answers physical LAN clients with the gateway address for every
// sunk name. The emulator (loopback or this host's own address) keeps the
// 10.0.2.2 sink, which only exists inside the emulator.
func sinkIPFor(remote net.IP, name string) net.IP {
	if name == "gws.play.naver.jp" {
		return dnsGatewayA
	}
	if remote.IsLoopback() || hostOwnIPs()[remote.String()] {
		return dnsSinkA
	}
	return dnsGatewayA
}

// sinkResponse answers A with the chosen IP and AAAA with NOERROR/empty so the
// client falls back to IPv4.
func sinkResponse(query []byte, qend int, qtype uint16, ip net.IP) []byte {
	resp := make([]byte, 12, qend+16)
	copy(resp, query[:12])
	binary.BigEndian.PutUint16(resp[2:], 0x8180)
	binary.BigEndian.PutUint16(resp[4:], 1)
	binary.BigEndian.PutUint16(resp[6:], 0)
	binary.BigEndian.PutUint16(resp[8:], 0)
	binary.BigEndian.PutUint16(resp[10:], 0)
	if qtype == 1 {
		binary.BigEndian.PutUint16(resp[6:], 1)
	}
	resp = append(resp, query[12:qend]...)
	if qtype == 1 {
		answer := []byte{0xC0, 0x0C, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 0, 0, 0, 0}
		copy(answer[12:], ip)
		resp = append(resp, answer...)
	}
	return resp
}

package web

import (
	"net"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestOPDSDiscoverySettings(t *testing.T) {
	tests := []struct {
		name         string
		addr         *net.TCPAddr
		hostname     string
		wantEnabled  bool
		wantInstance string
		wantHostname string
		wantBindIP   string
	}{
		{
			name:         "wildcard listener",
			addr:         &net.TCPAddr{IP: net.IPv4zero, Port: 8080},
			hostname:     "n37.local",
			wantEnabled:  true,
			wantInstance: "polka (on n37 port 8080)",
			wantHostname: "polka-8080-n37",
		},
		{
			name:         "specific interface",
			addr:         &net.TCPAddr{IP: net.ParseIP("192.0.2.15"), Port: 8081},
			hostname:     "book server",
			wantEnabled:  true,
			wantInstance: "polka (on book-server port 8081)",
			wantHostname: "polka-8081-book-server",
			wantBindIP:   "192.0.2.15",
		},
		{
			name:        "IPv4 loopback",
			addr:        &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
			hostname:    "n37",
			wantEnabled: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, enabled, err := makeOPDSDiscoverySettings(test.addr, test.hostname)
			if err != nil {
				t.Fatal(err)
			}
			if enabled != test.wantEnabled {
				t.Fatalf("enabled = %v, want %v", enabled, test.wantEnabled)
			}
			if !enabled {
				return
			}
			if got.instance != test.wantInstance {
				t.Errorf("instance = %q, want %q", got.instance, test.wantInstance)
			}
			if got.hostname != test.wantHostname {
				t.Errorf("hostname = %q, want %q", got.hostname, test.wantHostname)
			}
			if got.port != uint16(test.addr.Port) {
				t.Errorf("port = %d, want %d", got.port, test.addr.Port)
			}
			gotIP := ""
			if got.bindIP != nil {
				gotIP = got.bindIP.String()
			}
			if gotIP != test.wantBindIP {
				t.Errorf("bind IP = %q, want %q", gotIP, test.wantBindIP)
			}
		})
	}
}

func TestOPDSDiscoveryAnnouncement(t *testing.T) {
	service := testOPDSDiscoveryService(t)
	packet, err := service.announcement([]netip.Addr{
		netip.MustParseAddr("192.0.2.15"),
		netip.MustParseAddr("2001:db8::15"),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	var message dnsmessage.Message
	if err := message.Unpack(packet); err != nil {
		t.Fatal(err)
	}
	if !message.Header.Response || !message.Header.Authoritative {
		t.Errorf("header = %+v, want authoritative response", message.Header)
	}

	var foundPTR, foundSRV, foundTXT, foundA, foundAAAA bool
	for _, record := range message.Answers {
		wantTTL := uint32(mdnsDefaultTTL)
		if record.Header.Type == dnsmessage.TypeSRV || record.Header.Type == dnsmessage.TypeA || record.Header.Type == dnsmessage.TypeAAAA {
			wantTTL = mdnsHostTTL
		}
		if record.Header.TTL != wantTTL {
			t.Errorf("%v TTL = %d, want %d", record.Header.Type, record.Header.TTL, wantTTL)
		}
		cacheFlush := uint16(record.Header.Class)&mdnsCacheFlushBit != 0
		if want := record.Header.Type != dnsmessage.TypePTR; cacheFlush != want {
			t.Errorf("%v cache-flush = %v, want %v", record.Header.Type, cacheFlush, want)
		}
		switch body := record.Body.(type) {
		case *dnsmessage.PTRResource:
			if namesEqual(record.Header.Name, service.serviceType) && namesEqual(body.PTR, service.instance) {
				foundPTR = true
			}
		case *dnsmessage.SRVResource:
			foundSRV = body.Port == 8080 && namesEqual(body.Target, service.hostname)
		case *dnsmessage.TXTResource:
			foundTXT = len(body.TXT) == 1 && body.TXT[0] == "path=/opds"
		case *dnsmessage.AResource:
			foundA = body.A == netip.MustParseAddr("192.0.2.15").As4()
		case *dnsmessage.AAAAResource:
			foundAAAA = body.AAAA == netip.MustParseAddr("2001:db8::15").As16()
		}
	}
	if !foundPTR || !foundSRV || !foundTXT || !foundA || !foundAAAA {
		t.Errorf("records: PTR=%v SRV=%v TXT=%v A=%v AAAA=%v", foundPTR, foundSRV, foundTXT, foundA, foundAAAA)
	}

	packet, err = service.announcement([]netip.Addr{netip.MustParseAddr("192.0.2.15")}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := message.Unpack(packet); err != nil {
		t.Fatal(err)
	}
	for _, record := range message.Answers {
		if record.Header.TTL != 0 {
			t.Errorf("goodbye TTL = %d, want 0", record.Header.TTL)
		}
	}
}

func TestOPDSDiscoveryResponse(t *testing.T) {
	service := testOPDSDiscoveryService(t)
	question := dnsmessage.Question{
		Name:  service.serviceType,
		Type:  dnsmessage.TypePTR,
		Class: dnsmessage.Class(uint16(dnsmessage.ClassINET) | mdnsUnicastResponseBit),
	}
	query := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: 0x1234},
		Questions: []dnsmessage.Question{question},
	}
	queryPacket, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}

	responsePacket, preferUnicast, err := service.response(queryPacket, []netip.Addr{netip.MustParseAddr("192.0.2.15")}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !preferUnicast {
		t.Error("response did not preserve the query's unicast preference")
	}
	var response dnsmessage.Message
	if err := response.Unpack(responsePacket); err != nil {
		t.Fatal(err)
	}
	if response.Header.ID != 0 {
		t.Errorf("multicast response ID = %#x, want 0", response.Header.ID)
	}
	if len(response.Questions) != 0 {
		t.Errorf("multicast response repeated %d questions", len(response.Questions))
	}
	if len(response.Answers) != 1 || response.Answers[0].Header.Type != dnsmessage.TypePTR {
		t.Fatalf("answers = %#v, want one PTR", response.Answers)
	}
	var foundSRV, foundTXT, foundA bool
	for _, record := range response.Additionals {
		foundSRV = foundSRV || record.Header.Type == dnsmessage.TypeSRV
		foundTXT = foundTXT || record.Header.Type == dnsmessage.TypeTXT
		foundA = foundA || record.Header.Type == dnsmessage.TypeA
	}
	if !foundSRV || !foundTXT || !foundA {
		t.Errorf("additional records: SRV=%v TXT=%v A=%v", foundSRV, foundTXT, foundA)
	}

	question.Class = dnsmessage.ClassINET
	query.Questions[0] = question
	queryPacket, err = query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	if _, preferUnicast, err = service.response(queryPacket, nil, false); err != nil {
		t.Fatal(err)
	} else if preferUnicast {
		t.Error("ordinary multicast query requested a unicast response")
	}

	query.Header.ID = 0xbeef
	queryPacket, err = query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	responsePacket, _, err = service.response(queryPacket, []netip.Addr{netip.MustParseAddr("192.0.2.15")}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Unpack(responsePacket); err != nil {
		t.Fatal(err)
	}
	if response.Header.ID != query.Header.ID {
		t.Errorf("legacy response ID = %#x, want %#x", response.Header.ID, query.Header.ID)
	}
	if len(response.Questions) != 1 ||
		!namesEqual(response.Questions[0].Name, question.Name) ||
		response.Questions[0].Type != question.Type ||
		response.Questions[0].Class != question.Class {
		t.Fatalf("legacy response questions = %#v, want original question", response.Questions)
	}
	for _, records := range [][]dnsmessage.Resource{response.Answers, response.Additionals} {
		for _, record := range records {
			if uint16(record.Header.Class)&mdnsCacheFlushBit != 0 {
				t.Errorf("legacy %v record has cache-flush bit", record.Header.Type)
			}
			if record.Header.TTL != mdnsLegacyTTL {
				t.Errorf("legacy %v TTL = %d, want %d", record.Header.Type, record.Header.TTL, mdnsLegacyTTL)
			}
		}
	}
}

func testOPDSDiscoveryService(t *testing.T) opdsDiscoveryService {
	t.Helper()
	service, err := newOPDSDiscoveryService(opdsDiscoverySettings{
		instance: "polka (on n37 port 8080)",
		hostname: "polka-8080-n37",
		port:     8080,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

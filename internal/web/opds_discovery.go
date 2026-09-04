package web

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const (
	// OPDS readers use Calibre's DNS-SD service type for local catalog discovery.
	opdsDiscoveryServiceType  = "_calibre._tcp"
	mdnsPort                  = 5353
	mdnsCacheFlushBit         = 1 << 15
	mdnsUnicastResponseBit    = 1 << 15
	mdnsDefaultTTL            = 75 * 60
	mdnsHostTTL               = 120
	mdnsLegacyTTL             = 10
	mdnsWriteTimeout          = 100 * time.Millisecond
	mdnsMaxAddressesPerFamily = 8
)

type opdsDiscoverySettings struct {
	instance string
	hostname string
	port     uint16
	bindIP   net.IP
}

type opdsDiscoveryService struct {
	serviceType dnsmessage.Name
	instance    dnsmessage.Name
	hostname    dnsmessage.Name
	port        uint16
}

type opdsDiscoveryInterface struct {
	iface net.Interface
	addrs []netip.Addr
}

type opdsDiscoverySocket struct {
	conn    *net.UDPConn
	group   *net.UDPAddr
	iface   string
	addrs   []netip.Addr
	service opdsDiscoveryService
}

type opdsDiscoveryResponder struct {
	sockets   []*opdsDiscoverySocket
	done      chan struct{}
	wg        sync.WaitGroup
	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func startOPDSDiscovery(listener net.Listener) (*opdsDiscoveryResponder, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("get hostname: %w", err)
	}
	settings, ok, err := makeOPDSDiscoverySettings(listener.Addr(), hostname)
	if err != nil || !ok {
		return nil, err
	}
	service, err := newOPDSDiscoveryService(settings)
	if err != nil {
		return nil, err
	}
	interfaces, err := opdsDiscoveryInterfaces(settings.bindIP)
	if err != nil {
		return nil, err
	}

	responder := &opdsDiscoveryResponder{done: make(chan struct{})}
	var openErrs []error
	for _, candidate := range interfaces {
		var hasIPv4, hasIPv6 bool
		for _, addr := range candidate.addrs {
			if addr.Is6() {
				hasIPv6 = true
			} else {
				hasIPv4 = true
			}
		}
		var networks []string
		if hasIPv4 {
			networks = append(networks, "udp4")
		}
		if hasIPv6 {
			networks = append(networks, "udp6")
		}
		for _, network := range networks {
			socket, err := openOPDSDiscoverySocket(network, candidate, service)
			if err != nil {
				openErrs = append(openErrs, err)
				continue
			}
			responder.sockets = append(responder.sockets, socket)
		}
	}
	if len(responder.sockets) == 0 {
		if err := errors.Join(openErrs...); err != nil {
			return nil, fmt.Errorf("open mDNS sockets: %w", err)
		}
		return nil, errors.New("open mDNS sockets: no multicast-capable network interface")
	}

	for _, socket := range responder.sockets {
		responder.wg.Add(1)
		go responder.serve(socket)
	}
	if err := responder.broadcast(false); err != nil {
		_ = responder.Close()
		return nil, fmt.Errorf("announce OPDS service: %w", err)
	}
	responder.wg.Add(1)
	go responder.repeatAnnouncements()
	return responder, nil
}

func newOPDSDiscoveryService(settings opdsDiscoverySettings) (opdsDiscoveryService, error) {
	serviceType, err := dnsmessage.NewName(opdsDiscoveryServiceType + ".local.")
	if err != nil {
		return opdsDiscoveryService{}, err
	}
	instance, err := dnsmessage.NewName(settings.instance + "." + opdsDiscoveryServiceType + ".local.")
	if err != nil {
		return opdsDiscoveryService{}, fmt.Errorf("make mDNS service name: %w", err)
	}
	hostname, err := dnsmessage.NewName(settings.hostname + ".local.")
	if err != nil {
		return opdsDiscoveryService{}, fmt.Errorf("make mDNS hostname: %w", err)
	}
	return opdsDiscoveryService{
		serviceType: serviceType,
		instance:    instance,
		hostname:    hostname,
		port:        settings.port,
	}, nil
}

func opdsDiscoveryInterfaces(bindIP net.IP) ([]opdsDiscoveryInterface, error) {
	var (
		interfaces []net.Interface
		err        error
	)
	if bindIP == nil {
		interfaces, err = net.Interfaces()
	} else {
		interfaces, err = interfacesForIP(bindIP)
	}
	if err != nil {
		return nil, err
	}

	var candidates []opdsDiscoveryInterface
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := opdsDiscoveryAddresses(iface, bindIP)
		if err != nil {
			continue
		}
		if len(addrs) != 0 {
			candidates = append(candidates, opdsDiscoveryInterface{iface: iface, addrs: addrs})
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("no multicast-capable network interface")
	}
	return candidates, nil
}

func opdsDiscoveryAddresses(iface net.Interface, bindIP net.IP) ([]netip.Addr, error) {
	if bindIP != nil {
		addr, ok := netip.AddrFromSlice(bindIP)
		if !ok {
			return nil, fmt.Errorf("invalid listener address %q", bindIP)
		}
		return []netip.Addr{addr.Unmap()}, nil
	}

	addresses, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	// Keep aliases reachable without allowing an unusually large interface
	// configuration to grow multicast packets without bound.
	var ipv4Count, ipv6Count int
	seen := make(map[netip.Addr]struct{})
	var result []netip.Addr
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil {
			continue
		}
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		addr = addr.Unmap()
		if addr.IsUnspecified() || addr.IsLoopback() || addr.IsMulticast() ||
			(!addr.IsGlobalUnicast() && !addr.IsLinkLocalUnicast()) {
			continue
		}
		if _, exists := seen[addr]; exists {
			continue
		}
		if addr.Is4() {
			if ipv4Count == mdnsMaxAddressesPerFamily {
				continue
			}
			ipv4Count++
		} else {
			if ipv6Count == mdnsMaxAddressesPerFamily {
				continue
			}
			ipv6Count++
		}
		seen[addr] = struct{}{}
		result = append(result, addr)
	}
	return result, nil
}

func openOPDSDiscoverySocket(network string, candidate opdsDiscoveryInterface, service opdsDiscoveryService) (*opdsDiscoverySocket, error) {
	group := &net.UDPAddr{Port: mdnsPort}
	switch network {
	case "udp4":
		group.IP = net.IPv4(224, 0, 0, 251)
	case "udp6":
		group.IP = net.ParseIP("ff02::fb")
		group.Zone = candidate.iface.Name
	default:
		return nil, fmt.Errorf("unsupported mDNS network %q", network)
	}

	conn, err := net.ListenMulticastUDP(network, &candidate.iface, group)
	if err != nil {
		return nil, fmt.Errorf("listen for mDNS on %s/%s: %w", candidate.iface.Name, network, err)
	}
	if network == "udp4" {
		packetConn := ipv4.NewPacketConn(conn)
		err = errors.Join(
			packetConn.SetTTL(255),
			packetConn.SetMulticastTTL(255),
			packetConn.SetMulticastLoopback(true),
		)
	} else {
		packetConn := ipv6.NewPacketConn(conn)
		err = errors.Join(
			packetConn.SetHopLimit(255),
			packetConn.SetMulticastHopLimit(255),
			packetConn.SetMulticastLoopback(true),
		)
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("configure mDNS on %s/%s: %w", candidate.iface.Name, network, err)
	}
	return &opdsDiscoverySocket{
		conn:    conn,
		group:   group,
		iface:   candidate.iface.Name,
		addrs:   candidate.addrs,
		service: service,
	}, nil
}

func (r *opdsDiscoveryResponder) serve(socket *opdsDiscoverySocket) {
	defer r.wg.Done()
	buffer := make([]byte, 64*1024)
	for {
		n, source, err := socket.conn.ReadFromUDP(buffer)
		if err != nil {
			select {
			case <-r.done:
				return
			default:
				log.Printf("WARNING: OPDS local discovery stopped on %s: %v", socket.iface, err)
				return
			}
		}
		legacy := source.Port != mdnsPort
		response, preferUnicast, err := socket.service.response(buffer[:n], socket.addrs, legacy)
		if err != nil || len(response) == 0 {
			continue
		}
		destination := socket.group
		if legacy || preferUnicast {
			destination = source
		}
		if !r.writeResponse(socket, response, destination) {
			return
		}
	}
}

func (r *opdsDiscoveryResponder) writeResponse(socket *opdsDiscoverySocket, packet []byte, destination *net.UDPAddr) bool {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	select {
	case <-r.done:
		return false
	default:
		_ = socket.conn.SetWriteDeadline(time.Now().Add(mdnsWriteTimeout))
		_, _ = socket.conn.WriteToUDP(packet, destination)
		return true
	}
}

func (r *opdsDiscoveryResponder) repeatAnnouncements() {
	defer r.wg.Done()
	for delay := time.Second; delay <= 4*time.Second; delay *= 2 {
		select {
		case <-time.After(delay):
			_ = r.broadcast(false)
		case <-r.done:
			return
		}
	}
}

func (r *opdsDiscoveryResponder) broadcast(goodbye bool) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if !goodbye {
		select {
		case <-r.done:
			return nil
		default:
		}
	}

	var errs []error
	writes := 0
	for _, socket := range r.sockets {
		message, err := socket.service.announcement(socket.addrs, goodbye)
		if err == nil {
			_ = socket.conn.SetWriteDeadline(time.Now().Add(mdnsWriteTimeout))
			_, err = socket.conn.WriteToUDP(message, socket.group)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", socket.iface, err))
		} else {
			writes++
		}
	}
	if writes == 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (r *opdsDiscoveryResponder) Close() error {
	r.closeOnce.Do(func() {
		close(r.done)
		var errs []error
		// Query replies share writeMu with announcements, so the goodbye cannot
		// be followed by an in-flight response that restores the normal TTL.
		if err := r.broadcast(true); err != nil {
			errs = append(errs, err)
		}
		for _, socket := range r.sockets {
			if err := socket.conn.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		r.wg.Wait()
		r.closeErr = errors.Join(errs...)
	})
	return r.closeErr
}

type opdsRecordSet uint8

const (
	opdsServicePTR opdsRecordSet = 1 << iota
	opdsSRV
	opdsTXT
	opdsA
	opdsAAAA
)

func (s opdsDiscoveryService) announcement(addrs []netip.Addr, goodbye bool) ([]byte, error) {
	records := s.resources(opdsServicePTR|opdsSRV|opdsTXT|opdsA|opdsAAAA, addrs, goodbye)
	return (&dnsmessage.Message{
		Header:  dnsmessage.Header{Response: true, Authoritative: true},
		Answers: records,
	}).Pack()
}

func (s opdsDiscoveryService) response(queryPacket []byte, addrs []netip.Addr, legacy bool) ([]byte, bool, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(queryPacket)
	if err != nil {
		return nil, false, err
	}
	if header.Response || header.OpCode != 0 || header.RCode != 0 {
		return nil, false, nil
	}
	questions, err := parser.AllQuestions()
	if err != nil {
		return nil, false, err
	}
	answerSet, additionalSet, preferUnicast := s.recordsForQuestions(questions)
	if answerSet == 0 {
		return nil, false, nil
	}
	answers := s.resources(answerSet, addrs, false)
	if len(answers) == 0 {
		return nil, false, nil
	}
	additionalSet &^= answerSet
	response := dnsmessage.Message{
		Header:      dnsmessage.Header{Response: true, Authoritative: true},
		Answers:     answers,
		Additionals: s.resources(additionalSet, addrs, false),
	}
	if legacy {
		response.Header.ID = header.ID
		response.Questions = questions
		makeLegacyResources(response.Answers)
		makeLegacyResources(response.Additionals)
	}
	packet, err := response.Pack()
	return packet, preferUnicast, err
}

func makeLegacyResources(records []dnsmessage.Resource) {
	for i := range records {
		records[i].Header.Class = dnsmessage.Class(uint16(records[i].Header.Class) &^ mdnsCacheFlushBit)
		if records[i].Header.TTL > mdnsLegacyTTL {
			records[i].Header.TTL = mdnsLegacyTTL
		}
	}
}

func (s opdsDiscoveryService) recordsForQuestions(questions []dnsmessage.Question) (opdsRecordSet, opdsRecordSet, bool) {
	var answers, additionals opdsRecordSet
	preferUnicast := true
	matched := false
	for _, question := range questions {
		class := dnsmessage.Class(uint16(question.Class) &^ mdnsUnicastResponseBit)
		if class != dnsmessage.ClassINET && class != dnsmessage.ClassANY {
			continue
		}
		var answer, additional opdsRecordSet
		switch {
		case namesEqual(question.Name, s.serviceType) && typeMatches(question.Type, dnsmessage.TypePTR):
			answer = opdsServicePTR
			additional = opdsSRV | opdsTXT | opdsA | opdsAAAA
		case namesEqual(question.Name, s.instance):
			switch question.Type {
			case dnsmessage.TypeSRV:
				answer, additional = opdsSRV, opdsA|opdsAAAA
			case dnsmessage.TypeTXT:
				answer = opdsTXT
			case dnsmessage.TypeALL:
				answer, additional = opdsSRV|opdsTXT, opdsA|opdsAAAA
			}
		case namesEqual(question.Name, s.hostname):
			switch question.Type {
			case dnsmessage.TypeA:
				answer = opdsA
			case dnsmessage.TypeAAAA:
				answer = opdsAAAA
			case dnsmessage.TypeALL:
				answer = opdsA | opdsAAAA
			}
		}
		if answer == 0 {
			continue
		}
		matched = true
		if uint16(question.Class)&mdnsUnicastResponseBit == 0 {
			preferUnicast = false
		}
		answers |= answer
		additionals |= additional
	}
	return answers, additionals, matched && preferUnicast
}

func (s opdsDiscoveryService) resources(set opdsRecordSet, addrs []netip.Addr, goodbye bool) []dnsmessage.Resource {
	defaultTTL, hostTTL := uint32(mdnsDefaultTTL), uint32(mdnsHostTTL)
	if goodbye {
		defaultTTL, hostTTL = 0, 0
	}
	sharedClass := dnsmessage.ClassINET
	uniqueClass := dnsmessage.Class(uint16(dnsmessage.ClassINET) | mdnsCacheFlushBit)
	var records []dnsmessage.Resource
	if set&opdsServicePTR != 0 {
		records = append(records, dnsResource(s.serviceType, dnsmessage.TypePTR, sharedClass, defaultTTL,
			&dnsmessage.PTRResource{PTR: s.instance}))
	}
	if set&opdsSRV != 0 {
		records = append(records, dnsResource(s.instance, dnsmessage.TypeSRV, uniqueClass, hostTTL,
			&dnsmessage.SRVResource{Port: s.port, Target: s.hostname}))
	}
	if set&opdsTXT != 0 {
		records = append(records, dnsResource(s.instance, dnsmessage.TypeTXT, uniqueClass, defaultTTL,
			&dnsmessage.TXTResource{TXT: []string{"path=/opds"}}))
	}
	for _, addr := range addrs {
		switch {
		case set&opdsA != 0 && addr.Is4():
			records = append(records, dnsResource(s.hostname, dnsmessage.TypeA, uniqueClass, hostTTL,
				&dnsmessage.AResource{A: addr.As4()}))
		case set&opdsAAAA != 0 && addr.Is6():
			records = append(records, dnsResource(s.hostname, dnsmessage.TypeAAAA, uniqueClass, hostTTL,
				&dnsmessage.AAAAResource{AAAA: addr.As16()}))
		}
	}
	return records
}

func dnsResource(name dnsmessage.Name, recordType dnsmessage.Type, class dnsmessage.Class, ttl uint32, body dnsmessage.ResourceBody) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: name, Type: recordType, Class: class, TTL: ttl},
		Body:   body,
	}
}

func namesEqual(a, b dnsmessage.Name) bool {
	return strings.EqualFold(a.String(), b.String())
}

func typeMatches(got, want dnsmessage.Type) bool {
	return got == want || got == dnsmessage.TypeALL
}

func makeOPDSDiscoverySettings(addr net.Addr, hostname string) (opdsDiscoverySettings, bool, error) {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return opdsDiscoverySettings{}, false, fmt.Errorf("unsupported listener address %T", addr)
	}
	if tcpAddr.Port < 1 || tcpAddr.Port > 65535 {
		return opdsDiscoverySettings{}, false, fmt.Errorf("invalid listener port %d", tcpAddr.Port)
	}
	if tcpAddr.IP.IsLoopback() {
		return opdsDiscoverySettings{}, false, nil
	}

	host := mdnsHostLabel(hostname)
	settings := opdsDiscoverySettings{
		instance: mdnsInstanceName(host, tcpAddr.Port),
		// The SRV target belongs to this Polka listener, not to the machine as a
		// whole. Using the system's <host>.local name here would make Polka send
		// cache-flushing A/AAAA announcements and goodbye records for a name the
		// operating system still owns after Polka stops.
		hostname: mdnsEndpointHostLabel(host, tcpAddr.Port),
		port:     uint16(tcpAddr.Port),
	}
	if !tcpAddr.IP.IsUnspecified() {
		settings.bindIP = append(net.IP(nil), tcpAddr.IP...)
	}
	return settings, true, nil
}

func mdnsInstanceName(host string, port int) string {
	prefix := "polka (on "
	suffix := fmt.Sprintf(" port %d)", port)
	maxHostLength := 63 - len(prefix) - len(suffix)
	if len(host) > maxHostLength {
		host = strings.TrimRight(host[:maxHostLength], "-")
	}
	return prefix + host + suffix
}

func mdnsEndpointHostLabel(host string, port int) string {
	prefix := fmt.Sprintf("polka-%d-", port)
	maxHostLength := 63 - len(prefix)
	if len(host) > maxHostLength {
		host = strings.TrimRight(host[:maxHostLength], "-")
	}
	if host == "" {
		return strings.TrimSuffix(prefix, "-")
	}
	return prefix + host
}

func mdnsHostLabel(hostname string) string {
	hostname, _, _ = strings.Cut(strings.TrimSuffix(hostname, "."), ".")
	var label strings.Builder
	for _, r := range hostname {
		if r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-') {
			r = '-'
		}
		label.WriteRune(r)
		if label.Len() == 63 {
			break
		}
	}
	result := strings.Trim(label.String(), "-")
	if result == "" {
		return "polka"
	}
	return result
}

func interfacesForIP(ip net.IP) ([]net.Interface, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			candidate, _, err := net.ParseCIDR(addr.String())
			if err == nil && candidate.Equal(ip) {
				return []net.Interface{iface}, nil
			}
		}
	}
	return nil, fmt.Errorf("no network interface has listener address %s", ip)
}

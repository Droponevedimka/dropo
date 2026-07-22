package trafficorchestrator

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

var errUnsupportedPacket = errors.New("unsupported packet")

type parsedPacket struct {
	bytes           []byte
	ipVersion       int
	ipHeaderLength  int
	transportOffset int
	transportLength int
	payloadOffset   int
	network         Network
	source          netip.Addr
	destination     netip.Addr
	sourcePort      int
	destinationPort int
	tcpSequence     uint32
	tcpFlagsOffset  int
}

func parsePacket(packet []byte) (parsedPacket, error) {
	if len(packet) < 20 {
		return parsedPacket{}, errUnsupportedPacket
	}
	parsed := parsedPacket{bytes: packet}
	switch packet[0] >> 4 {
	case 4:
		if err := parsed.parseIPv4(); err != nil {
			return parsedPacket{}, err
		}
	case 6:
		if err := parsed.parseIPv6(); err != nil {
			return parsedPacket{}, err
		}
	default:
		return parsedPacket{}, errUnsupportedPacket
	}
	if err := parsed.parseTransport(); err != nil {
		return parsedPacket{}, err
	}
	return parsed, nil
}

func (p *parsedPacket) parseIPv4() error {
	ihl := int(p.bytes[0]&0x0f) * 4
	if ihl < 20 || ihl > len(p.bytes) {
		return errUnsupportedPacket
	}
	total := int(binary.BigEndian.Uint16(p.bytes[2:4]))
	if total < ihl || total > len(p.bytes) {
		return errUnsupportedPacket
	}
	frag := binary.BigEndian.Uint16(p.bytes[6:8])
	if frag&0x3fff != 0 {
		return errUnsupportedPacket
	}
	p.bytes = p.bytes[:total]
	p.ipVersion = 4
	p.ipHeaderLength = ihl
	p.transportOffset = ihl
	p.transportLength = total - ihl
	p.source = netip.AddrFrom4([4]byte(p.bytes[12:16]))
	p.destination = netip.AddrFrom4([4]byte(p.bytes[16:20]))
	switch p.bytes[9] {
	case 6:
		p.network = NetworkTCP
	case 17:
		p.network = NetworkUDP
	default:
		return errUnsupportedPacket
	}
	return nil
}

func (p *parsedPacket) parseIPv6() error {
	if len(p.bytes) < 40 {
		return errUnsupportedPacket
	}
	payloadLength := int(binary.BigEndian.Uint16(p.bytes[4:6]))
	total := 40 + payloadLength
	if total > len(p.bytes) {
		return errUnsupportedPacket
	}
	p.bytes = p.bytes[:total]
	p.ipVersion = 6
	p.ipHeaderLength = 40
	p.transportOffset = 40
	p.transportLength = payloadLength
	var source, destination [16]byte
	copy(source[:], p.bytes[8:24])
	copy(destination[:], p.bytes[24:40])
	p.source = netip.AddrFrom16(source)
	p.destination = netip.AddrFrom16(destination)
	switch p.bytes[6] {
	case 6:
		p.network = NetworkTCP
	case 17:
		p.network = NetworkUDP
	default:
		// Extension headers are passed unchanged until a bounded parser is added.
		return errUnsupportedPacket
	}
	return nil
}

func (p *parsedPacket) parseTransport() error {
	offset := p.transportOffset
	if p.network == NetworkTCP {
		if p.transportLength < 20 || offset+20 > len(p.bytes) {
			return errUnsupportedPacket
		}
		headerLength := int(p.bytes[offset+12]>>4) * 4
		if headerLength < 20 || headerLength > p.transportLength || offset+headerLength > len(p.bytes) {
			return errUnsupportedPacket
		}
		p.sourcePort = int(binary.BigEndian.Uint16(p.bytes[offset : offset+2]))
		p.destinationPort = int(binary.BigEndian.Uint16(p.bytes[offset+2 : offset+4]))
		p.tcpSequence = binary.BigEndian.Uint32(p.bytes[offset+4 : offset+8])
		p.tcpFlagsOffset = offset + 13
		p.payloadOffset = offset + headerLength
		return nil
	}
	if p.transportLength < 8 || offset+8 > len(p.bytes) {
		return errUnsupportedPacket
	}
	p.sourcePort = int(binary.BigEndian.Uint16(p.bytes[offset : offset+2]))
	p.destinationPort = int(binary.BigEndian.Uint16(p.bytes[offset+2 : offset+4]))
	udpLength := int(binary.BigEndian.Uint16(p.bytes[offset+4 : offset+6]))
	if udpLength < 8 || udpLength > p.transportLength {
		return errUnsupportedPacket
	}
	p.payloadOffset = offset + 8
	return nil
}

func (p parsedPacket) payload() []byte {
	if p.payloadOffset < 0 || p.payloadOffset > len(p.bytes) {
		return nil
	}
	return p.bytes[p.payloadOffset:]
}

func (p parsedPacket) flowEvidence() FlowEvidence {
	payload := p.payload()
	host := extractHTTPHost(payload)
	fingerprints := make([]string, 0, 3)
	if host != "" {
		fingerprints = append(fingerprints, "http-request")
	} else if tlsHost := extractTLSServerName(payload); tlsHost != "" {
		host = tlsHost
		fingerprints = append(fingerprints, "tls-client-hello")
	}
	if p.network == NetworkUDP {
		fingerprints = append(fingerprints, udpFingerprints(payload)...)
		if host == "" {
			host = extractQUICServerName(payload)
		}
	}
	return FlowEvidence{
		Network:      p.network,
		Destination:  p.destination.String(),
		Port:         p.destinationPort,
		Host:         host,
		Fingerprints: fingerprints,
	}
}

func extractHTTPHost(payload []byte) string {
	if len(payload) < 16 {
		return ""
	}
	firstEnd := bytesIndex(payload, []byte("\r\n"))
	if firstEnd <= 0 {
		return ""
	}
	requestLine := string(payload[:firstEnd])
	parts := strings.Split(requestLine, " ")
	if len(parts) != 3 || !strings.HasPrefix(parts[2], "HTTP/") {
		return ""
	}
	for cursor := firstEnd + 2; cursor < len(payload); {
		relativeEnd := bytesIndex(payload[cursor:], []byte("\r\n"))
		if relativeEnd < 0 {
			return ""
		}
		if relativeEnd == 0 {
			break
		}
		line := string(payload[cursor : cursor+relativeEnd])
		if colon := strings.IndexByte(line, ':'); colon > 0 && strings.EqualFold(strings.TrimSpace(line[:colon]), "host") {
			host := strings.TrimSpace(line[colon+1:])
			if parsed, err := netip.ParseAddrPort(host); err == nil {
				return parsed.Addr().String()
			}
			if index := strings.LastIndexByte(host, ':'); index > 0 && strings.Count(host, ":") == 1 {
				host = host[:index]
			}
			return normalizeHost(host)
		}
		cursor += relativeEnd + 2
	}
	return ""
}

func extractTLSServerName(payload []byte) string {
	if len(payload) < 5 || payload[0] != 0x16 {
		return ""
	}
	recordLength := int(binary.BigEndian.Uint16(payload[3:5]))
	if recordLength < 4 || 5+recordLength > len(payload) {
		return ""
	}
	handshake := payload[5 : 5+recordLength]
	if len(handshake) < 4 || handshake[0] != 0x01 {
		return ""
	}
	handshakeLength := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
	if handshakeLength+4 > len(handshake) || handshakeLength < 38 {
		return ""
	}
	return extractClientHelloServerName(handshake[:4+handshakeLength])
}

// extractClientHelloServerName parses a complete TLS ClientHello handshake
// without a TLS record wrapper. QUIC carries this form in CRYPTO frames.
func extractClientHelloServerName(handshake []byte) string {
	if len(handshake) < 42 || handshake[0] != 0x01 {
		return ""
	}
	handshakeLength := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
	if handshakeLength < 38 || handshakeLength+4 > len(handshake) {
		return ""
	}
	body := handshake[4 : 4+handshakeLength]
	cursor := 2 + 32
	if cursor >= len(body) {
		return ""
	}
	sessionLength := int(body[cursor])
	cursor++
	if cursor+sessionLength+2 > len(body) {
		return ""
	}
	cursor += sessionLength
	cipherLength := int(binary.BigEndian.Uint16(body[cursor : cursor+2]))
	cursor += 2
	if cursor+cipherLength+1 > len(body) {
		return ""
	}
	cursor += cipherLength
	compressionLength := int(body[cursor])
	cursor++
	if cursor+compressionLength+2 > len(body) {
		return ""
	}
	cursor += compressionLength
	extensionsLength := int(binary.BigEndian.Uint16(body[cursor : cursor+2]))
	cursor += 2
	if cursor+extensionsLength > len(body) {
		return ""
	}
	extensions := body[cursor : cursor+extensionsLength]
	for cursor = 0; cursor+4 <= len(extensions); {
		extensionType := binary.BigEndian.Uint16(extensions[cursor : cursor+2])
		extensionLength := int(binary.BigEndian.Uint16(extensions[cursor+2 : cursor+4]))
		cursor += 4
		if cursor+extensionLength > len(extensions) {
			return ""
		}
		if extensionType == 0 {
			return parseServerNameExtension(extensions[cursor : cursor+extensionLength])
		}
		cursor += extensionLength
	}
	return ""
}

func parseServerNameExtension(extension []byte) string {
	if len(extension) < 5 {
		return ""
	}
	listLength := int(binary.BigEndian.Uint16(extension[:2]))
	if listLength+2 > len(extension) {
		return ""
	}
	for cursor := 2; cursor+3 <= 2+listLength; {
		nameType := extension[cursor]
		nameLength := int(binary.BigEndian.Uint16(extension[cursor+1 : cursor+3]))
		cursor += 3
		if cursor+nameLength > len(extension) {
			return ""
		}
		if nameType == 0 {
			return normalizeHost(string(extension[cursor : cursor+nameLength]))
		}
		cursor += nameLength
	}
	return ""
}

func udpFingerprints(payload []byte) []string {
	result := make([]string, 0, 3)
	if isSTUN(payload) {
		result = append(result, "stun")
	}
	if isDiscordDiscovery(payload) {
		result = append(result, "discord-media")
	}
	if fingerprint := wireGuardFingerprint(payload); fingerprint != "" {
		result = append(result, fingerprint)
	}
	if len(payload) >= 5 && payload[0]&0x80 != 0 && binary.BigEndian.Uint32(payload[1:5]) != 0 {
		result = append(result, "quic-initial")
	}
	return result
}

func isSTUN(payload []byte) bool {
	return len(payload) >= 20 && payload[0]&0xc0 == 0 && binary.BigEndian.Uint16(payload[2:4])%4 == 0 && binary.BigEndian.Uint32(payload[4:8]) == 0x2112a442 && int(binary.BigEndian.Uint16(payload[2:4])) <= len(payload)-20
}

func isDiscordDiscovery(payload []byte) bool {
	if len(payload) != 74 || payload[0] != 0 || payload[1] != 1 || payload[2] != 0 || payload[3] != 70 {
		return false
	}
	for _, value := range payload[8:] {
		if value != 0 {
			return false
		}
	}
	return true
}

func wireGuardFingerprint(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	typeValue := binary.LittleEndian.Uint32(payload[:4])
	switch {
	case typeValue == 1 && len(payload) == 148:
		return "wireguard-initiation"
	case typeValue == 2 && len(payload) == 92:
		return "wireguard-response"
	case typeValue == 3 && len(payload) == 64:
		return "wireguard-cookie"
	case typeValue == 4 && len(payload) == 32:
		return "wireguard-keepalive"
	case typeValue == 4 && len(payload) >= 52:
		return "wireguard-data"
	default:
		return ""
	}
}

func bytesIndex(data, pattern []byte) int {
	if len(pattern) == 0 {
		return 0
	}
	for i := 0; i+len(pattern) <= len(data); i++ {
		matched := true
		for j := range pattern {
			if data[i+j] != pattern[j] {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	return -1
}

func resizePacketPayload(parsed parsedPacket, payload []byte) ([]byte, parsedPacket, error) {
	if len(payload) > 65535-parsed.payloadOffset {
		return nil, parsedPacket{}, errors.New("payload is too large")
	}
	packet := make([]byte, parsed.payloadOffset+len(payload))
	copy(packet, parsed.bytes[:parsed.payloadOffset])
	copy(packet[parsed.payloadOffset:], payload)
	if parsed.ipVersion == 4 {
		binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	} else {
		binary.BigEndian.PutUint16(packet[4:6], uint16(len(packet)-40))
	}
	if parsed.network == NetworkUDP {
		binary.BigEndian.PutUint16(packet[parsed.transportOffset+4:parsed.transportOffset+6], uint16(len(packet)-parsed.transportOffset))
	}
	updated, err := parsePacket(packet)
	if err != nil {
		return nil, parsedPacket{}, fmt.Errorf("parse resized packet: %w", err)
	}
	return packet, updated, nil
}

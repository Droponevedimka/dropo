package trafficorchestrator

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"testing"
)

func TestExtractQUICServerNameV1AndV2(t *testing.T) {
	for _, version := range []uint32{quicVersion1, quicVersion2} {
		packet := testQUICInitialPacket(t, version, "media.discordapp.net")
		if got := extractQUICServerName(packet); got != "media.discordapp.net" {
			t.Fatalf("version %#x SNI = %q", version, got)
		}
	}
}

func TestQUICServerNameClassifiesHTTP3Service(t *testing.T) {
	strategy := BuiltinStrategies()[0]
	plan := TrafficPlan{
		Revision: 1, CatalogRevision: "quic-test", Strategies: []TrafficStrategy{strategy},
		Services: []ServiceRule{{
			ID: "youtube", DisplayName: "YouTube", DomainSuffixes: []string{"googlevideo.com"},
			UDPPorts: []int{443}, CandidateStrategyIDs: []string{strategy.ID},
		}},
		Selections: []ServiceSelection{{ServiceID: "youtube", StrategyID: strategy.ID}},
	}
	processor, err := NewProcessor(plan)
	if err != nil {
		t.Fatal(err)
	}
	payload := testQUICInitialPacket(t, quicVersion1, "rr1---sn.example.googlevideo.com")
	decision := processor.Process(testIPv4UDPPacket("142.250.1.1", 443, payload))
	if decision.ServiceID != "youtube" || !decision.Transformed {
		t.Fatalf("HTTP/3 packet decision = %+v", decision)
	}
}

func TestQUICInitialParserFailsSafe(t *testing.T) {
	valid := testQUICInitialPacket(t, quicVersion1, "discord.com")
	for length := 0; length < len(valid); length++ {
		_ = extractQUICServerName(valid[:length])
	}
	corrupted := append([]byte(nil), valid...)
	corrupted[len(corrupted)-1] ^= 0xff
	if got := extractQUICServerName(corrupted); got != "" {
		t.Fatalf("authenticated corruption returned SNI %q", got)
	}
}

func testQUICInitialPacket(t *testing.T, version uint32, host string) []byte {
	t.Helper()
	record := fakeTLSClientHelloForServerName(host)
	handshake := record[5:]
	plaintext := []byte{0x06, 0x00}
	plaintext = append(plaintext, encodeTestQUICVarint(uint64(len(handshake)))...)
	plaintext = append(plaintext, handshake...)
	plaintext = append(plaintext, make([]byte, 32)...)

	dcid := []byte{0x83, 0x94, 0xc8, 0xf0, 0x3e, 0x51, 0x57, 0x08}
	first := byte(0xc0)
	if version == quicVersion2 {
		first = 0xd0
	}
	header := []byte{first, 0, 0, 0, 0, byte(len(dcid))}
	binary.BigEndian.PutUint32(header[1:5], version)
	header = append(header, dcid...)
	header = append(header, 0)    // source connection id length
	header = append(header, 0x00) // token length
	protectedLength := uint64(1 + len(plaintext) + 16)
	header = append(header, encodeTestQUICVarint(protectedLength)...)
	pnOffset := len(header)
	header = append(header, 0) // packet number zero, one byte

	keys, ok := deriveQUICClientInitialKeys(version, dcid)
	if !ok {
		t.Fatalf("derive keys for version %#x", version)
	}
	block, err := aes.NewCipher(keys.key[:])
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := aead.Seal(nil, keys.iv[:], plaintext, header)
	packet := append(append([]byte(nil), header...), ciphertext...)
	hpBlock, err := aes.NewCipher(keys.hp[:])
	if err != nil {
		t.Fatal(err)
	}
	var mask [16]byte
	hpBlock.Encrypt(mask[:], packet[pnOffset+4:pnOffset+20])
	packet[0] ^= mask[0] & 0x0f
	packet[pnOffset] ^= mask[1]
	return packet
}

func encodeTestQUICVarint(value uint64) []byte {
	if value < 64 {
		return []byte{byte(value)}
	}
	if value < 1<<14 {
		return []byte{byte(value>>8) | 0x40, byte(value)}
	}
	panic("test QUIC varint is too large")
}

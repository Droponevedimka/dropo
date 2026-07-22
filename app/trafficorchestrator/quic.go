package trafficorchestrator

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
)

const (
	quicVersion1      uint32 = 0x00000001
	quicVersion2      uint32 = 0x6b3343cf
	maxQUICCryptoData        = 64 * 1024
)

var (
	quicV1InitialSalt = [20]byte{0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17, 0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a}
	quicV2InitialSalt = [20]byte{0x0d, 0xed, 0xe3, 0xde, 0xf7, 0x00, 0xa6, 0xdb, 0x81, 0x93, 0x81, 0xbe, 0x6e, 0x26, 0x9d, 0xcb, 0xf9, 0xbd, 0x2e, 0xd9}
)

type quicInitialKeys struct {
	key [16]byte
	iv  [12]byte
	hp  [16]byte
}

// extractQUICServerName decrypts only the public client Initial keys defined by
// QUIC v1/v2 and extracts the ClientHello SNI. Application data is never
// decrypted, retained or logged. Any unsupported version, frame or malformed
// length fails closed to an empty host so the packet passes unchanged.
func extractQUICServerName(packet []byte) string {
	plaintext, ok := decryptQUICClientInitial(packet)
	if !ok {
		return ""
	}
	cryptoData, ok := quicInitialCryptoData(plaintext)
	if !ok {
		return ""
	}
	return extractClientHelloServerName(cryptoData)
}

func decryptQUICClientInitial(packet []byte) ([]byte, bool) {
	if len(packet) < 7 || packet[0]&0xc0 != 0xc0 {
		return nil, false
	}
	version := binary.BigEndian.Uint32(packet[1:5])
	switch version {
	case quicVersion1:
		if packet[0]&0x30 != 0x00 {
			return nil, false
		}
	case quicVersion2:
		if packet[0]&0x30 != 0x10 {
			return nil, false
		}
	default:
		return nil, false
	}
	cursor := 5
	dcidLength := int(packet[cursor])
	cursor++
	if dcidLength == 0 || dcidLength > 20 || cursor+dcidLength+1 > len(packet) {
		return nil, false
	}
	dcid := packet[cursor : cursor+dcidLength]
	cursor += dcidLength
	scidLength := int(packet[cursor])
	cursor++
	if scidLength > 20 || cursor+scidLength > len(packet) {
		return nil, false
	}
	cursor += scidLength
	tokenLength, tokenBytes, ok := decodeQUICVarint(packet[cursor:])
	if !ok || tokenLength > uint64(len(packet)) {
		return nil, false
	}
	cursor += tokenBytes
	if tokenLength > uint64(len(packet)-cursor) {
		return nil, false
	}
	cursor += int(tokenLength)
	protectedLength, lengthBytes, ok := decodeQUICVarint(packet[cursor:])
	if !ok {
		return nil, false
	}
	cursor += lengthBytes
	pnOffset := cursor
	if protectedLength < 1+16 || protectedLength > uint64(len(packet)-pnOffset) || pnOffset+4+16 > len(packet) {
		return nil, false
	}
	keys, ok := deriveQUICClientInitialKeys(version, dcid)
	if !ok {
		return nil, false
	}
	hpBlock, err := aes.NewCipher(keys.hp[:])
	if err != nil {
		return nil, false
	}
	var mask [16]byte
	hpBlock.Encrypt(mask[:], packet[pnOffset+4:pnOffset+4+16])
	first := packet[0] ^ (mask[0] & 0x0f)
	pnLength := int(first&0x03) + 1
	if protectedLength < uint64(pnLength+16) || pnOffset+pnLength > len(packet) {
		return nil, false
	}
	header := append([]byte(nil), packet[:pnOffset+pnLength]...)
	header[0] = first
	var packetNumber uint64
	for index := 0; index < pnLength; index++ {
		header[pnOffset+index] ^= mask[index+1]
		packetNumber = packetNumber<<8 | uint64(header[pnOffset+index])
	}
	packetEnd := pnOffset + int(protectedLength)
	ciphertext := packet[pnOffset+pnLength : packetEnd]
	block, err := aes.NewCipher(keys.key[:])
	if err != nil {
		return nil, false
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, false
	}
	nonce := keys.iv
	for index := 0; index < 8; index++ {
		nonce[len(nonce)-1-index] ^= byte(packetNumber >> (8 * index))
	}
	plaintext, err := aead.Open(nil, nonce[:], ciphertext, header)
	return plaintext, err == nil
}

func deriveQUICClientInitialKeys(version uint32, dcid []byte) (quicInitialKeys, bool) {
	var salt []byte
	var keyLabel, ivLabel, hpLabel string
	switch version {
	case quicVersion1:
		salt = quicV1InitialSalt[:]
		keyLabel, ivLabel, hpLabel = "quic key", "quic iv", "quic hp"
	case quicVersion2:
		salt = quicV2InitialSalt[:]
		keyLabel, ivLabel, hpLabel = "quicv2 key", "quicv2 iv", "quicv2 hp"
	default:
		return quicInitialKeys{}, false
	}
	initialSecret := hkdfExtract(salt, dcid)
	clientSecret := hkdfExpandLabel(initialSecret, "client in", 32)
	key := hkdfExpandLabel(clientSecret, keyLabel, 16)
	iv := hkdfExpandLabel(clientSecret, ivLabel, 12)
	hp := hkdfExpandLabel(clientSecret, hpLabel, 16)
	if len(key) != 16 || len(iv) != 12 || len(hp) != 16 {
		return quicInitialKeys{}, false
	}
	var result quicInitialKeys
	copy(result.key[:], key)
	copy(result.iv[:], iv)
	copy(result.hp[:], hp)
	return result, true
}

func hkdfExtract(salt, input []byte) []byte {
	mac := hmac.New(sha256.New, salt)
	_, _ = mac.Write(input)
	return mac.Sum(nil)
}

func hkdfExpandLabel(secret []byte, label string, length int) []byte {
	fullLabel := "tls13 " + label
	if length <= 0 || length > 255 || len(fullLabel) > 255 {
		return nil
	}
	info := make([]byte, 0, 4+len(fullLabel))
	info = append(info, byte(length>>8), byte(length), byte(len(fullLabel)))
	info = append(info, fullLabel...)
	info = append(info, 0) // empty context
	result := make([]byte, 0, length)
	var previous []byte
	for counter := byte(1); len(result) < length; counter++ {
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write(previous)
		_, _ = mac.Write(info)
		_, _ = mac.Write([]byte{counter})
		previous = mac.Sum(nil)
		need := length - len(result)
		if need > len(previous) {
			need = len(previous)
		}
		result = append(result, previous[:need]...)
	}
	return result
}

func decodeQUICVarint(data []byte) (uint64, int, bool) {
	if len(data) == 0 {
		return 0, 0, false
	}
	length := 1 << (data[0] >> 6)
	if length > 8 || len(data) < length {
		return 0, 0, false
	}
	value := uint64(data[0] & 0x3f)
	for index := 1; index < length; index++ {
		value = value<<8 | uint64(data[index])
	}
	return value, length, true
}

func quicInitialCryptoData(plaintext []byte) ([]byte, bool) {
	var assembled []byte
	covered := make([]bool, 0)
	for cursor := 0; cursor < len(plaintext); {
		frameType, read, ok := decodeQUICVarint(plaintext[cursor:])
		if !ok {
			return nil, false
		}
		cursor += read
		switch frameType {
		case 0x00: // PADDING
			continue
		case 0x01: // PING
			continue
		case 0x02, 0x03: // ACK / ACK_ECN
			var rangeCount uint64
			for field := 0; field < 4; field++ {
				value, size, valid := decodeQUICVarint(plaintext[cursor:])
				if !valid {
					return nil, false
				}
				cursor += size
				if field == 2 {
					rangeCount = value
				}
			}
			if rangeCount > 256 {
				return nil, false
			}
			for index := uint64(0); index < rangeCount*2; index++ {
				_, size, valid := decodeQUICVarint(plaintext[cursor:])
				if !valid {
					return nil, false
				}
				cursor += size
			}
			if frameType == 0x03 {
				for field := 0; field < 3; field++ {
					_, size, valid := decodeQUICVarint(plaintext[cursor:])
					if !valid {
						return nil, false
					}
					cursor += size
				}
			}
		case 0x06: // CRYPTO
			offset, offsetBytes, valid := decodeQUICVarint(plaintext[cursor:])
			if !valid {
				return nil, false
			}
			cursor += offsetBytes
			length, lengthBytes, valid := decodeQUICVarint(plaintext[cursor:])
			if !valid || offset > maxQUICCryptoData || length > maxQUICCryptoData || offset+length > maxQUICCryptoData {
				return nil, false
			}
			cursor += lengthBytes
			if length > uint64(len(plaintext)-cursor) {
				return nil, false
			}
			end := int(offset + length)
			if end > len(assembled) {
				assembled = append(assembled, make([]byte, end-len(assembled))...)
				covered = append(covered, make([]bool, end-len(covered))...)
			}
			copy(assembled[int(offset):end], plaintext[cursor:cursor+int(length)])
			for index := int(offset); index < end; index++ {
				covered[index] = true
			}
			cursor += int(length)
		default:
			// Initial packets normally contain ACK, CRYPTO and PADDING only.
			// Unknown frames are not guessed because their lengths are type-specific.
			return nil, false
		}
	}
	if len(assembled) < 4 {
		return nil, false
	}
	handshakeLength := int(assembled[1])<<16 | int(assembled[2])<<8 | int(assembled[3])
	total := 4 + handshakeLength
	if assembled[0] != 0x01 || handshakeLength < 38 || total > len(assembled) {
		return nil, false
	}
	for index := 0; index < total; index++ {
		if !covered[index] {
			return nil, false
		}
	}
	return assembled[:total], true
}

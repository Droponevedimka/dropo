package trafficorchestrator

import "encoding/binary"

func calculateChecksums(packet []byte) {
	parsed, err := parsePacket(packet)
	if err != nil {
		return
	}
	if parsed.ipVersion == 4 {
		packet[10], packet[11] = 0, 0
		binary.BigEndian.PutUint16(packet[10:12], internetChecksum(packet[:parsed.ipHeaderLength]))
	}
	checksumOffset := parsed.transportOffset + 16
	if parsed.network == NetworkUDP {
		checksumOffset = parsed.transportOffset + 6
	}
	if checksumOffset+2 > len(packet) {
		return
	}
	packet[checksumOffset], packet[checksumOffset+1] = 0, 0
	pseudo := make([]byte, 0, 40+parsed.transportLength)
	if parsed.ipVersion == 4 {
		pseudo = append(pseudo, packet[12:20]...)
		pseudo = append(pseudo, 0, packet[9])
		pseudo = append(pseudo, byte(parsed.transportLength>>8), byte(parsed.transportLength))
	} else {
		pseudo = append(pseudo, packet[8:40]...)
		pseudo = append(pseudo,
			byte(parsed.transportLength>>24), byte(parsed.transportLength>>16), byte(parsed.transportLength>>8), byte(parsed.transportLength),
			0, 0, 0, packet[6],
		)
	}
	pseudo = append(pseudo, packet[parsed.transportOffset:]...)
	checksum := internetChecksum(pseudo)
	if parsed.network == NetworkUDP && checksum == 0 {
		checksum = 0xffff
	}
	binary.BigEndian.PutUint16(packet[checksumOffset:checksumOffset+2], checksum)
}

func internetChecksum(data []byte) uint16 {
	var sum uint32
	for len(data) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(data[:2]))
		data = data[2:]
	}
	if len(data) == 1 {
		sum += uint32(data[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

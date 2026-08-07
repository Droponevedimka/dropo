//go:build windows

package trafficorchestrator

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	winDivertNetworkLayer = 0
	winDivertShutdownBoth = 3
	winDivertQueueLength  = 0
	winDivertQueueTime    = 1
	winDivertQueueSize    = 2

	dropoQueueLength = 4096
	dropoQueueTimeMS = 512
	dropoQueueSize   = 8 * 1024 * 1024
	dropoBatchSize   = 16
	dropoPacketSize  = 65535
)

// winDivertCaptureFilter mirrors the protocol evidence understood by
// parsedPacket.flowEvidence. Keeping the filter at handshake/fingerprint scope
// is essential: unclassified game traffic (for example CS2 UDP) must remain in
// kernel space and must never take a user-mode receive/reinject round trip.
//
// The UDP signatures are bounded forms of QUIC Initial, STUN, Discord IP
// discovery and WireGuard handshakes. Opaque Discord RTP is intentionally not
// captured or modified.
const winDivertCaptureFilter = `outbound and !loopback and !impostor and (
    (tcp and tcp.PayloadLength >= 4 and (
        (tcp.Payload[0] == 0x16 and tcp.Payload[1] == 0x03) or
        tcp.Payload32[0] == 0x47455420 or
        tcp.Payload32[0] == 0x504f5354 or
        tcp.Payload32[0] == 0x48454144 or
        tcp.Payload32[0] == 0x4f505449
    )) or
    (udp and (
        (udp.DstPort == 443 and udp.PayloadLength >= 5 and
            udp.Payload[0] >= 0x80 and udp.Payload32[1] != 0) or
        (udp.PayloadLength >= 20 and udp.Payload[0] < 0x40 and
            udp.Payload32[1] == 0x2112a442) or
        (udp.PayloadLength == 74 and udp.Payload32[0] == 0x00010046 and
            udp.Payload32[2] == 0 and udp.Payload32[3] == 0 and
            udp.Payload32[4] == 0 and udp.Payload32[5] == 0 and
            udp.Payload32[6] == 0 and udp.Payload32[7] == 0 and
            udp.Payload32[8] == 0 and udp.Payload32[9] == 0 and
            udp.Payload32[10] == 0 and udp.Payload32[11] == 0 and
            udp.Payload32[12] == 0 and udp.Payload32[13] == 0 and
            udp.Payload32[14] == 0 and udp.Payload32[15] == 0 and
            udp.Payload32[16] == 0 and udp.Payload32[17] == 0) or
        (udp.PayloadLength == 148 and udp.Payload32[0] == 0x01000000) or
        (udp.PayloadLength == 92 and udp.Payload32[0] == 0x02000000) or
        (udp.PayloadLength == 64 and udp.Payload32[0] == 0x03000000)
    ))
)`

var invalidWinDivertHandle = ^uintptr(0)

// WinDivertBackend is the only production owner of a WinDivert handle.
type WinDivertBackend struct {
	dll          *windows.LazyDLL
	handle       uintptr
	ownerMutex   windows.Handle
	procRecv     *windows.LazyProc
	procRecvEx   *windows.LazyProc
	procSend     *windows.LazyProc
	procShutdown *windows.LazyProc
	procClose    *windows.LazyProc
	closeOnce    sync.Once
	closeErr     error
	pending      []capturedPacket
	batchPackets []byte
	batchAddrs   []PacketAddress
}

type capturedPacket struct {
	data    []byte
	address PacketAddress
}

func OpenWinDivertBackend(dllPath string) (*WinDivertBackend, error) {
	dllPath, err := filepath.Abs(dllPath)
	if err != nil {
		return nil, fmt.Errorf("resolve WinDivert path: %w", err)
	}
	owner, err := acquireEngineOwnerMutex()
	if err != nil {
		return nil, err
	}
	backend := &WinDivertBackend{
		ownerMutex:   owner,
		batchPackets: make([]byte, dropoBatchSize*dropoPacketSize),
		batchAddrs:   make([]PacketAddress, dropoBatchSize),
	}
	cleanup := true
	defer func() {
		if cleanup {
			backend.releaseOwnerMutex()
		}
	}()

	backend.dll = windows.NewLazyDLL(dllPath)
	if err := backend.dll.Load(); err != nil {
		return nil, fmt.Errorf("load WinDivert DLL: %w", err)
	}
	procOpen := backend.dll.NewProc("WinDivertOpen")
	backend.procRecv = backend.dll.NewProc("WinDivertRecv")
	backend.procRecvEx = backend.dll.NewProc("WinDivertRecvEx")
	backend.procSend = backend.dll.NewProc("WinDivertSend")
	backend.procShutdown = backend.dll.NewProc("WinDivertShutdown")
	backend.procClose = backend.dll.NewProc("WinDivertClose")
	procSetParam := backend.dll.NewProc("WinDivertSetParam")
	for _, proc := range []*windows.LazyProc{procOpen, backend.procRecv, backend.procRecvEx, backend.procSend, backend.procShutdown, backend.procClose, procSetParam} {
		if err := proc.Find(); err != nil {
			return nil, fmt.Errorf("resolve %s: %w", proc.Name, err)
		}
	}

	// Empty TCP ACKs and zero-length UDP packets cannot carry a protocol
	// fingerprint or an application handshake. Reject them in kernel space so
	// bulk downloads do not divert every ACK through user mode.
	filter, err := syscall.BytePtrFromString(winDivertCaptureFilter)
	if err != nil {
		return nil, err
	}
	handle, _, callErr := procOpen.Call(uintptr(unsafe.Pointer(filter)), winDivertNetworkLayer, 0, 0)
	if handle == 0 || handle == invalidWinDivertHandle {
		return nil, windowsCallError("WinDivertOpen", callErr)
	}
	backend.handle = handle
	if err := setWinDivertParam(procSetParam, handle, winDivertQueueLength, dropoQueueLength); err != nil {
		_ = backend.Close()
		return nil, err
	}
	if err := setWinDivertParam(procSetParam, handle, winDivertQueueTime, dropoQueueTimeMS); err != nil {
		_ = backend.Close()
		return nil, err
	}
	if err := setWinDivertParam(procSetParam, handle, winDivertQueueSize, dropoQueueSize); err != nil {
		_ = backend.Close()
		return nil, err
	}
	cleanup = false
	return backend, nil
}

func (b *WinDivertBackend) Receive(buffer []byte) (int, PacketAddress, error) {
	if b == nil || b.handle == 0 {
		return 0, PacketAddress{}, ErrBackendClosed
	}
	if len(buffer) == 0 {
		return 0, PacketAddress{}, errors.New("receive buffer is empty")
	}
	if len(b.pending) > 0 {
		packet := b.pending[0]
		b.pending = b.pending[1:]
		if len(packet.data) > len(buffer) {
			return 0, PacketAddress{}, errors.New("queued packet exceeds receive buffer")
		}
		copy(buffer, packet.data)
		return len(packet.data), packet.address, nil
	}
	packetBuffer := b.batchPackets
	addresses := b.batchAddrs
	if len(packetBuffer) == 0 || len(addresses) == 0 {
		return 0, PacketAddress{}, errors.New("WinDivert batch buffers are not initialized")
	}
	var received uint32
	addressBytes := uint32(len(addresses)) * uint32(unsafe.Sizeof(PacketAddress{}))
	result, _, callErr := b.procRecvEx.Call(
		b.handle,
		uintptr(unsafe.Pointer(&packetBuffer[0])),
		uintptr(len(packetBuffer)),
		uintptr(unsafe.Pointer(&received)),
		0,
		uintptr(unsafe.Pointer(&addresses[0])),
		uintptr(unsafe.Pointer(&addressBytes)),
		0,
	)
	if result == 0 {
		if b.handle == 0 {
			return 0, PacketAddress{}, ErrBackendClosed
		}
		return 0, PacketAddress{}, windowsCallError("WinDivertRecv", callErr)
	}
	addressSize := uint32(unsafe.Sizeof(PacketAddress{}))
	if addressBytes == 0 || addressBytes%addressSize != 0 {
		return 0, PacketAddress{}, errors.New("WinDivertRecvEx returned an invalid address batch")
	}
	packetCount := int(addressBytes / addressSize)
	if packetCount > len(addresses) {
		return 0, PacketAddress{}, errors.New("WinDivertRecvEx exceeded the requested batch size")
	}
	offset := 0
	for index := 0; index < packetCount; index++ {
		packetLength, err := divertedPacketLength(packetBuffer[offset:int(received)])
		if err != nil || packetLength <= 0 || offset+packetLength > int(received) {
			return 0, PacketAddress{}, fmt.Errorf("decode WinDivert batch packet %d: %w", index, err)
		}
		b.pending = append(b.pending, capturedPacket{
			data: append([]byte(nil), packetBuffer[offset:offset+packetLength]...), address: addresses[index],
		})
		offset += packetLength
	}
	if offset != int(received) || len(b.pending) == 0 {
		return 0, PacketAddress{}, errors.New("WinDivertRecvEx returned an inconsistent packet batch")
	}
	packet := b.pending[0]
	b.pending = b.pending[1:]
	if len(packet.data) > len(buffer) {
		return 0, PacketAddress{}, errors.New("captured packet exceeds receive buffer")
	}
	copy(buffer, packet.data)
	return len(packet.data), packet.address, nil
}

func divertedPacketLength(packet []byte) (int, error) {
	if len(packet) < 1 {
		return 0, errors.New("empty packet")
	}
	switch packet[0] >> 4 {
	case 4:
		if len(packet) < 20 {
			return 0, errors.New("truncated IPv4 packet")
		}
		length := int(packet[2])<<8 | int(packet[3])
		if length < 20 {
			return 0, errors.New("invalid IPv4 total length")
		}
		return length, nil
	case 6:
		if len(packet) < 40 {
			return 0, errors.New("truncated IPv6 packet")
		}
		return 40 + (int(packet[4])<<8 | int(packet[5])), nil
	default:
		return 0, errors.New("unsupported IP version")
	}
}

func (b *WinDivertBackend) Send(packet []byte, address *PacketAddress) error {
	if b == nil || b.handle == 0 {
		return ErrBackendClosed
	}
	if len(packet) == 0 || address == nil {
		return errors.New("packet and address are required")
	}
	var sent uint32
	result, _, callErr := b.procSend.Call(
		b.handle,
		uintptr(unsafe.Pointer(&packet[0])),
		uintptr(len(packet)),
		uintptr(unsafe.Pointer(&sent)),
		uintptr(unsafe.Pointer(address)),
	)
	if result == 0 {
		return windowsCallError("WinDivertSend", callErr)
	}
	if int(sent) != len(packet) {
		return fmt.Errorf("WinDivertSend sent %d of %d bytes", sent, len(packet))
	}
	return nil
}

func (b *WinDivertBackend) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		handle := b.handle
		b.handle = 0
		if handle != 0 {
			_, _, _ = b.procShutdown.Call(handle, winDivertShutdownBoth)
			result, _, callErr := b.procClose.Call(handle)
			if result == 0 {
				b.closeErr = windowsCallError("WinDivertClose", callErr)
			}
		}
		b.releaseOwnerMutex()
	})
	return b.closeErr
}

func setWinDivertParam(proc *windows.LazyProc, handle uintptr, parameter, value uintptr) error {
	result, _, callErr := proc.Call(handle, parameter, value)
	if result == 0 {
		return windowsCallError("WinDivertSetParam", callErr)
	}
	return nil
}

func acquireEngineOwnerMutex() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(`Global\DropoTrafficOrchestrator`)
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		return 0, fmt.Errorf("create traffic engine mutex: %w", err)
	}
	status, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return 0, fmt.Errorf("wait for traffic engine mutex: %w", err)
	}
	if status != windows.WAIT_OBJECT_0 && status != windows.WAIT_ABANDONED {
		_ = windows.CloseHandle(handle)
		return 0, errors.New("another Dropo traffic engine already owns WinDivert")
	}
	return handle, nil
}

func (b *WinDivertBackend) releaseOwnerMutex() {
	if b.ownerMutex == 0 {
		return
	}
	_ = windows.ReleaseMutex(b.ownerMutex)
	_ = windows.CloseHandle(b.ownerMutex)
	b.ownerMutex = 0
}

func windowsCallError(operation string, err error) error {
	if err == nil || errors.Is(err, windows.ERROR_SUCCESS) {
		return errors.New(operation + " failed")
	}
	return fmt.Errorf("%s: %w", operation, err)
}

var _ PacketBackend = (*WinDivertBackend)(nil)

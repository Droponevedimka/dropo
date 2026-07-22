//go:build !windows

package trafficorchestrator

import "errors"

type WinDivertBackend struct{}

func OpenWinDivertBackend(string) (*WinDivertBackend, error) {
	return nil, errors.New("WinDivert is available only on Windows")
}

func (*WinDivertBackend) Receive([]byte) (int, PacketAddress, error) {
	return 0, PacketAddress{}, ErrBackendClosed
}

func (*WinDivertBackend) Send([]byte, *PacketAddress) error {
	return ErrBackendClosed
}

func (*WinDivertBackend) Close() error {
	return nil
}

//go:build windows

package main

import (
	"encoding/binary"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modiphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = modiphlpapi.NewProc("GetExtendedTcpTable")
)

const (
	tcpTableOwnerPidAll = 5                  // TCP_TABLE_OWNER_PID_ALL
	mibTCPStateEstab    = 5                  // MIB_TCP_STATE_ESTAB
	afInet              = 2                  // AF_INET
	loopbackAddrLE      = uint32(0x0100007F) // 127.0.0.1 as the DWORD MIB_TCPROW stores it
)

// mibTCPRowOwnerPID mirrors MIB_TCPROW_OWNER_PID (all fields DWORD, ports/addrs
// in network byte order).
type mibTCPRowOwnerPID struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPID  uint32
}

func portFromDword(d uint32) uint16 {
	// The low two bytes hold the port in network byte order.
	return uint16(byte(d))<<8 | uint16(byte(d>>8))
}

// telegramProxyHasActiveConnection reports whether any ESTABLISHED IPv4 TCP
// connection has its LOCAL endpoint at 127.0.0.1:<port> — i.e. the local
// MTProto sidecar has an accepted connection, which means Telegram is actively
// using the proxy. Telegram exposes no API to read its proxy config, so this
// loopback-socket check is the reliable indirect signal.
func telegramProxyHasActiveConnection(port uint16) bool {
	var size uint32
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPidAll, 0)
	if size == 0 {
		return false
	}
	buf := make([]byte, size)
	ret, _, _ := procGetExtendedTcpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0, afInet, tcpTableOwnerPidAll, 0,
	)
	if ret != 0 {
		return false
	}
	numEntries := binary.LittleEndian.Uint32(buf[0:4])
	rowSize := int(unsafe.Sizeof(mibTCPRowOwnerPID{}))
	for i := uint32(0); i < numEntries; i++ {
		off := 4 + int(i)*rowSize
		if off+rowSize > len(buf) {
			break
		}
		row := (*mibTCPRowOwnerPID)(unsafe.Pointer(&buf[off]))
		if row.State == mibTCPStateEstab && row.LocalAddr == loopbackAddrLE && portFromDword(row.LocalPort) == port {
			return true
		}
	}
	return false
}

// isProcessRunningByName reports whether a process with the given executable
// name (case-insensitive, e.g. "Telegram.exe") is currently running.
func isProcessRunningByName(name string) bool {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return false
	}
	target := strings.ToLower(name)
	for {
		if strings.ToLower(windows.UTF16ToString(entry.ExeFile[:])) == target {
			return true
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return false
		}
	}
}

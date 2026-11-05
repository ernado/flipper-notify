package transport

import (
	"encoding/binary"
	"strings"
	"time"
	"unsafe"

	"github.com/go-faster/errors"
	"golang.org/x/sys/unix"
)

// HCI definitions (include/net/bluetooth/hci.h, hci_sock.h) missing from x/sys/unix.
const (
	hciFilter       = 2          // HCI_FILTER socket option
	hciGetConnList  = 0x800448d4 // HCIGETCONNLIST = _IOR('H', 212, int)
	hciMaxConn      = 10
	hciConnInfoSize = 16 // struct hci_conn_info
	btConnected     = 1  // BT_CONNECTED connection state

	hciCommandPkt  = 0x01
	hciEventPkt    = 0x04
	evtCmdComplete = 0x0e

	opReadRSSI = 0x05<<10 | 0x0005 // OGF status params, OCF Read RSSI
)

// ReadRSSI returns the RSSI (dBm) of the live connection to mac on hci0, as measured
// by the local controller (HCI Read RSSI). Allowed for unprivileged users by the
// kernel's HCI security filter.
func ReadRSSI(mac string) (int, error) {
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.BTPROTO_HCI)
	if err != nil {
		return 0, errors.Wrap(err, "hci socket")
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Bind(fd, &unix.SockaddrHCI{Dev: 0, Channel: unix.HCI_CHANNEL_RAW}); err != nil {
		return 0, errors.Wrap(err, "bind hci0")
	}

	handle, err := connHandle(fd, mac)
	if err != nil {
		return 0, err
	}

	// struct hci_filter { u32 type_mask; u32 event_mask[2]; u16 opcode; } (padded to 16).
	var flt [16]byte
	binary.LittleEndian.PutUint32(flt[0:], 1<<hciEventPkt)
	binary.LittleEndian.PutUint32(flt[4:], 1<<evtCmdComplete)
	if err := unix.SetsockoptString(fd, unix.SOL_HCI, hciFilter, string(flt[:])); err != nil {
		return 0, errors.Wrap(err, "set hci filter")
	}
	tv := unix.NsecToTimeval((2 * time.Second).Nanoseconds())
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		return 0, errors.Wrap(err, "set timeout")
	}

	cmd := []byte{hciCommandPkt, 0, 0, 2, 0, 0}
	binary.LittleEndian.PutUint16(cmd[1:], opReadRSSI)
	binary.LittleEndian.PutUint16(cmd[4:], handle)
	if _, err := unix.Write(fd, cmd); err != nil {
		return 0, errors.Wrap(err, "send read rssi")
	}

	buf := make([]byte, 260)
	for range 8 {
		n, err := unix.Read(fd, buf)
		if err != nil {
			return 0, errors.Wrap(err, "read hci event")
		}
		// event pkt, Command Complete, plen, ncmd, opcode(2), status, handle(2), rssi
		if n < 10 || buf[0] != hciEventPkt || buf[1] != evtCmdComplete ||
			binary.LittleEndian.Uint16(buf[4:]) != opReadRSSI ||
			binary.LittleEndian.Uint16(buf[7:]) != handle {
			continue
		}
		if status := buf[6]; status != 0 {
			return 0, errors.Errorf("read rssi: hci status 0x%02x", status)
		}
		return int(int8(buf[9])), nil
	}
	return 0, errors.New("read rssi: no reply")
}

// connHandle finds the HCI connection handle for mac (HCIGETCONNLIST).
func connHandle(fd int, mac string) (uint16, error) {
	want, err := parseBDAddr(mac)
	if err != nil {
		return 0, err
	}
	// struct hci_conn_list_req { u16 dev_id; u16 conn_num; struct hci_conn_info conn_info[]; }
	// struct hci_conn_info { u16 handle; bdaddr_t bdaddr; u8 type; u8 out; u16 state; u32 link_mode; }
	buf := make([]byte, 4+hciMaxConn*hciConnInfoSize)
	binary.LittleEndian.PutUint16(buf[0:], 0)
	binary.LittleEndian.PutUint16(buf[2:], hciMaxConn)
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), hciGetConnList, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		return 0, errors.Wrap(errno, "list hci connections")
	}
	n := int(binary.LittleEndian.Uint16(buf[2:]))
	for i := range min(n, hciMaxConn) {
		ci := buf[4+i*hciConnInfoSize:]
		if [6]byte(ci[2:8]) == want && binary.LittleEndian.Uint16(ci[10:]) == btConnected {
			return binary.LittleEndian.Uint16(ci[0:]), nil
		}
	}
	return 0, errors.New("not connected")
}

// parseBDAddr parses "AA:BB:CC:DD:EE:FF" into bdaddr_t (little-endian byte order).
func parseBDAddr(mac string) ([6]byte, error) {
	var a [6]byte
	parts := strings.Split(mac, ":")
	if len(parts) != 6 {
		return a, errors.Errorf("bad mac %q", mac)
	}
	for i, p := range parts {
		var b byte
		if len(p) != 2 {
			return a, errors.Errorf("bad mac %q", mac)
		}
		for _, c := range []byte(strings.ToLower(p)) {
			switch {
			case c >= '0' && c <= '9':
				b = b<<4 | (c - '0')
			case c >= 'a' && c <= 'f':
				b = b<<4 | (c - 'a' + 10)
			default:
				return a, errors.Errorf("bad mac %q", mac)
			}
		}
		a[5-i] = b
	}
	return a, nil
}

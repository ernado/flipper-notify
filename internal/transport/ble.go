package transport

import (
	"context"
	"encoding/binary"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/go-faster/errors"
	"github.com/godbus/dbus/v5"
	"tinygo.org/x/bluetooth"
)

// Flipper BLE serial service (targets/f7/ble_glue/services/serial_service_uuid.inc).
var (
	serialService = bluetooth.NewUUID([16]byte{0x8f, 0xe5, 0xb3, 0xd5, 0x2e, 0x7f, 0x4a, 0x98, 0x2a, 0x48, 0x7a, 0xcc, 0x60, 0xfe, 0x00, 0x00})
	// RX: host -> Flipper (write).
	serialRX = bluetooth.NewUUID([16]byte{0x19, 0xed, 0x82, 0xae, 0xed, 0x21, 0x4c, 0x9d, 0x41, 0x45, 0x22, 0x8e, 0x62, 0xfe, 0x00, 0x00})
	// TX: Flipper -> host (indicate).
	serialTX = bluetooth.NewUUID([16]byte{0x19, 0xed, 0x82, 0xae, 0xed, 0x21, 0x4c, 0x9d, 0x41, 0x45, 0x22, 0x8e, 0x61, 0xfe, 0x00, 0x00})
	// Flow control: big-endian uint32, bytes the Flipper is ready to receive (notify).
	serialFlow = bluetooth.NewUUID([16]byte{0x19, 0xed, 0x82, 0xae, 0xed, 0x21, 0x4c, 0x9d, 0x41, 0x45, 0x22, 0x8e, 0x63, 0xfe, 0x00, 0x00})
)

const blePacketMax = 486 // BLE_SVC_SERIAL_DATA_LEN_MAX

var (
	adapterOnce sync.Once
	adapterErr  error
)

func adapter() (*bluetooth.Adapter, error) {
	adapterOnce.Do(func() {
		adapterErr = bluetooth.DefaultAdapter.Enable()
	})
	if adapterErr != nil {
		return nil, errors.Wrap(adapterErr, "enable adapter")
	}
	return bluetooth.DefaultAdapter, nil
}

type bleConn struct {
	dev        bluetooth.Device
	rx, tx, fc bluetooth.DeviceCharacteristic
	chunk      int

	pr *io.PipeReader
	pw *io.PipeWriter

	mu     sync.Mutex
	cond   *sync.Cond
	credit int
	closed bool
	once   sync.Once
	stop   chan struct{}
}

// OpenBLE connects to an already bonded Flipper by MAC and returns the RPC byte stream.
func OpenBLE(ctx context.Context, mac string) (io.ReadWriteCloser, error) {
	a, err := adapter()
	if err != nil {
		return nil, err
	}
	m, err := bluetooth.ParseMAC(mac)
	if err != nil {
		return nil, errors.Wrap(err, "parse mac")
	}

	type result struct {
		dev bluetooth.Device
		err error
	}
	done := make(chan result, 1)
	go func() {
		dev, err := a.Connect(bluetooth.Address{MACAddress: bluetooth.MACAddress{MAC: m}}, bluetooth.ConnectionParams{})
		done <- result{dev, err}
	}()
	var dev bluetooth.Device
	select {
	case r := <-done:
		if r.err != nil {
			return nil, errors.Wrap(r.err, "connect")
		}
		dev = r.dev
	case <-ctx.Done():
		// BlueZ keeps the connection attempt pending after we give up, and later attempts
		// then fail with "In Progress"; cancel it explicitly.
		cancelPending(m)
		go func() {
			if r := <-done; r.err == nil {
				_ = r.dev.Disconnect()
			}
		}()
		return nil, errors.Wrap(ctx.Err(), "connect")
	}

	c, err := setupBLE(dev)
	if err != nil {
		_ = dev.Disconnect()
		return nil, err
	}
	return c, nil
}

// cancelPending aborts an in-flight BlueZ connection attempt to mac (Device1.Disconnect).
func cancelPending(mac bluetooth.MAC) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	path := dbus.ObjectPath("/org/bluez/hci0/dev_" + strings.ReplaceAll(mac.String(), ":", "_"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Object("org.bluez", path).CallWithContext(ctx, "org.bluez.Device1.Disconnect", 0).Err
}

func setupBLE(dev bluetooth.Device) (*bleConn, error) {
	svcs, err := dev.DiscoverServices([]bluetooth.UUID{serialService})
	if err != nil {
		return nil, errors.Wrap(err, "discover services")
	}
	if len(svcs) == 0 {
		return nil, errors.New("serial service not found (is the Flipper in serial/RPC BLE profile?)")
	}
	chars, err := svcs[0].DiscoverCharacteristics([]bluetooth.UUID{serialRX, serialTX, serialFlow})
	if err != nil {
		return nil, errors.Wrap(err, "discover characteristics")
	}
	c := &bleConn{dev: dev, chunk: blePacketMax, stop: make(chan struct{})}
	c.cond = sync.NewCond(&c.mu)
	found := 0
	for _, ch := range chars {
		switch ch.UUID() {
		case serialRX:
			c.rx = ch
			found++
		case serialTX:
			c.tx = ch
			found++
		case serialFlow:
			c.fc = ch
			found++
		}
	}
	if found != 3 {
		return nil, errors.Errorf("serial characteristics: found %d of 3", found)
	}
	if mtu, err := c.rx.GetMTU(); err == nil && mtu > 3 && int(mtu)-3 < c.chunk {
		c.chunk = int(mtu) - 3
	}

	buf := make([]byte, 4)
	if n, err := c.fc.Read(buf); err == nil && n == 4 {
		c.credit = int(binary.BigEndian.Uint32(buf))
	}
	if err := c.fc.EnableNotifications(func(b []byte) {
		if len(b) != 4 {
			return
		}
		c.mu.Lock()
		c.credit = int(binary.BigEndian.Uint32(b))
		c.cond.Broadcast()
		c.mu.Unlock()
	}); err != nil {
		return nil, errors.Wrap(err, "enable flow control notifications")
	}

	c.pr, c.pw = io.Pipe()
	data := make(chan []byte, 256)
	go func() {
		for {
			select {
			case b := <-data:
				if _, err := c.pw.Write(b); err != nil {
					return
				}
			case <-c.stop:
				return
			}
		}
	}()
	if err := c.tx.EnableNotifications(func(b []byte) {
		cp := append([]byte(nil), b...)
		select {
		case data <- cp:
		case <-c.stop:
		}
	}); err != nil {
		_ = c.fc.EnableNotifications(nil)
		return nil, errors.Wrap(err, "enable tx indications")
	}
	go c.watch()
	return c, nil
}

// watch closes the connection when BlueZ reports the device disconnected.
func (c *bleConn) watch() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			if ok, err := c.dev.Connected(); err != nil || !ok {
				c.shutdown(errors.New("ble disconnected"))
				return
			}
		}
	}
}

func (c *bleConn) Read(p []byte) (int, error) { return c.pr.Read(p) }

func (c *bleConn) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		c.mu.Lock()
		for c.credit <= 0 && !c.closed {
			c.cond.Wait()
		}
		if c.closed {
			c.mu.Unlock()
			return written, io.ErrClosedPipe
		}
		n := min(len(p), c.credit, c.chunk)
		c.credit -= n
		c.mu.Unlock()

		if _, err := c.rx.Write(p[:n]); err != nil {
			return written, errors.Wrap(err, "ble write")
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

func (c *bleConn) shutdown(reason error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.cond.Broadcast()
		c.mu.Unlock()
		close(c.stop)
		_ = c.tx.EnableNotifications(nil)
		_ = c.fc.EnableNotifications(nil)
		_ = c.dev.Disconnect()
		_ = c.pw.CloseWithError(reason)
	})
}

func (c *bleConn) Close() error {
	c.shutdown(io.EOF)
	return nil
}

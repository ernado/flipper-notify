// Package transport provides byte streams carrying Flipper RPC: USB CDC and BLE serial.
package transport

import (
	"bytes"
	"io"
	"time"

	"github.com/go-faster/errors"
	"go.bug.st/serial"
)

// OpenUSB opens the Flipper USB CLI at path and switches it into RPC mode.
func OpenUSB(path string) (io.ReadWriteCloser, error) {
	port, err := serial.Open(path, &serial.Mode{BaudRate: 230400})
	if err != nil {
		return nil, errors.Wrap(err, "open serial")
	}
	if err := startRPC(port); err != nil {
		_ = port.Close()
		return nil, err
	}
	return port, nil
}

func readUntil(port serial.Port, marker []byte, timeout time.Duration) ([]byte, error) {
	if err := port.SetReadTimeout(100 * time.Millisecond); err != nil {
		return nil, errors.Wrap(err, "set timeout")
	}
	var buf []byte
	tmp := make([]byte, 512)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n, err := port.Read(tmp)
		if err != nil {
			return buf, errors.Wrap(err, "read")
		}
		buf = append(buf, tmp[:n]...)
		if bytes.Contains(buf, marker) {
			return buf, nil
		}
	}
	return buf, errors.Errorf("timeout waiting for %q", marker)
}

func startRPC(port serial.Port) error {
	// Get to a clean CLI prompt. If a previous RPC session is still active, the
	// stray bytes make it fail to decode and the device drops back to the CLI.
	var err error
	for range 3 {
		if _, err = port.Write([]byte("\r")); err != nil {
			return errors.Wrap(err, "write")
		}
		if _, err = readUntil(port, []byte(">: "), 1500*time.Millisecond); err == nil {
			break
		}
	}
	if err != nil {
		return errors.Wrap(err, "wait for cli prompt")
	}
	_ = port.ResetInputBuffer()
	if _, err := port.Write([]byte("start_rpc_session\r")); err != nil {
		return errors.Wrap(err, "write")
	}
	if _, err := readUntil(port, []byte("\n"), 2*time.Second); err != nil {
		return errors.Wrap(err, "start rpc session")
	}
	if err := port.SetReadTimeout(serial.NoTimeout); err != nil {
		return errors.Wrap(err, "set timeout")
	}
	return nil
}

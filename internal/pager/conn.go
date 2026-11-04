package pager

import (
	"context"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/go-faster/errors"

	"github.com/ernado/flipper-notify/internal/pb/flipper"
	"github.com/ernado/flipper-notify/internal/rpc"
	"github.com/ernado/flipper-notify/internal/transport"
)

// Dial settings.
type Dial struct {
	Mode       string // "auto", "ble", "usb"
	MAC        string // BLE address of the bonded Flipper
	USB        string // serial device path
	BLETimeout time.Duration
}

// Open connects using the configured transport(s) and returns the stream and its name.
func (d Dial) Open(ctx context.Context) (io.ReadWriteCloser, string, error) {
	ble := func() (io.ReadWriteCloser, string, error) {
		if d.MAC == "" {
			return nil, "", errors.New("ble: no MAC configured")
		}
		tctx, cancel := context.WithTimeout(ctx, d.BLETimeout)
		defer cancel()
		rw, err := transport.OpenBLE(tctx, d.MAC)
		if err != nil {
			return nil, "", errors.Wrap(err, "ble")
		}
		return rw, "ble", nil
	}
	usb := func() (io.ReadWriteCloser, string, error) {
		if d.USB == "" {
			return nil, "", errors.New("usb: no device configured")
		}
		if _, err := os.Stat(d.USB); err != nil {
			return nil, "", errors.Wrap(err, "usb")
		}
		rw, err := transport.OpenUSB(d.USB)
		if err != nil {
			return nil, "", errors.Wrap(err, "usb")
		}
		return rw, "usb", nil
	}
	switch d.Mode {
	case "ble":
		return ble()
	case "usb":
		return usb()
	case "", "auto":
		rw, name, bleErr := ble()
		if bleErr == nil {
			return rw, name, nil
		}
		rw, name, usbErr := usb()
		if usbErr == nil {
			return rw, name, nil
		}
		return nil, "", errors.Join(bleErr, usbErr)
	}
	return nil, "", errors.Errorf("unknown transport %q", d.Mode)
}

// Connect opens an RPC session and verifies it with a ping.
func (d Dial) Connect(ctx context.Context, onEvent func(*rpc.Client, *flipper.Main)) (*rpc.Client, string, error) {
	rw, name, err := d.Open(ctx)
	if err != nil {
		return nil, "", err
	}
	c := rpc.New(rw, onEvent)
	pctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if err := c.Ping(pctx); err != nil {
		_ = c.Close()
		return nil, "", errors.Wrapf(err, "%s", name)
	}
	return c, name, nil
}

// Maintain keeps a session to the Flipper open, reconnecting with backoff.
func (p *Pager) Maintain(ctx context.Context, d Dial) {
	const (
		minBackoff   = 2 * time.Second
		maxBackoff   = 30 * time.Second
		pingEvery    = 30 * time.Second
		batteryEvery = 5 * time.Minute
	)
	backoff := minBackoff
	var lastErr string
	for ctx.Err() == nil {
		c, name, err := d.Connect(ctx, p.onEvent)
		if err != nil {
			// Log only changes to avoid spamming while the Flipper is away.
			if msg := err.Error(); msg != lastErr {
				p.log.Warn("Connect failed", "err", err, "retry", backoff)
				lastErr = msg
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff, lastErr = minBackoff, ""
		select {
		case p.events <- evConnected{client: c, transport: name}:
		case <-ctx.Done():
			_ = c.Close()
			return
		}

		p.pollBattery(ctx, c)
		lastBattery := time.Now()
		t := time.NewTicker(pingEvery)
	alive:
		for {
			select {
			case <-ctx.Done():
				t.Stop()
				// Let Run hide the display first; then close.
				time.Sleep(500 * time.Millisecond)
				_ = c.Close()
				return
			case <-c.Done():
				break alive
			case <-t.C:
				pctx, cancel := context.WithTimeout(ctx, callTimeout)
				err := c.Ping(pctx)
				cancel()
				if err != nil {
					p.log.Warn("Ping failed", "err", err)
					_ = c.Close()
					break alive
				}
				if time.Since(lastBattery) >= batteryEvery {
					p.pollBattery(ctx, c)
					lastBattery = time.Now()
				}
			}
		}
		t.Stop()
		select {
		case p.events <- evDisconnected{client: c}:
		case <-ctx.Done():
			return
		}
	}
}

// pollBattery reads pwrinfo and reports it to Run.
func (p *Pager) pollBattery(ctx context.Context, c *rpc.Client) {
	pctx, cancel := context.WithTimeout(ctx, callTimeout)
	props, err := c.Properties(pctx, "pwrinfo")
	cancel()
	if err != nil {
		p.log.Debug("Battery read failed", "err", err)
		return
	}
	lvl, err := strconv.Atoi(props["charge.level"])
	if err != nil {
		return
	}
	select {
	case p.events <- evBattery{client: c, b: Battery{Level: lvl, State: props["charge.state"], Updated: time.Now().UTC()}}:
	case <-ctx.Done():
	}
}

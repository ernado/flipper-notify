package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/go-faster/errors"

	"github.com/ernado/flipper-notify/internal/httpapi"
	"github.com/ernado/flipper-notify/internal/ipc"
	"github.com/ernado/flipper-notify/internal/pager"
)

func dialFlags(fs interface {
	StringVar(*string, string, string, string)
	DurationVar(*time.Duration, string, time.Duration, string)
}) *pager.Dial {
	d := &pager.Dial{}
	fs.StringVar(&d.Mode, "transport", envOr("FPAGER_TRANSPORT", "auto"), "auto, ble or usb")
	fs.StringVar(&d.MAC, "mac", os.Getenv("FPAGER_MAC"), "BLE MAC of the bonded Flipper")
	fs.StringVar(&d.USB, "usb", defaultUSB(), "USB CDC device")
	fs.DurationVar(&d.BLETimeout, "ble-timeout", 30*time.Second, "BLE connect timeout")
	return d
}

func cmdDaemon(ctx context.Context, args []string) error {
	fs := newFlags("daemon")
	d := dialFlags(fs)
	socket := fs.String("socket", ipc.SocketPath(), "unix socket path")
	debug := fs.Bool("debug", false, "debug logging")
	alert := fs.String("alert", envOr("FPAGER_ALERT", "vibro"), "alert: vibro (companion app, no sound), full (vibro+sound+LED), none")
	sound := fs.String("sound", envOr("FPAGER_DEFAULT_SOUND", "pda"), "default sound for notifications without one (none = silent)")
	levelSounds := fs.String("level-sounds", envOr("FPAGER_LEVEL_SOUNDS", "fail=sms"), "per-level default sounds, e.g. fail=sms,ok=pda (none = silent)")
	batteryLow := fs.Int("battery-low", envInt("FPAGER_BATTERY_LOW", 15), "notify when the Flipper battery drops below this percent (0 = off)")
	httpAddr := fs.String("http", os.Getenv("FPAGER_HTTP"), "also serve the HTTP API on this address (requires FPAGER_TOKEN), e.g. 192.168.1.10:9732")
	_ = fs.Parse(args)

	lvl := slog.LevelInfo
	if *debug {
		lvl = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: lvl,
		// No timestamps: journald adds its own.
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	if d.MAC == "" && d.Mode != "usb" {
		log.Warn("No BLE MAC configured (-mac / FPAGER_MAC), only USB will be used")
	}

	p := pager.New(log)
	if err := p.SetAlert(*alert); err != nil {
		return err
	}
	p.SetBatteryLow(*batteryLow)
	if err := p.SetDefaultSound(*sound); err != nil {
		return err
	}
	if err := p.SetLevelSounds(*levelSounds); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errc := make(chan error, 1)
	go func() { errc <- ipc.Serve(ctx, log, *socket, p) }()
	go p.Maintain(ctx, *d)
	if *httpAddr != "" {
		token := os.Getenv("FPAGER_TOKEN")
		if len(token) < 16 {
			return errors.New("-http requires FPAGER_TOKEN (at least 16 characters)")
		}
		go httpapi.Serve(ctx, log, *httpAddr, httpapi.Handler(log, p, token))
	}
	runErr := make(chan error, 1)
	go func() { runErr <- p.Run(ctx) }()
	log.Info("Started", "socket", *socket, "transport", d.Mode, "mac", d.MAC, "usb", d.USB)

	select {
	case err := <-errc:
		cancel()
		<-runErr
		if err != nil {
			return errors.Wrap(err, "serve")
		}
		return nil
	case <-ctx.Done():
		<-runErr
		time.Sleep(700 * time.Millisecond) // let Maintain close the session
		return nil
	}
}

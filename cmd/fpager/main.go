// Command fpager turns a Flipper Zero into a pager for host notifications.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
)

const usage = `fpager — Flipper Zero pager

Usage:
  fpager daemon  [flags]                 keep a connection to the Flipper, serve the socket
  fpager send    [flags] [message...]    send a notification (message from stdin if omitted or "-")
  fpager run     [flags] -- cmd [args]   run a command, notify when it exits
  fpager wait    [flags] PID             notify when an existing process exits
  fpager status                          show daemon / connection state
  fpager claude-hook [-s]                Claude Code hook: reads hook JSON on stdin, always exits 0
  fpager probe   [flags]                 connect directly and test the display (daemon must not hold the transport)

Environment:
  FPAGER_MAC        BLE address of the bonded Flipper
  FPAGER_USB        USB CDC device (default: /dev/serial/by-id/usb-Flipper_Devices*)
  FPAGER_TRANSPORT  auto (BLE, then USB), ble, usb
  FPAGER_SOCKET     daemon socket (default: $XDG_RUNTIME_DIR/fpager.sock)
  FPAGER_ALERT      daemon: vibro (default), full, none
  FPAGER_DEFAULT_SOUND daemon: default sound (default pda, none = silent)
  FPAGER_LEVEL_SOUNDS daemon: per-level default sounds (default fail=sms)
  FPAGER_LEVEL_VOLUMES daemon: per-level sound volume 0..1 (default ok=0.25)
  FPAGER_SOUND      client: sound for send/run/wait (overrides the daemon default)
  FPAGER_BATTERY_LOW daemon: low-battery warning threshold, percent (default 15, 0 = off)
  FPAGER_HTTP       daemon: also serve the HTTP API on this address
  FPAGER_TOKEN      HTTP API bearer token (daemon and remote clients)
  FPAGER_URL        client: send to a remote daemon, e.g. http://192.168.1.10:9732
  Unset FPAGER_* variables are read from ~/.config/fpager/env.

Buttons on the Flipper: OK = acknowledge, Back / long OK = acknowledge all,
Left/Right = previous/next notification, Up/Down = scroll.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	loadEnvFile()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, args := os.Args[1], os.Args[2:]
	var err error
	code := 0
	switch cmd {
	case "daemon":
		err = cmdDaemon(ctx, args)
	case "send":
		err = cmdSend(ctx, args)
	case "run":
		code, err = cmdRun(ctx, args)
	case "wait":
		err = cmdWait(ctx, args)
	case "status":
		err = cmdStatus(ctx, args)
	case "probe":
		err = cmdProbe(ctx, args)
	case "claude-hook":
		err = cmdClaudeHook(ctx, args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fpager:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func defaultUSB() string {
	if v := os.Getenv("FPAGER_USB"); v != "" {
		return v
	}
	m, _ := filepath.Glob("/dev/serial/by-id/usb-Flipper_Devices*")
	if len(m) > 0 {
		return m[0]
	}
	return "/dev/ttyACM0"
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage of fpager %s:\n", name)
		fs.PrintDefaults()
	}
	return fs
}

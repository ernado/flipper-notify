// Package focus tells whether the user is looking at a given terminal tab.
//
// GNOME on Wayland hides the focused window from clients, so this uses the
// accessibility bus (AT-SPI): the terminal emulator that is an ancestor of the
// current process must have an ACTIVE window whose title (= active tab title)
// contains one of the expected titles. Screen lock and idle time override it.
package focus

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/godbus/dbus/v5"
)

// Result explains the decision.
type Result struct {
	Focused bool          // user is looking at the tab
	Reason  string        // human-readable explanation
	Idle    time.Duration // input idle time, if known
}

// Check reports whether a window of an ancestor process of pid is active and
// its title contains one of titles, the screen is unlocked and the user was
// active within maxIdle.
func Check(ctx context.Context, pid int, titles []string, maxIdle time.Duration) (Result, error) {
	var nonEmpty []string
	for _, t := range titles {
		if t = strings.TrimSpace(t); t != "" {
			nonEmpty = append(nonEmpty, t)
		}
	}
	if len(nonEmpty) == 0 {
		return Result{Reason: "session title unknown"}, nil
	}

	session, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return Result{}, errors.Wrap(err, "session bus")
	}
	defer func() { _ = session.Close() }()

	var locked bool
	if err := session.Object("org.gnome.ScreenSaver", "/org/gnome/ScreenSaver").
		CallWithContext(ctx, "org.gnome.ScreenSaver.GetActive", 0).Store(&locked); err == nil && locked {
		return Result{Reason: "screen locked"}, nil
	}
	var idleMS uint64
	idleKnown := session.Object("org.gnome.Mutter.IdleMonitor", "/org/gnome/Mutter/IdleMonitor/Core").
		CallWithContext(ctx, "org.gnome.Mutter.IdleMonitor.GetIdletime", 0).Store(&idleMS) == nil
	idle := time.Duration(idleMS) * time.Millisecond
	if idleKnown && maxIdle > 0 && idle > maxIdle {
		return Result{Reason: "user idle", Idle: idle}, nil
	}

	var addr string
	if err := session.Object("org.a11y.Bus", "/org/a11y/bus").
		CallWithContext(ctx, "org.a11y.Bus.GetAddress", 0).Store(&addr); err != nil {
		return Result{}, errors.Wrap(err, "a11y bus address")
	}
	a11y, err := dbus.Connect(addr, dbus.WithContext(ctx))
	if err != nil {
		return Result{}, errors.Wrap(err, "a11y bus")
	}
	defer func() { _ = a11y.Close() }()

	ancestors := Ancestors(pid)
	apps, err := children(ctx, a11y, ref{Name: "org.a11y.atspi.Registry", Path: "/org/a11y/atspi/accessible/root"})
	if err != nil {
		return Result{}, errors.Wrap(err, "list applications")
	}
	sawTerminal := false
	for _, app := range apps {
		var appPID uint32
		if err := a11y.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetConnectionUnixProcessID", 0, app.Name).
			Store(&appPID); err != nil || !ancestors[int(appPID)] {
			continue
		}
		sawTerminal = true
		wins, err := children(ctx, a11y, app)
		if err != nil {
			continue
		}
		for _, w := range wins {
			title, active, err := window(ctx, a11y, w)
			if err != nil || !active {
				continue
			}
			for _, t := range nonEmpty {
				if strings.Contains(title, t) {
					return Result{Focused: true, Reason: "terminal tab active", Idle: idle}, nil
				}
			}
			return Result{Reason: "terminal active, other tab", Idle: idle}, nil
		}
	}
	if !sawTerminal {
		return Result{Reason: "terminal not found on accessibility bus", Idle: idle}, nil
	}
	return Result{Reason: "terminal not active", Idle: idle}, nil
}

type ref struct {
	Name string
	Path dbus.ObjectPath
}

func children(ctx context.Context, conn *dbus.Conn, r ref) ([]ref, error) {
	var out []ref
	err := conn.Object(r.Name, r.Path).CallWithContext(ctx, "org.a11y.atspi.Accessible.GetChildren", 0).Store(&out)
	return out, err
}

const stateActive = 1 // ATSPI_STATE_ACTIVE

func window(ctx context.Context, conn *dbus.Conn, r ref) (title string, active bool, err error) {
	obj := conn.Object(r.Name, r.Path)
	var states []uint32
	if err := obj.CallWithContext(ctx, "org.a11y.atspi.Accessible.GetState", 0).Store(&states); err != nil {
		return "", false, err
	}
	active = len(states) > 0 && states[0]&(1<<stateActive) != 0
	v, err := obj.GetProperty("org.a11y.atspi.Accessible.Name")
	if err != nil {
		return "", active, err
	}
	title, _ = v.Value().(string)
	return title, active, nil
}

// Ancestors returns pid and all its ancestors (from /proc).
func Ancestors(pid int) map[int]bool {
	out := map[int]bool{}
	for i := 0; pid > 1 && i < 64; i++ {
		out[pid] = true
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			break
		}
		// pid (comm) state ppid ...; comm may contain spaces/parens.
		s := string(b)
		f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
		if len(f) < 2 {
			break
		}
		pid, _ = strconv.Atoi(f[1])
	}
	return out
}

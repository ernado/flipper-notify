package pager

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/ernado/flipper-notify/internal/pb/gui"
	"github.com/ernado/flipper-notify/internal/screen"
)

// newTestPager returns a pager with n pending notifications and the display marked on.
// With no client attached, show/hide are no-ops, so only the state machine is exercised.
func newTestPager(t *testing.T, n int) *Pager {
	t.Helper()
	p := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	for range n {
		reply := make(chan uint64, 1)
		p.handle(evNotify{n: Notification{Title: "t", Body: strings.Repeat("line ", 40)}, reply: reply})
		<-reply
	}
	p.display = true
	return p
}

func TestInputNavigation(t *testing.T) {
	p := newTestPager(t, 3)
	if p.cur != 2 {
		t.Fatalf("newest should be current, got %d", p.cur)
	}
	p.input(gui.InputKey_LEFT, gui.InputType_SHORT)
	p.input(gui.InputKey_LEFT, gui.InputType_SHORT)
	p.input(gui.InputKey_LEFT, gui.InputType_SHORT)
	if p.cur != 0 {
		t.Fatalf("left should clamp at 0, got %d", p.cur)
	}
	p.input(gui.InputKey_DOWN, gui.InputType_SHORT)
	p.input(gui.InputKey_DOWN, gui.InputType_REPEAT)
	if p.scroll != 2 {
		t.Fatalf("scroll = %d", p.scroll)
	}
	p.input(gui.InputKey_RIGHT, gui.InputType_SHORT)
	if p.cur != 1 || p.scroll != 0 {
		t.Fatalf("right: cur=%d scroll=%d", p.cur, p.scroll)
	}
	// Press/release must not act.
	p.input(gui.InputKey_OK, gui.InputType_PRESS)
	p.input(gui.InputKey_OK, gui.InputType_RELEASE)
	if len(p.pending) != 3 {
		t.Fatal("press/release acted")
	}
	p.input(gui.InputKey_OK, gui.InputType_SHORT)
	if len(p.pending) != 2 || p.cur != 1 {
		t.Fatalf("ok: len=%d cur=%d", len(p.pending), p.cur)
	}
	if p.pending[0].ID != 1 || p.pending[1].ID != 3 {
		t.Fatalf("wrong one removed: %d %d", p.pending[0].ID, p.pending[1].ID)
	}
	p.input(gui.InputKey_BACK, gui.InputType_SHORT)
	if len(p.pending) != 0 {
		t.Fatal("back should acknowledge all")
	}
}

func TestMaxPending(t *testing.T) {
	p := newTestPager(t, MaxPending+5)
	if len(p.pending) != MaxPending || p.pending[0].ID != 6 {
		t.Fatalf("len=%d first=%d", len(p.pending), p.pending[0].ID)
	}
}

func TestBatteryLow(t *testing.T) {
	p := newTestPager(t, 0)
	feed := func(level int, state string) {
		p.handle(evBattery{client: p.client, b: Battery{Level: level, State: state}})
	}
	feed(40, "discharging")
	feed(14, "charging") // charging: no warning
	if len(p.pending) != 0 {
		t.Fatal("warned while charging")
	}
	feed(14, "discharging")
	feed(12, "discharging")
	if len(p.pending) != 1 || !strings.Contains(p.pending[0].Body, "14%") {
		t.Fatalf("want one warning at 14%%, got %d", len(p.pending))
	}
	feed(17, "discharging") // below re-arm level (20): still armed off
	feed(13, "discharging")
	if len(p.pending) != 1 {
		t.Fatal("warned twice without recovery")
	}
	feed(30, "charging")
	feed(10, "discharging")
	if len(p.pending) != 2 {
		t.Fatalf("want second warning after charging, got %d", len(p.pending))
	}
	p.SetBatteryLow(0)
	p.batteryWarned = false
	feed(5, "discharging")
	if len(p.pending) != 2 {
		t.Fatal("warned while disabled")
	}
}

func TestLevelSounds(t *testing.T) {
	p := newTestPager(t, 0)
	if err := p.SetLevelSounds("fail=sms, ok=none"); err != nil {
		t.Fatal(err)
	}
	if p.levelSounds[screen.LevelFail] != "sms" || p.levelSounds[screen.LevelOK] != "none" {
		t.Fatalf("parsed %v", p.levelSounds)
	}
	for _, bad := range []string{"fail", "bogus=sms", "fail=nosuch"} {
		if err := p.SetLevelSounds(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestSignalHysteresis(t *testing.T) {
	var buf strings.Builder
	p := New(slog.New(slog.NewTextHandler(&buf, nil)))
	for _, rssi := range []int{-60, -82, -85, -78, -76, -74, -81} {
		p.updateSignal(rssi)
	}
	if p.signal.RSSI != -81 || p.signal.Min != -85 || !p.weak {
		t.Fatalf("signal %+v weak %v", *p.signal, p.weak)
	}
	out := buf.String()
	if n := strings.Count(out, "Weak signal"); n != 2 {
		t.Errorf("weak warnings: %d\n%s", n, out)
	}
	if n := strings.Count(out, "Signal recovered"); n != 1 || !strings.Contains(out, "Signal recovered\" rssi=-74") {
		t.Errorf("recovered logs: %d\n%s", n, out)
	}
}

func TestWantBlink(t *testing.T) {
	p := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if got := p.wantBlink(); got != "off" {
		t.Fatalf("empty: %q", got)
	}
	for _, c := range []struct {
		add  screen.Level
		want string
	}{
		{screen.LevelInfo, "info"},
		{screen.LevelFail, "fail"},
		{screen.LevelOK, "fail"},
	} {
		p.pending = append(p.pending, &Notification{Level: c.add})
		if got := p.wantBlink(); got != c.want {
			t.Errorf("after %v: got %q, want %q", c.add, got, c.want)
		}
	}
	p.pending = p.pending[2:] // only the ok one left
	if got := p.wantBlink(); got != "ok" {
		t.Errorf("ok only: %q", got)
	}
}

func TestLevelVolumes(t *testing.T) {
	p := newTestPager(t, 0)
	if err := p.SetLevelVolumes("ok=0.5, info=1"); err != nil {
		t.Fatal(err)
	}
	if p.levelVolumes[screen.LevelOK] != 0.5 || p.levelVolumes[screen.LevelInfo] != 1 {
		t.Fatalf("parsed %v", p.levelVolumes)
	}
	if _, ok := p.levelVolumes[screen.LevelFail]; ok {
		t.Fatal("fail should be unset")
	}
	for _, bad := range []string{"ok", "bogus=0.5", "ok=loud", "ok=1.5", "ok=-0.1"} {
		if err := p.SetLevelVolumes(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

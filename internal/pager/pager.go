// Package pager keeps pending notifications and drives the Flipper display.
//
// All state is owned by the Run goroutine; everything else talks to it via events.
package pager

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"

	"github.com/ernado/flipper-notify/internal/fap"
	"github.com/ernado/flipper-notify/internal/pb/flipper"
	"github.com/ernado/flipper-notify/internal/pb/gui"
	"github.com/ernado/flipper-notify/internal/rpc"
	"github.com/ernado/flipper-notify/internal/screen"
)

// MaxPending is the number of unacknowledged notifications kept; older ones are dropped.
const MaxPending = 50

const callTimeout = 10 * time.Second

// Notification is a single page.
type Notification struct {
	ID      uint64       `json:"id"`
	Title   string       `json:"title"`
	Body    string       `json:"body"`
	Level   screen.Level `json:"level"`
	Silent  bool         `json:"silent,omitempty"`
	Sound   string       `json:"sound,omitempty"` // embedded tone file name, played with the vibro alert
	Created time.Time    `json:"created"`

	alerted bool
}

// Status is a snapshot of the pager state.
type Status struct {
	Connected bool           `json:"connected"`
	Transport string         `json:"transport,omitempty"`
	Since     time.Time      `json:"since,omitzero"`
	Display   bool           `json:"display"`
	Alert     string         `json:"alert"`
	Battery   *Battery       `json:"battery,omitempty"`
	Signal    *Signal        `json:"signal,omitempty"`
	Pending   []Notification `json:"pending"`
}

// Battery is the last known Flipper battery state.
type Battery struct {
	Level   int       `json:"level"` // percent
	State   string    `json:"state"` // charging, discharging, charged
	Updated time.Time `json:"updated"`
}

// Signal is the BLE link quality of the current session, as seen by the host controller.
type Signal struct {
	RSSI    int       `json:"rssi"`     // dBm, last sample
	Min     int       `json:"min_rssi"` // dBm, weakest sample this session
	Updated time.Time `json:"updated"`
}

// RSSI thresholds (dBm) for weak-signal logging, with hysteresis.
const (
	weakRSSI    = -80
	recoverRSSI = -75
)

type (
	evNotify struct {
		n     Notification
		reply chan uint64
	}
	evInput struct {
		client *rpc.Client
		key    gui.InputKey
		typ    gui.InputType
	}
	evConnected struct {
		client    *rpc.Client
		transport string
		rssi      *int // nil if unknown (USB or read failed)
	}
	evSignal struct {
		client *rpc.Client
		rssi   int
	}
	evDisconnected struct{ client *rpc.Client }
	evStatus       struct{ reply chan Status }
	evBattery      struct {
		client *rpc.Client
		b      Battery
	}
)

// Alert modes.
const (
	AlertVibro = "vibro" // companion app: vibration + LED, no sound
	AlertFull  = "full"  // built-in audiovisual alert: vibration, sound, LED
	AlertNone  = "none"
)

// Pager is the notification state machine.
type Pager struct {
	log    *slog.Logger
	font   *screen.Font
	events chan any

	// Owned by Run.
	pending   []*Notification
	cur       int
	scroll    int
	client    *rpc.Client
	transport string
	since     time.Time
	display   bool
	nextID    uint64
	alertMode string
	vibroOK   bool   // companion app installed on the current session's device
	blink     string // unread-LED blink level last set on the device: "", "off", or a level; "" = unknown
	battery   *Battery
	signal    *Signal
	weak      bool // weak-signal warning logged for the current session

	defaultSound  string                   // used when a notification has no sound; "" = none
	levelSounds   map[screen.Level]string  // per-level default, overrides defaultSound
	levelVolumes  map[screen.Level]float64 // per-level sound volume scale 0..1; missing = 1
	batteryLow    int                      // warn below this percent while discharging; 0 = off
	batteryWarned bool                     // warning sent, re-armed after charging or recovery
}

// batteryRearm is how far above the threshold the level must recover to warn again.
const batteryRearm = 5

// New creates a pager with the given alert mode (AlertVibro by default).
func New(log *slog.Logger) *Pager {
	return &Pager{
		log:        log,
		font:       screen.DefaultFont(),
		events:     make(chan any, 64),
		alertMode:  AlertVibro,
		batteryLow: 15,
	}
}

// SetDefaultSound sets the sound for notifications that don't name one ("" or "none" = silent).
func (p *Pager) SetDefaultSound(name string) error {
	if name == "none" {
		name = ""
	}
	if name != "" && fap.SoundPath(name) == "" {
		return errors.Errorf("unknown sound %q (have: %s)", name, strings.Join(fap.Sounds(), ", "))
	}
	p.defaultSound = name
	return nil
}

// SetLevelVolumes sets per-level sound volume scales from "level=v,level=v" (0..1, e.g. "ok=0.5").
func (p *Pager) SetLevelVolumes(spec string) error {
	m := map[screen.Level]float64{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return errors.Errorf("bad level volume %q (want level=0..1)", part)
		}
		lvl, err := screen.ParseLevel(strings.TrimSpace(k))
		if err != nil {
			return err
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || f < 0 || f > 1 {
			return errors.Errorf("bad volume %q for %s (want 0..1)", v, lvl)
		}
		m[lvl] = f
	}
	p.levelVolumes = m
	return nil
}

// SetLevelSounds sets per-level default sounds from "level=sound,level=sound" (e.g. "fail=sms").
func (p *Pager) SetLevelSounds(spec string) error {
	m := map[screen.Level]string{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return errors.Errorf("bad level sound %q (want level=sound)", part)
		}
		lvl, err := screen.ParseLevel(strings.TrimSpace(k))
		if err != nil {
			return err
		}
		v = strings.TrimSpace(v)
		if !fap.Valid(v) {
			return errors.Errorf("unknown sound %q (have: %s, none)", v, strings.Join(fap.Sounds(), ", "))
		}
		m[lvl] = v
	}
	p.levelSounds = m
	return nil
}

// SetBatteryLow sets the low-battery warning threshold in percent (0 disables); call before Run.
func (p *Pager) SetBatteryLow(percent int) { p.batteryLow = percent }

// SetAlert sets the alert mode; call before Run.
func (p *Pager) SetAlert(mode string) error {
	switch mode {
	case AlertVibro, AlertFull, AlertNone:
		p.alertMode = mode
		return nil
	}
	return errors.Errorf("unknown alert mode %q (vibro, full, none)", mode)
}

// Notify queues a notification and returns its ID.
func (p *Pager) Notify(ctx context.Context, n Notification) (uint64, error) {
	reply := make(chan uint64, 1)
	select {
	case p.events <- evNotify{n: n, reply: reply}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	select {
	case id := <-reply:
		return id, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Status returns the current state.
func (p *Pager) Status(ctx context.Context) (Status, error) {
	reply := make(chan Status, 1)
	select {
	case p.events <- evStatus{reply: reply}:
	case <-ctx.Done():
		return Status{}, ctx.Err()
	}
	select {
	case s := <-reply:
		return s, nil
	case <-ctx.Done():
		return Status{}, ctx.Err()
	}
}

// onEvent is the rpc.Client event callback; it must not block.
func (p *Pager) onEvent(c *rpc.Client, m *flipper.Main) {
	in := m.GetGuiSendInputEventRequest()
	if in == nil {
		return
	}
	select {
	case p.events <- evInput{client: c, key: in.GetKey(), typ: in.GetType()}:
	default:
		p.log.Warn("Input event dropped: queue full")
	}
}

// Run processes events until ctx is done.
func (p *Pager) Run(ctx context.Context) error {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			p.hide()
			return ctx.Err()
		case <-tick.C:
			if p.display {
				p.show() // refresh age
			}
		case ev := <-p.events:
			p.handle(ev)
		}
	}
}

func (p *Pager) handle(ev any) {
	switch ev := ev.(type) {
	case evNotify:
		p.nextID++
		n := ev.n
		n.ID = p.nextID
		if n.Created.IsZero() {
			n.Created = time.Now().UTC()
		}
		p.pending = append(p.pending, &n)
		if len(p.pending) > MaxPending {
			p.pending = p.pending[len(p.pending)-MaxPending:]
		}
		p.cur, p.scroll = len(p.pending)-1, 0
		ev.reply <- n.ID
		p.log.Info("Notification", "id", n.ID, "title", n.Title, "level", n.Level.String(), "connected", p.client != nil)
		p.alert()
		p.show()
	case evStatus:
		s := Status{Connected: p.client != nil, Transport: p.transport, Since: p.since, Display: p.display, Alert: p.alertMode, Battery: p.battery}
		if p.signal != nil {
			sig := *p.signal
			s.Signal = &sig
		}
		for _, n := range p.pending {
			s.Pending = append(s.Pending, *n)
		}
		ev.reply <- s
	case evConnected:
		p.client, p.transport, p.since, p.display = ev.client, ev.transport, time.Now().UTC(), false
		p.signal, p.weak = nil, false
		attrs := []any{"transport", ev.transport, "pending", len(p.pending)}
		if ev.rssi != nil {
			p.updateSignal(*ev.rssi)
			attrs = append(attrs, "rssi", *ev.rssi)
		}
		p.log.Info("Connected", attrs...)
		p.vibroOK, p.blink = false, ""
		if p.alertMode == AlertVibro {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			wrote, err := fap.Ensure(ctx, p.client)
			cancel()
			if err != nil {
				p.log.Warn("Companion app install failed, falling back to full alert", "err", err)
			} else {
				p.vibroOK = true
				if wrote > 0 {
					p.log.Info("Companion app files installed", "files", wrote, "dir", fap.Dir)
				}
			}
		}
		if len(p.pending) > 0 {
			p.alert()
			p.show()
		} else {
			p.syncBlink() // clear a blink left from a previous session
		}
	case evDisconnected:
		if ev.client == p.client {
			attrs := []any{"transport", p.transport, "err", ev.client.Err()}
			if sig := p.signal; sig != nil {
				attrs = append(attrs, "rssi", sig.RSSI, "min_rssi", sig.Min, "rssi_age", time.Since(sig.Updated).Round(time.Second))
			}
			p.log.Info("Disconnected", attrs...)
			p.client, p.transport, p.display, p.signal = nil, "", false, nil
		}
	case evBattery:
		if ev.client == p.client {
			b := ev.b
			p.battery = &b
			p.checkBattery(b)
		}
	case evSignal:
		if ev.client == p.client {
			p.updateSignal(ev.rssi)
		}
	case evInput:
		p.log.Debug("Input", "key", ev.key.String(), "type", ev.typ.String())
		if ev.client == p.client {
			p.input(ev.key, ev.typ)
		}
	}
}

// updateSignal records an RSSI sample and logs weak/recovered transitions.
func (p *Pager) updateSignal(rssi int) {
	now := time.Now().UTC()
	if p.signal == nil {
		p.signal = &Signal{RSSI: rssi, Min: rssi, Updated: now}
	} else {
		p.signal.RSSI, p.signal.Updated = rssi, now
		p.signal.Min = min(p.signal.Min, rssi)
	}
	p.log.Debug("Signal", "rssi", rssi)
	switch {
	case !p.weak && rssi < weakRSSI:
		p.weak = true
		p.log.Warn("Weak signal", "rssi", rssi, "threshold", weakRSSI)
	case p.weak && rssi >= recoverRSSI:
		p.weak = false
		p.log.Info("Signal recovered", "rssi", rssi, "min_rssi", p.signal.Min)
	}
}

// alert buzzes once for notifications that have not alerted yet, using the most
// severe level among them.
func (p *Pager) alert() {
	if p.client == nil {
		return
	}
	need, lvl, sound := false, screen.LevelInfo, ""
	for _, n := range p.pending {
		if !n.alerted {
			n.alerted = true
			if !n.Silent {
				need = true
				if severity(n.Level) >= severity(lvl) {
					lvl = n.Level
					if n.Sound != "" || sound == "" {
						sound = n.Sound
					}
				}
			}
		}
	}
	if !need || p.alertMode == AlertNone {
		p.syncBlink()
		return
	}
	var err error
	if p.alertMode == AlertVibro && p.vibroOK {
		blink := p.wantBlink()
		args := lvl.String() + " light blink:" + blink
		if sound == "" {
			if s, ok := p.levelSounds[lvl]; ok {
				sound = s
			} else {
				sound = p.defaultSound
			}
		}
		if sound == "none" {
			sound = ""
		}
		if sound != "" {
			if sp := fap.SoundPath(sound); sp != "" {
				if v, ok := p.levelVolumes[lvl]; ok && v != 1 {
					args += " vol:" + strconv.FormatFloat(v, 'f', -1, 64)
				}
				args += " " + sp
			} else {
				p.log.Warn("Unknown sound, ignoring", "sound", sound, "known", fap.Sounds())
			}
		}
		if err = p.startVibro(args); err == nil {
			p.blink = blink
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		err = p.client.Alert(ctx)
		cancel()
	}
	p.alertErr(err)
}

// wantBlink is the unread-LED state for the current pending list: the most severe level, or "off".
func (p *Pager) wantBlink() string {
	if len(p.pending) == 0 {
		return "off"
	}
	lvl := screen.LevelInfo
	for _, n := range p.pending {
		if severity(n.Level) > severity(lvl) {
			lvl = n.Level
		}
	}
	return lvl.String()
}

// syncBlink updates the unread-LED blink on the device if it differs from wantBlink.
func (p *Pager) syncBlink() {
	if p.client == nil || p.alertMode != AlertVibro || !p.vibroOK {
		return
	}
	want := p.wantBlink()
	if want == p.blink {
		return
	}
	err := p.startVibro("none blink:" + want)
	if err == nil {
		p.blink = want
	}
	p.alertErr(err)
}

// startVibro launches the companion app. A previous run may still be playing (the
// Flipper runs one app at a time), so APP_SYSTEM_LOCKED is retried briefly.
func (p *Pager) startVibro(args string) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	for deadline := time.Now().Add(2 * time.Second); ; {
		err := p.client.StartApp(ctx, fap.VibroPath, args)
		var se *rpc.StatusError
		if !errors.As(err, &se) || se.Status != flipper.CommandStatus_ERROR_APP_SYSTEM_LOCKED || time.Now().After(deadline) {
			return err
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// alertErr logs device-side refusals and drops the session on transport errors.
func (p *Pager) alertErr(err error) {
	var se *rpc.StatusError
	switch {
	case err == nil:
	case errors.As(err, &se):
		// Device-side refusal (e.g. another app is running): the session is fine.
		p.log.Warn("Alert refused by device", "err", err)
	default:
		p.fail(err)
	}
}

// checkBattery raises one notification when the battery drops below the threshold
// while not charging; it re-arms once charging or recovered above threshold+batteryRearm.
func (p *Pager) checkBattery(b Battery) {
	if p.batteryLow <= 0 {
		return
	}
	charging := b.State == "charging" || b.State == "charged"
	switch {
	case p.batteryWarned && (charging || b.Level >= p.batteryLow+batteryRearm):
		p.batteryWarned = false
	case !p.batteryWarned && !charging && b.Level < p.batteryLow:
		p.batteryWarned = true
		p.log.Warn("Battery low", "level", b.Level, "state", b.State)
		reply := make(chan uint64, 1)
		p.handle(evNotify{n: Notification{
			Title: "Flipper battery",
			Body:  fmt.Sprintf("Battery low: %d%% (%s).\nCharge the Flipper soon.", b.Level, b.State),
			Level: screen.LevelFail,
		}, reply: reply})
	}
}

func severity(l screen.Level) int {
	switch l {
	case screen.LevelFail:
		return 2
	case screen.LevelOK:
		return 1
	default:
		return 0
	}
}

func (p *Pager) input(key gui.InputKey, typ gui.InputType) {
	if !p.display || len(p.pending) == 0 {
		return
	}
	repeat := typ == gui.InputType_SHORT || typ == gui.InputType_REPEAT
	switch {
	case key == gui.InputKey_UP && repeat:
		p.scroll = max(p.scroll-1, 0)
	case key == gui.InputKey_DOWN && repeat:
		p.scroll = min(p.scroll+1, screen.MaxScroll(p.font, p.pending[p.cur].Body))
	case key == gui.InputKey_LEFT && typ == gui.InputType_SHORT:
		p.cur, p.scroll = max(p.cur-1, 0), 0
	case key == gui.InputKey_RIGHT && typ == gui.InputType_SHORT:
		p.cur, p.scroll = min(p.cur+1, len(p.pending)-1), 0
	case key == gui.InputKey_OK && typ == gui.InputType_SHORT:
		p.log.Info("Acknowledged", "id", p.pending[p.cur].ID)
		p.pending = append(p.pending[:p.cur], p.pending[p.cur+1:]...)
		p.cur, p.scroll = min(p.cur, len(p.pending)-1), 0
	case key == gui.InputKey_BACK && typ == gui.InputType_SHORT,
		key == gui.InputKey_OK && typ == gui.InputType_LONG:
		p.log.Info("Acknowledged all", "count", len(p.pending))
		p.pending, p.cur, p.scroll = nil, 0, 0
	default:
		return
	}
	p.syncBlink()
	if len(p.pending) == 0 {
		p.hide()
		return
	}
	p.show()
}

func (p *Pager) show() {
	if p.client == nil || len(p.pending) == 0 {
		return
	}
	n := p.pending[p.cur]
	frame := screen.Render(p.font, screen.Page{
		Title:  n.Title,
		Body:   n.Body,
		Level:  n.Level,
		Index:  p.cur + 1,
		Total:  len(p.pending),
		Age:    time.Since(n.Created),
		Scroll: p.scroll,
	}).XBM()
	if p.display {
		if err := p.client.Frame(frame); err != nil {
			p.fail(err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if err := p.client.StartVirtualDisplay(ctx, frame, true); err != nil {
		p.fail(err)
		return
	}
	p.display = true
}

func (p *Pager) hide() {
	if p.client == nil || !p.display {
		return
	}
	p.display = false
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if err := p.client.StopVirtualDisplay(ctx); err != nil {
		p.fail(err)
	}
}

// fail drops a broken session; the connection manager reconnects.
func (p *Pager) fail(err error) {
	p.log.Warn("RPC failed, dropping session", "err", err)
	if p.client != nil {
		_ = p.client.Close()
	}
	p.client, p.transport, p.display = nil, "", false
}

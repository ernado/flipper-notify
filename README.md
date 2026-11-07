# flipper-notify

Turn a [Flipper Zero](https://flipperzero.one) into a pager. Long builds, backups, CI jobs,
scripts on other machines: anything that can run a command or make an HTTP request can buzz the
Flipper on your desk or in your pocket.

```
fpager run -- make -j16                 # notify when the build finishes (ok / fail, duration)
fpager send -l fail -t backup "disk full"
curl -H "Authorization: Bearer $TOKEN" -d 'deploy done' "http://pager-host:9732/v1/notify?level=ok"
```

- **Bluetooth LE** (USB as fallback). Uses the stock firmware's RPC protocol, so no custom
  firmware is needed.
- **Fullscreen notification** on the Flipper with buttons: OK = acknowledge, Back = acknowledge
  all, Left/Right = previous/next, Up/Down = scroll. Unread notifications survive reconnects.
- **Vibration patterns + LED** per level (info: 1 buzz/blue, ok: 2/green, fail: 3/red), optional
  **sounds**, no sound at all if you prefer. Works in Stealth mode (vibration is forced, sound is
  skipped).
- **Remote hosts** via a small token-protected HTTP API.
- **Low-battery warning** (below 15%) and battery level in `fpager status`.
- Cyrillic/Latin/Greek text (embedded Terminus 6×12 font).

## How it works

```
 CLI / scripts ──unix socket──┐
                              ├─► fpager daemon ══ BLE serial (or USB CDC) ══► Flipper RPC
 other hosts ─────HTTP────────┘          │                                      ├─ virtual display (screen + buttons)
                                         └─ uploads companion app on connect ──└─ app_start fpager_vibro.fap (vibro/LED/tones)
```

The daemon keeps one RPC session open (varint-delimited protobuf `PB.Main` over the Flipper's
BLE serial service, or `start_rpc_session` on USB). The screen is a host-rendered 128×64 frame
shown with `gui_start_virtual_display` (input forwarded back to the host). The alert is a tiny
companion app (`fap/fpager_vibro`, ~150 lines of C) that the daemon embeds and uploads to
`/ext/apps_data/fpager/` whenever its md5 differs, then launches with `app_start`.

## Requirements

- Flipper Zero with official firmware **1.5.1** (API 88.2); see [Firmware updates](#firmware-updates).
- Linux with BlueZ (tested on 5.83) for BLE; for USB, the user must be in `dialout`.
- Go 1.26+ to build.

## Install

```
go install github.com/ernado/flipper-notify/cmd/fpager@latest     # or: make build
```

1. Pair the Flipper once (Settings → Bluetooth on; the PIN is shown on the Flipper):
   ```
   bluetoothctl
   > agent KeyboardOnly
   > default-agent
   > scan le          # note the "Flipper <name>" address
   > pair AA:BB:CC:DD:EE:FF
   > trust AA:BB:CC:DD:EE:FF
   ```
   Only one BLE central can be connected: the Flipper mobile app and fpager exclude each other.
2. Configure and start the daemon as a user service:
   ```
   mkdir -p ~/.config/fpager && echo FPAGER_MAC=AA:BB:CC:DD:EE:FF > ~/.config/fpager/env
   cp contrib/fpager.service ~/.config/systemd/user/   # ExecStart=%h/.local/bin/fpager daemon
   systemctl --user enable --now fpager
   fpager status
   ```
3. Try it: `fpager send "hello"`.

## Usage

```
fpager send    [-t title] [-l info|ok|fail] [-s] [-sound name|none] [message...]   (stdin if omitted)
fpager run     [-t title] [-min 1m] [-s] [-sound ...] -- cmd args                   (exit status + duration)
fpager wait    [-t title] PID                                                      (an existing process)
fpager status  [-json]
fpager daemon  [flags]
fpager probe   [-transport usb|ble] [-alert] [-capture screen.png] [-play x.tones] (direct, for testing)
fpager claude-hook                                                                 (Claude Code hooks, see below)
```

`-s` = silent (screen only). Configuration is read from flags, environment, or
`~/.config/fpager/env` (`KEY=VALUE` lines):

| Variable | Default | Meaning |
|---|---|---|
| `FPAGER_MAC` | – | BLE address of the bonded Flipper |
| `FPAGER_TRANSPORT` | `auto` | `auto` (BLE, then USB), `ble`, `usb` |
| `FPAGER_USB` | `/dev/serial/by-id/usb-Flipper_Devices*` | USB CDC device |
| `FPAGER_ALERT` | `vibro` | `vibro` (companion app), `full` (built-in alert with beeps), `none` |
| `FPAGER_DEFAULT_SOUND` | `pda` | sound for notifications without one (`none` = silent) |
| `FPAGER_LEVEL_SOUNDS` | `fail=sms` | per-level default sounds |
| `FPAGER_LEVEL_VOLUMES` | `ok=0.25` | per-level sound volume, 0..1 (e.g. `ok=0.5,info=0.8`) |
| `FPAGER_BATTERY_LOW` | `15` | low-battery warning threshold, % (`0` = off) |
| `FPAGER_HTTP` / `FPAGER_TOKEN` | – | daemon: serve the HTTP API on this address with this bearer token |
| `FPAGER_URL` / `FPAGER_TOKEN` | – | client: send to a remote daemon instead of the local socket |
| `FPAGER_SOCKET` | `$XDG_RUNTIME_DIR/fpager.sock` | daemon socket |

### HTTP API (other hosts)

Run the daemon with `FPAGER_HTTP=<lan-ip>:9732` and a random `FPAGER_TOKEN`
(e.g. `head -c32 /dev/urandom | od -An -tx1 | tr -d ' \n'`). Every request needs
`Authorization: Bearer <token>`. Plain HTTP: use it on a trusted LAN or behind a tunnel.

- `POST /v1/notify`: JSON `{"title","body","level","silent","sound"}`, or a text body with
  `?title=&level=&silent=1&sound=` query parameters. Title defaults to the caller's IP.
- `GET /v1/status`: connection, battery, pending notifications.

On another host, `FPAGER_URL=http://<lan-ip>:9732` + the same `FPAGER_TOKEN` in its
`~/.config/fpager/env` makes `fpager send/run/wait/status` go over HTTP.

### Sounds

Tone files are `<freq_hz> <duration_ms> [volume 0..1]` lines (0 Hz = rest) in
`internal/fap/sounds/`, embedded and synced to the Flipper. Included:

| Name | Description |
|---|---|
| `pda` | five-note PDA-style chirp, balanced for the Flipper speaker (default) |
| `pda-loud` | same at full volume |
| `sms` | classic two-pairs SMS beeps (default for `fail`) |

Make your own from a short clip:

```
tools/soundfit.py clip.mp3 sounds/name --notes 5        # detect + fit notes → .tones, .mid, .wav preview
tools/micloop.py work/ clip.wav sounds/name.tones --mic <pipewire source>   # tune on the real speaker
```

`soundfit.py` fits square-wave notes to the clip's spectrogram by coordinate search.
`micloop.py` plays candidates on the Flipper over USB, records them with a microphone, and
adjusts durations and per-note volume. Things learned on the way:
- The speaker plays only `128000/N` Hz (TIM16 at 64 MHz/500, period `floor(128000/f)`), so the
  pitch always rounds up. `soundfit.py` writes pre-compensated frequencies.
- It is loud near 3 kHz and weak around 2 kHz. Volume is the PWM duty (`volume/2`); below 50%
  it adds even harmonics.

### Claude Code

`fpager claude-hook` reads a hook payload on stdin and notifies, unless you are looking at that
session's terminal tab. It checks via AT-SPI: the terminal emulator that started the hook has an
active window whose title contains the session title. The screen being locked or more than 5 min
of idle input counts as away. Example `~/.claude/settings.json`:

```json
{"hooks": {
  "Notification": [{"hooks": [{"type": "command", "command": "fpager claude-hook", "timeout": 10}]}],
  "Stop":         [{"hooks": [{"type": "command", "command": "fpager claude-hook", "timeout": 10}]}]
}}
```

### Shell

`. contrib/fpager.bash` in `~/.bashrc` reports interactive commands that ran ≥ 60 s
(`FPAGER_MIN`), ignoring editors, pagers, ssh, etc. (`FPAGER_IGNORE`).

## Firmware updates

The companion app is built for the firmware API it was compiled against. After updating the
Flipper, rebuild and redeploy: `pip install ufbt && make fap && make install`.

## Development

```
make build test
buf generate            # internal/pb from proto/ (flipperdevices/flipperzero-protobuf)
make fap                # companion app via ufbt
fpager probe -transport usb -capture screen.png   # render check: read the screen back
```

Layout: `cmd/fpager` (CLI, daemon) · `internal/rpc` (RPC client) · `internal/transport` (USB, BLE
with flow control) · `internal/pager` (state machine, reconnect) · `internal/screen` (renderer) ·
`internal/fap` (embedded app + sounds) · `internal/httpapi`, `internal/ipc` · `internal/focus`
(AT-SPI focus check) · `fap/fpager_vibro` (C) · `tools/` (sound fitting).

## Credits

- [flipperdevices/flipperzero-protobuf](https://github.com/flipperdevices/flipperzero-protobuf)
  and the [firmware](https://github.com/flipperdevices/flipperzero-firmware) (GPL-3.0).
- [Terminus Font](https://terminus-font.sourceforge.net/) by Dimitar Toshkov Zhekov, SIL OFL 1.1
  (`internal/screen/font/Terminus-LICENSE.txt`).
- [tinygo-org/bluetooth](https://github.com/tinygo-org/bluetooth), [go.bug.st/serial](https://github.com/bugst/go-serial).
- [awesome-flipperzero](https://github.com/djsime1/awesome-flipperzero) for pointers.

Not affiliated with Flipper Devices Inc.

## License

GPL-3.0, see [LICENSE](LICENSE). The embedded font is under the SIL Open Font License 1.1.

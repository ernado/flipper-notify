// Package fap embeds the companion Flipper app (fap/fpager_vibro, built with ufbt) and its
// tone files, and installs them on the device.
package fap

import (
	"context"
	"crypto/md5"
	"embed"
	"encoding/hex"
	"path"
	"sort"
	"strings"

	"github.com/go-faster/errors"

	"github.com/ernado/flipper-notify/internal/pb/flipper"
	"github.com/ernado/flipper-notify/internal/rpc"
)

// Vibro is fpager_vibro.fap, built for firmware API 88.2 (OFW 1.5.1). Rebuild: make fap.
//
//go:embed fpager_vibro.fap
var Vibro []byte

// Tone files ("<freq_hz> <duration_ms>" per line), made with tools/soundfit.py.
//
//go:embed sounds/*.tones
var sounds embed.FS

const (
	Dir       = "/ext/apps_data/fpager"
	VibroPath = Dir + "/fpager_vibro.fap"
	SoundDir  = Dir + "/sounds"
)

// Sounds lists the embedded sound names.
func Sounds() []string {
	entries, _ := sounds.ReadDir("sounds")
	var out []string
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".tones"))
	}
	sort.Strings(out)
	return out
}

// Valid reports whether name may be used as a notification sound: an embedded sound or "none".
func Valid(name string) bool { return name == "none" || SoundPath(name) != "" }

// SoundPath returns the device path of an embedded sound, or "" if there is no such sound.
func SoundPath(name string) string {
	if name == "" || strings.ContainsAny(name, "/ ") {
		return ""
	}
	if _, err := sounds.ReadFile("sounds/" + name + ".tones"); err != nil {
		return ""
	}
	return SoundDir + "/" + name + ".tones"
}

// Ensure uploads the app and sounds that are missing or differ. Reports how many files it wrote.
func Ensure(ctx context.Context, c *rpc.Client) (int, error) {
	if err := c.Mkdir(ctx, Dir); err != nil {
		return 0, err
	}
	wrote := 0
	ok, err := sync(ctx, c, VibroPath, Vibro)
	if err != nil {
		return wrote, err
	}
	if ok {
		wrote++
	}
	if err := c.Mkdir(ctx, SoundDir); err != nil {
		return wrote, err
	}
	for _, name := range Sounds() {
		data, err := sounds.ReadFile("sounds/" + name + ".tones")
		if err != nil {
			return wrote, errors.Wrap(err, "read embedded sound")
		}
		ok, err := sync(ctx, c, path.Join(SoundDir, name+".tones"), data)
		if err != nil {
			return wrote, err
		}
		if ok {
			wrote++
		}
	}
	return wrote, nil
}

// sync writes data to p unless the device already has an identical file.
func sync(ctx context.Context, c *rpc.Client, p string, data []byte) (bool, error) {
	sum := md5.Sum(data)
	want := hex.EncodeToString(sum[:])
	got, err := c.MD5(ctx, p)
	var se *rpc.StatusError
	switch {
	case err == nil && got == want:
		return false, nil
	case err == nil, errors.As(err, &se) && se.Status == flipper.CommandStatus_ERROR_STORAGE_NOT_EXIST:
	default:
		return false, err
	}
	if err := c.WriteFile(ctx, p, data); err != nil {
		return false, err
	}
	if got, err := c.MD5(ctx, p); err != nil || got != want {
		return true, errors.Errorf("verify %s: md5 %q, want %q (%v)", p, got, want, err)
	}
	return true, nil
}

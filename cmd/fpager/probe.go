package main

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"time"

	"github.com/go-faster/errors"

	"github.com/ernado/flipper-notify/internal/fap"
	"github.com/ernado/flipper-notify/internal/pb/flipper"
	"github.com/ernado/flipper-notify/internal/rpc"
	"github.com/ernado/flipper-notify/internal/screen"
)

func cmdProbe(ctx context.Context, args []string) error {
	fs := newFlags("probe")
	d := dialFlags(fs)
	alert := fs.Bool("alert", false, "play the audiovisual alert")
	hold := fs.Duration("hold", 5*time.Second, "how long to keep the test page and log button presses")
	capture := fs.String("capture", "", "save the device screen (while showing the test page) to this PNG")
	render := fs.String("render", "", "only render the test page locally to this PNG, no device")
	vibro := fs.String("vibro", "", "install the companion app and vibrate with this level (info, ok, fail)")
	sound := fs.String("sound", "", "with -vibro: also play this embedded sound")
	play := fs.String("play", "", "upload this local .tones file and play it (no vibration, no screen), then exit")
	props := fs.String("props", "", "print device properties with this prefix (e.g. pwrinfo, devinfo)")
	_ = fs.Parse(args)

	f := screen.DefaultFont()
	page := screen.Render(f, screen.Page{
		Title: "fpager probe",
		Body:  "Привет! Hello from fpager.\nOK=ack, ←/→ switch, ↑/↓ scroll. Long text wraps on words and scrolls with Up/Down keys.",
		Level: screen.LevelOK, Index: 1, Total: 3, Age: 42 * time.Second,
	})
	if *render != "" {
		return savePNG(*render, page)
	}

	start := time.Now()
	frames := make(chan []byte, 4)
	c, name, err := d.Connect(ctx, func(_ *rpc.Client, m *flipper.Main) {
		if in := m.GetGuiSendInputEventRequest(); in != nil {
			fmt.Printf("input: %s %s\n", in.GetKey(), in.GetType())
		}
		if fr := m.GetGuiScreenFrame(); fr != nil {
			select {
			case frames <- fr.GetData():
			default:
			}
		}
	})
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	fmt.Printf("connected via %s in %s (ping OK)\n", name, time.Since(start).Round(time.Millisecond))

	call := func(f func(context.Context) error) error {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return f(cctx)
	}
	if *play != "" {
		data, err := os.ReadFile(*play)
		if err != nil {
			return errors.Wrap(err, "read tones")
		}
		pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if _, err := fap.Ensure(pctx, c); err != nil {
			return err
		}
		dst := fap.SoundDir + "/_play.tones"
		if err := c.WriteFile(pctx, dst, data); err != nil {
			return err
		}
		if err := c.StartApp(pctx, fap.VibroPath, "none "+dst); err != nil {
			return err
		}
		fmt.Println("playing", dst)
		return nil
	}
	if *props != "" {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		m, err := c.Properties(pctx, *props)
		cancel()
		if err != nil {
			return err
		}
		for k, v := range m {
			fmt.Printf("%s = %s\n", k, v)
		}
		return nil
	}
	if *vibro != "" {
		vctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		wrote, err := fap.Ensure(vctx, c)
		cancel()
		if err != nil {
			return err
		}
		fmt.Printf("app %s (files uploaded: %d)\n", fap.VibroPath, wrote)
		args := *vibro
		if *sound != "" {
			args += " " + fap.SoundPath(*sound)
		}
		if err := call(func(ctx context.Context) error { return c.StartApp(ctx, fap.VibroPath, args) }); err != nil {
			return err
		}
		fmt.Println("vibro started")
	}
	if *alert {
		if err := call(c.Alert); err != nil {
			return err
		}
		fmt.Println("alert OK")
	}
	if err := call(func(ctx context.Context) error { return c.StartVirtualDisplay(ctx, page.XBM(), true) }); err != nil {
		return err
	}
	fmt.Println("virtual display started")

	if *capture != "" {
		if err := call(c.StartScreenStream); err != nil {
			return err
		}
		select {
		case data := <-frames:
			if err := savePNG(*capture, screen.FromFramebuffer(data)); err != nil {
				return err
			}
			fmt.Printf("captured %d bytes -> %s\n", len(data), *capture)
		case <-time.After(5 * time.Second):
			fmt.Println("no screen frame received")
		}
		if err := call(c.StopScreenStream); err != nil {
			return err
		}
	}

	select {
	case <-time.After(*hold):
	case <-ctx.Done():
	}
	if err := call(c.StopVirtualDisplay); err != nil {
		return err
	}
	if name == "usb" {
		_ = call(c.StopSession)
	}
	fmt.Println("done")
	return nil
}

func savePNG(path string, c *screen.Canvas) error {
	out, err := os.Create(path)
	if err != nil {
		return errors.Wrap(err, "create png")
	}
	if err := png.Encode(out, c.Image(4)); err != nil {
		_ = out.Close()
		return errors.Wrap(err, "encode png")
	}
	return out.Close()
}

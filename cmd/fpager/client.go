package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-faster/errors"

	"github.com/ernado/flipper-notify/internal/ipc"
	"github.com/ernado/flipper-notify/internal/pager"
	"github.com/ernado/flipper-notify/internal/screen"
)

func send(ctx context.Context, req ipc.Request) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if url := os.Getenv("FPAGER_URL"); url != "" {
		return remoteSend(ctx, url, os.Getenv("FPAGER_TOKEN"), req)
	}
	req.Op = "send"
	_, err := ipc.Do(ctx, ipc.SocketPath(), req)
	return err
}

func cmdSend(ctx context.Context, args []string) error {
	fs := newFlags("send")
	title := fs.String("t", "", "title (default: hostname)")
	level := fs.String("l", "info", "level: info, ok, fail")
	silent := fs.Bool("s", false, "silent: no vibration/sound")
	sound := fs.String("sound", os.Getenv("FPAGER_SOUND"), "sound: name (e.g. pda-loud), none = no sound; default: the daemon's (pda)")
	_ = fs.Parse(args)

	body := strings.Join(fs.Args(), " ")
	if fs.NArg() == 0 || body == "-" {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		if err != nil {
			return errors.Wrap(err, "read stdin")
		}
		body = strings.TrimRight(string(b), "\n")
	}
	if _, err := screen.ParseLevel(*level); err != nil {
		return err
	}
	if *title == "" {
		*title, _ = os.Hostname()
	}
	return send(ctx, ipc.Request{Title: *title, Body: body, Level: *level, Silent: *silent, Sound: *sound})
}

// shortDuration formats d as e.g. 3m12s, 1h05m, 850ms.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	case d < time.Hour:
		return d.Round(time.Second).String()
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func cmdRun(ctx context.Context, args []string) (int, error) {
	fs := newFlags("run")
	title := fs.String("t", "", "title (default: command name)")
	minDur := fs.Duration("min", 0, "notify only if the command ran at least this long")
	silent := fs.Bool("s", false, "silent: no vibration/sound")
	sound := fs.String("sound", os.Getenv("FPAGER_SOUND"), "sound: name (e.g. pda-loud), none = no sound; default: the daemon's (pda)")
	_ = fs.Parse(args)
	argv := fs.Args()
	if len(argv) == 0 {
		return 2, errors.New("run: no command given (use: fpager run -- cmd args)")
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	start := time.Now()
	runErr := cmd.Start()
	if runErr == nil {
		// Forward termination signals to the child; we only report.
		go func() {
			<-ctx.Done()
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
		}()
		runErr = cmd.Wait()
	}
	elapsed := time.Since(start)

	code := 0
	var status string
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		status = "exit 0"
	case errors.As(runErr, &exitErr):
		code = exitErr.ExitCode()
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			status = "killed by " + ws.Signal().String()
			code = 128 + int(ws.Signal())
		} else {
			status = "exit " + strconv.Itoa(code)
		}
	default:
		code = 127
		status = runErr.Error()
	}

	if elapsed < *minDur {
		return code, nil
	}
	if *title == "" {
		*title = filepath.Base(argv[0])
	}
	lvl := "ok"
	if code != 0 {
		lvl = "fail"
	}
	wd, _ := os.Getwd()
	body := fmt.Sprintf("%s in %s\n$ %s\n%s", status, shortDuration(elapsed), strings.Join(argv, " "), wd)
	if err := send(context.WithoutCancel(ctx), ipc.Request{Title: *title, Body: body, Level: lvl, Silent: *silent, Sound: *sound}); err != nil {
		fmt.Fprintln(os.Stderr, "fpager: notify:", err)
	}
	return code, nil
}

func cmdWait(ctx context.Context, args []string) error {
	fs := newFlags("wait")
	title := fs.String("t", "", "title (default: process name)")
	silent := fs.Bool("s", false, "silent: no vibration/sound")
	sound := fs.String("sound", os.Getenv("FPAGER_SOUND"), "sound: name (e.g. pda-loud), none = no sound; default: the daemon's (pda)")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("wait: expected exactly one PID")
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil || pid <= 0 {
		return errors.Errorf("wait: bad PID %q", fs.Arg(0))
	}
	procDir := fmt.Sprintf("/proc/%d", pid)
	cmdline, err := os.ReadFile(procDir + "/cmdline")
	if err != nil {
		return errors.Wrapf(err, "wait: process %d", pid)
	}
	argv := strings.Fields(strings.ReplaceAll(string(cmdline), "\x00", " "))
	name := strconv.Itoa(pid)
	if len(argv) > 0 {
		name = filepath.Base(argv[0])
	}
	start := time.Now()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		if _, err := os.Stat(procDir); err != nil {
			break
		}
		// Zombie children of someone else still count as finished.
		if b, err := os.ReadFile(procDir + "/stat"); err == nil {
			if f := strings.Fields(string(b)); len(f) > 2 && strings.HasSuffix(f[1], ")") && f[2] == "Z" {
				break
			}
		}
	}
	if *title == "" {
		*title = name
	}
	body := fmt.Sprintf("pid %d exited (waited %s)\n$ %s", pid, shortDuration(time.Since(start)), strings.Join(argv, " "))
	return send(ctx, ipc.Request{Title: *title, Body: body, Level: "info", Silent: *silent, Sound: *sound})
}

func cmdStatus(ctx context.Context, args []string) error {
	fs := newFlags("status")
	asJSON := fs.Bool("json", false, "print raw JSON")
	_ = fs.Parse(args)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var s *pager.Status
	if url := os.Getenv("FPAGER_URL"); url != "" {
		rs, err := remoteStatus(ctx, url, os.Getenv("FPAGER_TOKEN"))
		if err != nil {
			return err
		}
		s = rs
	} else {
		resp, err := ipc.Do(ctx, ipc.SocketPath(), ipc.Request{Op: "status"})
		if err != nil {
			return err
		}
		s = resp.Status
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}
	if s.Connected {
		fmt.Printf("connected via %s for %s\n", s.Transport, shortDuration(time.Since(s.Since)))
	} else {
		fmt.Println("not connected")
	}
	if b := s.Battery; b != nil {
		fmt.Printf("battery: %d%% %s (%s ago)\n", b.Level, b.State, screen.FormatAge(time.Since(b.Updated)))
	}
	if sig := s.Signal; sig != nil {
		fmt.Printf("signal: %d dBm (min %d, %s ago)\n", sig.RSSI, sig.Min, screen.FormatAge(time.Since(sig.Updated)))
	}
	fmt.Printf("alert: %s\n", s.Alert)
	fmt.Printf("pending: %d (display %v)\n", len(s.Pending), s.Display)
	for _, n := range s.Pending {
		fmt.Printf("  #%d [%s] %s — %s ago\n", n.ID, n.Level, n.Title, screen.FormatAge(time.Since(n.Created)))
	}
	return nil
}

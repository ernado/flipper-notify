package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ernado/flipper-notify/internal/focus"
	"github.com/ernado/flipper-notify/internal/ipc"
)

// claudeHookInput is the subset of the Claude Code hook payload we use.
type claudeHookInput struct {
	Event          string `json:"hook_event_name"`
	CWD            string `json:"cwd"`
	Message        string `json:"message"`
	SessionTitle   string `json:"session_title"`
	TranscriptPath string `json:"transcript_path"`
}

// transcriptTitles returns the last customTitle and aiTitle recorded in a transcript.
func transcriptTitles(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var custom, ai string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line[:min(len(line), 200)]), "-title") {
			continue
		}
		var rec struct {
			Type        string `json:"type"`
			AITitle     string `json:"aiTitle"`
			CustomTitle string `json:"customTitle"`
		}
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		switch {
		case rec.AITitle != "":
			ai = rec.AITitle
		case rec.CustomTitle != "":
			custom = rec.CustomTitle
		}
	}
	return []string{custom, ai}
}

// cmdClaudeHook turns a Claude Code hook event (JSON on stdin) into a notification,
// unless the user is looking at this session's terminal tab.
// It never fails the hook: errors go to stderr and the exit code is always 0.
func cmdClaudeHook(ctx context.Context, args []string) error {
	fs := newFlags("claude-hook")
	silent := fs.Bool("s", false, "silent: no vibration/sound")
	always := fs.Bool("always", false, "notify even when the session's terminal tab is focused")
	maxIdle := fs.Duration("idle", 5*time.Minute, "treat the tab as unattended after this much input idle time (0 = ignore idle)")
	check := fs.Bool("check", false, "only print the focus decision, do not notify")
	_ = fs.Parse(args)

	var in claudeHookInput
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in); err != nil {
		fmt.Fprintln(os.Stderr, "fpager claude-hook: bad input:", err)
		return nil
	}

	if !*always || *check {
		titles := append([]string{in.SessionTitle}, transcriptTitles(in.TranscriptPath)...)
		fctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		res, err := focus.Check(fctx, os.Getpid(), titles, *maxIdle)
		cancel()
		if *check {
			fmt.Printf("focused=%v reason=%q idle=%s titles=%q err=%v\n", res.Focused, res.Reason, res.Idle.Round(time.Second), titles, err)
			return nil
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "fpager claude-hook: focus check:", err)
		} else if res.Focused {
			return nil
		}
	}

	project := filepath.Base(in.CWD)
	if in.CWD == "" {
		project = "claude"
	}
	req := ipc.Request{Title: "Claude · " + project, Silent: *silent}
	switch in.Event {
	case "Stop":
		req.Level, req.Body = "ok", "Turn finished, waiting for you.\n"+in.CWD
	case "Notification":
		req.Level, req.Body = "info", in.Message+"\n"+in.CWD
	default:
		req.Level, req.Body = "info", in.Event+"\n"+in.Message
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := send(ctx, req); err != nil {
		fmt.Fprintln(os.Stderr, "fpager claude-hook: notify:", err)
	}
	return nil
}

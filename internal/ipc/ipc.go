// Package ipc is the unix-socket protocol between the fpager CLI and daemon:
// one JSON request and one JSON response per connection.
package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-faster/errors"

	"github.com/ernado/flipper-notify/internal/fap"
	"github.com/ernado/flipper-notify/internal/pager"
	"github.com/ernado/flipper-notify/internal/screen"
)

// SocketPath returns the default socket path.
func SocketPath() string {
	if p := os.Getenv("FPAGER_SOCKET"); p != "" {
		return p
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "fpager.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("fpager-%d.sock", os.Getuid()))
}

// Request from CLI to daemon.
type Request struct {
	Op     string `json:"op"` // "send", "status"
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	Level  string `json:"level,omitempty"`
	Silent bool   `json:"silent,omitempty"`
	Sound  string `json:"sound,omitempty"`
}

// Response from daemon.
type Response struct {
	Error  string        `json:"error,omitempty"`
	ID     uint64        `json:"id,omitempty"`
	Status *pager.Status `json:"status,omitempty"`
}

// Serve accepts requests on path until ctx is done.
func Serve(ctx context.Context, log *slog.Logger, path string, p *pager.Pager) error {
	if c, err := net.Dial("unix", path); err == nil {
		_ = c.Close()
		return errors.Errorf("another daemon is listening on %s", path)
	}
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return errors.Wrap(err, "listen")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return errors.Wrap(err, "chmod socket")
	}
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	defer func() { _ = os.Remove(path) }()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.Wrap(err, "accept")
		}
		go func() {
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
			var req Request
			if err := json.NewDecoder(conn).Decode(&req); err != nil {
				log.Warn("Bad request", "err", err)
				return
			}
			resp := handle(ctx, p, req)
			_ = json.NewEncoder(conn).Encode(resp)
		}()
	}
}

func handle(ctx context.Context, p *pager.Pager, req Request) Response {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switch req.Op {
	case "send":
		lvl, err := screen.ParseLevel(req.Level)
		if err != nil {
			return Response{Error: err.Error()}
		}
		if req.Sound != "" && !fap.Valid(req.Sound) {
			return Response{Error: fmt.Sprintf("unknown sound %q (have: %s)", req.Sound, strings.Join(fap.Sounds(), ", "))}
		}
		id, err := p.Notify(ctx, pager.Notification{Title: req.Title, Body: req.Body, Level: lvl, Silent: req.Silent, Sound: req.Sound})
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{ID: id}
	case "status":
		s, err := p.Status(ctx)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{Status: &s}
	}
	return Response{Error: fmt.Sprintf("unknown op %q", req.Op)}
}

// Do sends a request to the daemon.
func Do(ctx context.Context, path string, req Request) (Response, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return Response{}, errors.Wrap(err, "daemon not reachable (is `fpager daemon` running?)")
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, errors.Wrap(err, "send request")
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, errors.Wrap(err, "read response")
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

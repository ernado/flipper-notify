package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-faster/errors"

	"github.com/ernado/flipper-notify/internal/httpapi"
	"github.com/ernado/flipper-notify/internal/ipc"
	"github.com/ernado/flipper-notify/internal/pager"
)

// httpClient talks to the daemon directly: it is on the LAN, never via a proxy.
var httpClient = &http.Client{Transport: &http.Transport{Proxy: nil}}

func remoteDo(ctx context.Context, method, url, token string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return errors.Wrap(err, "request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "remote")
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return errors.Wrap(err, "read response")
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		return errors.Errorf("remote: %s: %s", resp.Status, e.Error)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return errors.Wrap(err, "decode response")
		}
	}
	return nil
}

func remoteSend(ctx context.Context, base, token string, r ipc.Request) error {
	body, err := json.Marshal(httpapi.Notify{Title: r.Title, Body: r.Body, Level: r.Level, Silent: r.Silent, Sound: r.Sound})
	if err != nil {
		return errors.Wrap(err, "marshal")
	}
	return remoteDo(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/notify", token, body, nil)
}

func remoteStatus(ctx context.Context, base, token string) (*pager.Status, error) {
	var s pager.Status
	if err := remoteDo(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/v1/status", token, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// loadEnvFile sets FPAGER_* variables from $XDG_CONFIG_HOME/fpager/env (KEY=VALUE lines)
// unless they are already set, so remote hosts need no shell configuration.
func loadEnvFile() {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		dir = filepath.Join(home, ".config")
	}
	f, err := os.Open(filepath.Join(dir, "fpager", "env"))
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !strings.HasPrefix(k, "FPAGER_") {
			continue
		}
		if _, set := os.LookupEnv(k); !set {
			_ = os.Setenv(k, strings.Trim(v, `"'`))
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "fpager: env file:", err)
	}
}

package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ernado/flipper-notify/internal/pager"
)

func TestAPI(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := pager.New(log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Run(ctx) }()

	const token = "0123456789abcdef0123"
	srv := httptest.NewServer(Handler(log, p, token))
	defer srv.Close()

	do := func(method, path, ct, body, tok string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	if r := do("POST", "/v1/notify", "", "x", ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", r.StatusCode)
	}
	if r := do("POST", "/v1/notify", "", "x", "wrong-token-wrong-token"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", r.StatusCode)
	}
	if r := do("POST", "/v1/notify?title=backup&level=fail", "text/plain", "disk full\n", token); r.StatusCode != http.StatusOK {
		t.Fatalf("text: %d", r.StatusCode)
	}
	if r := do("POST", "/v1/notify", "application/json", `{"title":"deploy","body":"ok","level":"ok"}`, token); r.StatusCode != http.StatusOK {
		t.Fatalf("json: %d", r.StatusCode)
	}
	if r := do("POST", "/v1/notify?level=bogus", "", "x", token); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad level: %d", r.StatusCode)
	}

	r := do("GET", "/v1/status", "", "", token)
	var s pager.Status
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	if len(s.Pending) != 2 || s.Pending[0].Title != "backup" || s.Pending[0].Body != "disk full" || s.Pending[1].Title != "deploy" {
		t.Fatalf("pending: %+v", s.Pending)
	}
}

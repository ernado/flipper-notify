// Package httpapi exposes the pager over HTTP for other hosts (bearer token auth).
//
//	POST /v1/notify   JSON {"title","body","level","silent","sound"}, or a text body with
//	                  ?title=&level=&silent=1&sound= query parameters.
//	GET  /v1/status   pager status JSON.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-faster/errors"

	"github.com/ernado/flipper-notify/internal/fap"
	"github.com/ernado/flipper-notify/internal/pager"
	"github.com/ernado/flipper-notify/internal/screen"
)

const maxBody = 64 << 10

// Notify is the JSON request body of POST /v1/notify.
type Notify struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Level  string `json:"level,omitempty"`
	Silent bool   `json:"silent,omitempty"`
	Sound  string `json:"sound,omitempty"`
}

// Handler returns the API handler.
func Handler(log *slog.Logger, p *pager.Pager, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/notify", func(w http.ResponseWriter, r *http.Request) {
		var req Notify
		ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		body := http.MaxBytesReader(w, r.Body, maxBody)
		if ct == "application/json" {
			if err := json.NewDecoder(body).Decode(&req); err != nil {
				writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
				return
			}
		} else {
			b, err := io.ReadAll(body)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			q := r.URL.Query()
			req = Notify{
				Title:  q.Get("title"),
				Body:   strings.TrimRight(string(b), "\n"),
				Level:  q.Get("level"),
				Silent: q.Get("silent") == "1" || q.Get("silent") == "true",
				Sound:  q.Get("sound"),
			}
		}
		lvl, err := screen.ParseLevel(req.Level)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.Title == "" {
			req.Title = remoteName(r)
		}
		if req.Sound != "" && !fap.Valid(req.Sound) {
			writeErr(w, http.StatusBadRequest, "unknown sound "+req.Sound+" (have: "+strings.Join(fap.Sounds(), ", ")+")")
			return
		}
		id, err := p.Notify(r.Context(), pager.Notification{Title: req.Title, Body: req.Body, Level: lvl, Silent: req.Silent, Sound: req.Sound})
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		log.Info("HTTP notify", "remote", remoteName(r), "id", id)
		writeJSON(w, http.StatusOK, map[string]any{"id": id})
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		s, err := p.Status(r.Context())
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, s)
	})
	return auth(token, mux)
}

func remoteName(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func auth(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// Serve listens on addr until ctx is done. If the address is not available yet
// (e.g. the LAN interface is still down) it retries instead of failing the daemon.
func Serve(ctx context.Context, log *slog.Logger, addr string, h http.Handler) {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	var lastErr string
	for ctx.Err() == nil {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			if msg := err.Error(); msg != lastErr {
				log.Warn("HTTP listen failed, retrying", "addr", addr, "err", err)
				lastErr = msg
			}
			select {
			case <-time.After(10 * time.Second):
			case <-ctx.Done():
			}
			continue
		}
		log.Info("HTTP API listening", "addr", addr)
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warn("HTTP serve failed", "err", err)
		}
		return
	}
}

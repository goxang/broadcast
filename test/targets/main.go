// Command broadcast-target is a tiny HTTP server used to verify Broadcast
// delivery in tests. It records every request it receives and exposes /stats so
// an external observer can prove which pods received a given broadcast.
package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

type item struct {
	Path string    `json:"path"`
	Body string    `json:"body,omitempty"`
	At   time.Time `json:"at"`
}

type state struct {
	mu       sync.Mutex
	hostname string
	items    []item
	slowFor  time.Duration
}

func (s *state) record(path, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, item{Path: path, Body: body, At: time.Now()})
}

func (s *state) snapshot() []item {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]item, len(s.items))
	copy(out, s.items)
	return out
}

func (s *state) setSlow(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slowFor = d
}

func (s *state) slow() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.slowFor
}

func main() {
	host, _ := os.Hostname()
	s := &state{hostname: host}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	addr := ":8080"

	mux := http.NewServeMux()

	// Record every request on any path/method (broadcast hits /broadcast-test).
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		s.record(r.URL.Path, string(body))
		if d := s.slow(); d > 0 {
			time.Sleep(d)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		items := s.snapshot()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"hostname": s.hostname,
			"received": len(items),
			"items":    items,
		})
	})

	mux.HandleFunc("/control", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("mode") {
		case "slow":
			d, err := time.ParseDuration(r.URL.Query().Get("ms"))
			if err != nil {
				d = time.Second
			}
			s.setSlow(d)
			_, _ = w.Write([]byte("slow " + d.String()))
		default:
			s.setSlow(0)
			_, _ = w.Write([]byte("ok"))
		}
	})

	logger.Info("target listening", "hostname", host, "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

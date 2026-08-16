// Package proxy implements the broadcast data plane: an HTTP handler that
// receives one request and fans it out to every ready endpoint of a Broadcast.
//
// The proxy reads endpoint state exclusively from an in-memory resolver and
// never issues Kubernetes API calls on the request path. Delivery is
// best-effort by design (see the package docs and README for the exact
// response semantics).
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/goxang/broadcast/pkg/metrics"
	"github.com/goxang/broadcast/pkg/resolver"
)

const (
	// broadcastPrefix is the path prefix for broadcast requests.
	broadcastPrefix = "/broadcast/"

	// defaultMaxBodyBytes caps the request body read into memory before fan-out.
	// Broadcasts are intended for small hint/invalidation payloads, so large
	// bodies are rejected rather than buffered unboundedly.
	defaultMaxBodyBytes = 1 << 20 // 1 MiB
)

// Options configures a Proxy.
type Options struct {
	// Resolver supplies the current endpoint state.
	Resolver resolver.Resolver
	// Namespace is the namespace in which Broadcasts are looked up.
	Namespace string
	// Metrics collects Prometheus metrics (may be nil to disable).
	Metrics *metrics.Metrics
	// MaxBodyBytes bounds the request body buffered before fan-out.
	MaxBodyBytes int64
	// Logger is the structured logger.
	Logger *slog.Logger
}

// Proxy fans HTTP requests out to all eligible endpoints of a Broadcast.
type Proxy struct {
	resolver  resolver.Resolver
	ns        string
	client    *http.Client
	transport *http.Transport
	metrics   *metrics.Metrics
	maxBody   int64
	log       *slog.Logger
}

// New builds a Proxy with a connection-pooled HTTP transport.
func New(opts Options) *Proxy {
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = defaultMaxBodyBytes
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   3 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 8,
		MaxConnsPerHost:     32,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	return &Proxy{
		resolver:  opts.Resolver,
		ns:        opts.Namespace,
		client:    &http.Client{Transport: transport},
		transport: transport,
		metrics:   opts.Metrics,
		maxBody:   opts.MaxBodyBytes,
		log:       opts.Logger,
	}
}

// Close releases idle keep-alive connections to target endpoints. It should be
// called on graceful shutdown; in-flight requests are unaffected.
func (p *Proxy) Close() {
	if p.transport != nil {
		p.transport.CloseIdleConnections()
	}
}

// targetResult records the outcome of fanning out to one endpoint.
type targetResult struct {
	status int
	err    error
}

// summary is the JSON response body returned to the caller.
type summary struct {
	Broadcast  string         `json:"broadcast"`
	Targets    int            `json:"targets"`
	Dispatched int            `json:"dispatched"`
	Responses  int            `json:"responses"`
	Errors     int            `json:"errors"`
	TimedOut   bool           `json:"timed_out"`
	DurationMS int64          `json:"duration_ms"`
	Statuses   map[string]int `json:"statuses,omitempty"`
}

// ServeHTTP routes a broadcast request and implements the documented response
// semantics. It is the entire request path: no Kubernetes API calls happen
// here.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, broadcastPrefix) {
		http.NotFound(w, r)
		return
	}

	name, forwardPath, ok := parseBroadcastPath(r.URL.Path)
	if !ok {
		p.respondError(w, http.StatusBadRequest, "malformed broadcast path, expected /broadcast/{name}[/{path}]")
		return
	}

	key := types.NamespacedName{Namespace: p.ns, Name: name}
	state, found := p.resolver.State(key)
	if !found {
		p.respondError(w, http.StatusNotFound, "unknown broadcast "+name)
		return
	}
	if len(state.Endpoints) == 0 {
		p.respondError(w, http.StatusServiceUnavailable, "broadcast "+name+" has no ready endpoints")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, p.maxBody))
	if err != nil {
		p.respondError(w, http.StatusRequestEntityTooLarge, "request body exceeds limit")
		return
	}

	res := p.fanout(r.Context(), name, state, forwardPath, r.URL.RawQuery, r, body)

	if p.metrics != nil {
		p.metrics.RequestsTotal.WithLabelValues(name, fmt.Sprintf("%d", res.status)).Inc()
	}
	p.respondJSON(w, res.status, res.summary)
}

// parseBroadcastPath splits /broadcast/{name}/{rest...}. The forward path is
// everything after the name, always rooted at "/" (empty rest means "/").
func parseBroadcastPath(path string) (name, forwardPath string, ok bool) {
	trimmed := strings.TrimPrefix(path, broadcastPrefix)
	segments := strings.Split(trimmed, "/")
	if len(segments) == 0 || segments[0] == "" {
		return "", "", false
	}
	name = segments[0]
	rest := segments[1:]
	if len(rest) == 0 || (len(rest) == 1 && rest[0] == "") {
		return name, "/", true
	}
	return name, "/" + strings.Join(rest, "/"), true
}

// fanout dispatches the request to every target within the timeout budget and
// returns the collected summary. It never blocks longer than the timeout and
// never retries.
func (p *Proxy) fanout(
	ctx context.Context,
	name string,
	state resolver.State,
	forwardPath, rawQuery string,
	req *http.Request,
	body []byte,
) *broadcastResult {
	start := time.Now()
	timeout := state.Timeout
	if timeout <= 0 {
		timeout = time.Second
	}

	fctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	concurrency := int(state.Concurrency)
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)

	targets := state.Endpoints
	results := make([]targetResult, len(targets))
	var wg sync.WaitGroup

	dispatched := 0
	for i, ep := range targets {
		select {
		case sem <- struct{}{}:
		case <-fctx.Done():
			// Budget exhausted before this target could be dispatched; the
			// remaining targets are skipped (best effort).
			goto done
		}

		wg.Add(1)
		dispatched++
		go func(i int, ep resolver.Endpoint) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = p.sendOne(fctx, name, ep, forwardPath, rawQuery, req, body)
		}(i, ep)
	}

done:
	wg.Wait()

	// The wait is bounded: sendOne uses fctx, which is cancelled at the
	// timeout, so wg.Wait returns at worst shortly after the timeout.
	timedOut := fctx.Err() != nil

	sum := summary{
		Broadcast:  name,
		Targets:    len(targets),
		Dispatched: dispatched,
		DurationMS: time.Since(start).Milliseconds(),
		TimedOut:   timedOut,
		Statuses:   map[string]int{},
	}
	for _, r := range results[:dispatched] {
		if r.err != nil {
			sum.Errors++
			continue
		}
		sum.Responses++
		sum.Statuses[fmt.Sprintf("%d", r.status)]++
	}

	// Dispatch was initiated for at least one target: this is best-effort, so
	// we acknowledge acceptance rather than guarantee delivery.
	if p.metrics != nil {
		p.metrics.FanoutDuration.Observe(time.Since(start).Seconds())
	}
	return &broadcastResult{status: http.StatusAccepted, summary: sum}
}

// sendOne fans the request out to a single target using the shared
// connection-pooled client. It does not retry.
func (p *Proxy) sendOne(
	ctx context.Context,
	name string,
	ep resolver.Endpoint,
	forwardPath, rawQuery string,
	src *http.Request,
	body []byte,
) targetResult {
	url := fmt.Sprintf("http://%s:%d%s", ep.IP, ep.Port, forwardPath)
	if rawQuery != "" {
		url += "?" + rawQuery
	}

	targetReq, err := http.NewRequestWithContext(ctx, src.Method, url, bytes.NewReader(body))
	if err != nil {
		return targetResult{err: err}
	}
	copyHeaders(targetReq.Header, src.Header)

	resp, err := p.client.Do(targetReq)
	if err != nil {
		p.observeTarget(name, "error")
		return targetResult{err: err}
	}
	defer resp.Body.Close()
	// Drain and discard the response body so the connection can be reused.
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	p.observeTarget(name, fmt.Sprintf("%d", resp.StatusCode))
	return targetResult{status: resp.StatusCode}
}

func (p *Proxy) observeTarget(name, outcome string) {
	if p.metrics == nil {
		return
	}
	p.metrics.TargetRequests.WithLabelValues(name, outcome).Inc()
}

// copyHeaders copies request headers, dropping hop-by-hop headers that must not
// be forwarded.
func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func isHopByHop(h string) bool {
	switch strings.ToLower(h) {
	case "connection", "proxy-connection", "keep-alive", "te", "trailer",
		"transfer-encoding", "upgrade", "host":
		return true
	}
	return false
}

// broadcastResult carries the collected summary plus the HTTP status code to
// return to the caller.
type broadcastResult struct {
	summary summary
	status  int
}

func (p *Proxy) respondJSON(w http.ResponseWriter, status int, s summary) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(s)
}

func (p *Proxy) respondError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

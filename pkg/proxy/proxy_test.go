package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/goxang/broadcast/pkg/resolver"
)

func TestParseBroadcastPath(t *testing.T) {
	tests := []struct {
		in          string
		name        string
		forwardPath string
		ok          bool
	}{
		{"/broadcast/foo/invalidate", "foo", "/invalidate", true},
		{"/broadcast/foo/", "foo", "/", true},
		{"/broadcast/foo", "foo", "/", true},
		{"/broadcast/foo/a/b/c", "foo", "/a/b/c", true},
		{"/broadcast/", "", "", false},
		{"/other", "", "", false},
	}
	for _, tt := range tests {
		name, fp, ok := parseBroadcastPath(tt.in)
		if name != tt.name || fp != tt.forwardPath || ok != tt.ok {
			t.Errorf("parseBroadcastPath(%q) = (%q,%q,%v), want (%q,%q,%v)",
				tt.in, name, fp, ok, tt.name, tt.forwardPath, tt.ok)
		}
	}
}

// startTargets launches n httptest servers that record the paths and bodies
// they receive, returning their endpoints.
func startTargets(t *testing.T, n int) ([]resolver.Endpoint, *int32) {
	t.Helper()
	var hits int32
	var endpoints []resolver.Endpoint
	for i := 0; i < n; i++ {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)
		host := strings.TrimPrefix(srv.URL, "http://")
		host = strings.Split(host, ":")[0]
		endpoints = append(endpoints, resolver.Endpoint{IP: host, Port: int32(mustPort(srv))})
	}
	return endpoints, &hits
}

func mustPort(srv *httptest.Server) int {
	// httptest server URL is http://127.0.0.1:PORT; parse port.
	u := srv.URL
	idx := strings.LastIndex(u, ":")
	var p int
	for _, c := range u[idx+1:] {
		p = p*10 + int(c-'0')
	}
	return p
}

func newTestProxy(res resolver.Resolver) *Proxy {
	return New(Options{Resolver: res, Namespace: "test"})
}

func TestUnknownBroadcast(t *testing.T) {
	p := newTestProxy(resolver.New())
	req := httptest.NewRequest(http.MethodPost, "/broadcast/nope/x", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestNoReadyEndpoints(t *testing.T) {
	res := resolver.New()
	res.Set(types.NamespacedName{Namespace: "test", Name: "b"}, resolver.State{})
	p := newTestProxy(res)
	req := httptest.NewRequest(http.MethodPost, "/broadcast/b/x", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestFanoutToAllTargets(t *testing.T) {
	endpoints, hits := startTargets(t, 3)
	res := resolver.New()
	res.Set(types.NamespacedName{Namespace: "test", Name: "b"}, resolver.State{
		Endpoints:   endpoints,
		Timeout:     time.Second,
		Concurrency: 16,
	})
	p := newTestProxy(res)

	req := httptest.NewRequest(http.MethodPost, "/broadcast/b/invalidate", strings.NewReader("hello"))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if got := atomic.LoadInt32(hits); got != 3 {
		t.Fatalf("targets hit = %d, want 3", got)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if !strings.Contains(string(body), `"responses":3`) {
		t.Fatalf("summary should report 3 responses, got %s", body)
	}
}

func TestTimeoutRespected(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()

	res := resolver.New()
	res.Set(types.NamespacedName{Namespace: "test", Name: "b"}, resolver.State{
		Endpoints:   []resolver.Endpoint{{IP: "127.0.0.1", Port: int32(mustPort(slow))}},
		Timeout:     50 * time.Millisecond,
		Concurrency: 16,
	})
	p := newTestProxy(res)

	start := time.Now()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/broadcast/b/x", nil))
	elapsed := time.Since(start)

	if elapsed > 300*time.Millisecond {
		t.Fatalf("broadcast took %v, timeout not respected", elapsed)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
}

func TestFailureIsolationNoRetry(t *testing.T) {
	var badHits, goodHits int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&badHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&goodHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer good.Close()

	res := resolver.New()
	res.Set(types.NamespacedName{Namespace: "test", Name: "b"}, resolver.State{
		Endpoints: []resolver.Endpoint{
			{IP: "127.0.0.1", Port: int32(mustPort(bad))},
			{IP: "127.0.0.1", Port: int32(mustPort(good))},
		},
		Timeout:     time.Second,
		Concurrency: 16,
	})
	p := newTestProxy(res)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/broadcast/b/x", nil))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (best effort even with one failing target)", rec.Code)
	}
	if got := atomic.LoadInt32(&goodHits); got != 1 {
		t.Fatalf("good target hit %d times, want 1", got)
	}
	if got := atomic.LoadInt32(&badHits); got != 1 {
		t.Fatalf("bad target hit %d times, want 1 (no retry)", got)
	}
}

func TestBoundedConcurrency(t *testing.T) {
	var inflight, maxInflight int32
	mu := sync.Mutex{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&inflight, 1)
		mu.Lock()
		if cur > maxInflight {
			maxInflight = cur
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	const n = 32
	endpoints := make([]resolver.Endpoint, n)
	for i := range endpoints {
		endpoints[i] = resolver.Endpoint{IP: "127.0.0.1", Port: int32(mustPort(target))}
	}
	res := resolver.New()
	res.Set(types.NamespacedName{Namespace: "test", Name: "b"}, resolver.State{
		Endpoints:   endpoints,
		Timeout:     time.Second,
		Concurrency: 4,
	})
	p := newTestProxy(res)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/broadcast/b/x", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if maxInflight > 4 {
		t.Fatalf("max concurrency = %d, want <= 4", maxInflight)
	}
}

func TestBodyForwarded(t *testing.T) {
	var gotBody string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	res := resolver.New()
	res.Set(types.NamespacedName{Namespace: "test", Name: "b"}, resolver.State{
		Endpoints:   []resolver.Endpoint{{IP: "127.0.0.1", Port: int32(mustPort(target))}},
		Timeout:     time.Second,
		Concurrency: 16,
	})
	p := newTestProxy(res)
	p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/broadcast/b/x", strings.NewReader("payload")))
	if gotBody != "payload" {
		t.Fatalf("body = %q, want payload", gotBody)
	}
}

// TestBodyDuplicatedToAllTargets verifies each target gets its own full copy of
// the body (the source body must not be consumed once and emptied for later
// targets).
func TestBodyDuplicatedToAllTargets(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string]string{}
	endpoints := make([]resolver.Endpoint, 0, 3)
	for i := 0; i < 3; i++ {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies[r.Host] = string(b)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)
		endpoints = append(endpoints, resolver.Endpoint{IP: "127.0.0.1", Port: int32(mustPort(srv))})
	}

	res := resolver.New()
	res.Set(types.NamespacedName{Namespace: "test", Name: "b"}, resolver.State{
		Endpoints:   endpoints,
		Timeout:     time.Second,
		Concurrency: 16,
	})
	p := newTestProxy(res)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/broadcast/b/x", strings.NewReader("shared-body")))

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 {
		t.Fatalf("distinct targets = %d, want 3", len(bodies))
	}
	for host, body := range bodies {
		if body != "shared-body" {
			t.Fatalf("target %s body = %q, want shared-body", host, body)
		}
	}
}

// TestPathQueryMethodForwarded verifies the forward path, query string, and
// method are preserved.
func TestPathQueryMethodForwarded(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	res := resolver.New()
	res.Set(types.NamespacedName{Namespace: "test", Name: "b"}, resolver.State{
		Endpoints:   []resolver.Endpoint{{IP: "127.0.0.1", Port: int32(mustPort(target))}},
		Timeout:     time.Second,
		Concurrency: 16,
	})
	p := newTestProxy(res)
	p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/broadcast/b/a/b/c?k=v&x=1", strings.NewReader("x")))

	if gotMethod != http.MethodPut {
		t.Fatalf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/a/b/c" {
		t.Fatalf("path = %q, want /a/b/c", gotPath)
	}
	if gotQuery != "k=v&x=1" {
		t.Fatalf("query = %q, want k=v&x=1", gotQuery)
	}
}

// TestHeaderForwarding verifies end-to-end headers are preserved and
// hop-by-hop headers are dropped.
func TestHeaderForwarding(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		seen["x-custom"] = r.Header.Get("X-Custom")
		seen["content-type"] = r.Header.Get("Content-Type")
		seen["connection"] = r.Header.Get("Connection")
		seen["host"] = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	res := resolver.New()
	res.Set(types.NamespacedName{Namespace: "test", Name: "b"}, resolver.State{
		Endpoints:   []resolver.Endpoint{{IP: "127.0.0.1", Port: int32(mustPort(target))}},
		Timeout:     time.Second,
		Concurrency: 16,
	})
	p := newTestProxy(res)

	req := httptest.NewRequest(http.MethodPost, "/broadcast/b/x", strings.NewReader("y"))
	req.Header.Set("X-Custom", "hello")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "keep-alive")
	p.ServeHTTP(httptest.NewRecorder(), req)

	mu.Lock()
	defer mu.Unlock()
	if seen["x-custom"] != "hello" {
		t.Fatalf("X-Custom = %q, want hello", seen["x-custom"])
	}
	if seen["content-type"] != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", seen["content-type"])
	}
	if seen["connection"] != "" {
		t.Fatalf("Connection should be dropped, got %q", seen["connection"])
	}
	// Host must be the target's own host, not the original.
	if !strings.Contains(seen["host"], "127.0.0.1") {
		t.Fatalf("Host = %q, want target host", seen["host"])
	}
}

func TestBodyTooLarge(t *testing.T) {
	res := resolver.New()
	res.Set(types.NamespacedName{Namespace: "test", Name: "b"}, resolver.State{
		Endpoints:   []resolver.Endpoint{{IP: "127.0.0.1", Port: 8080}},
		Timeout:     time.Second,
		Concurrency: 16,
	})
	p := New(Options{Resolver: res, Namespace: "test", MaxBodyBytes: 4})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/broadcast/b/x", strings.NewReader("12345")))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

// TestCancelledContextAccounting verifies the summary accounting invariant:
// every dispatched target is counted exactly once (as a response or an error),
// and targets skipped because of a cancelled/expired context are not
// miscounted as responses with a bogus status code.
func TestCancelledContextAccounting(t *testing.T) {
	endpoints, _ := startTargets(t, 3)
	res := resolver.New()
	res.Set(types.NamespacedName{Namespace: "test", Name: "b"}, resolver.State{
		Endpoints:   endpoints,
		Timeout:     time.Second,
		Concurrency: 16,
	})
	p := newTestProxy(res)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/broadcast/b/x", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	var sum summary
	if err := json.Unmarshal(rec.Body.Bytes(), &sum); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if sum.Targets != 3 {
		t.Fatalf("targets = %d, want 3", sum.Targets)
	}
	if sum.Dispatched > sum.Targets {
		t.Fatalf("dispatched = %d exceeds targets = %d", sum.Dispatched, sum.Targets)
	}
	// The core invariant: responses + errors must equal the number of targets
	// actually dispatched (skipped targets must not be counted).
	if sum.Responses+sum.Errors != sum.Dispatched {
		t.Fatalf("responses(%d)+errors(%d) != dispatched(%d)", sum.Responses, sum.Errors, sum.Dispatched)
	}
	if _, ok := sum.Statuses["0"]; ok {
		t.Fatalf("summary should not report a bogus status \"0\": %+v", sum.Statuses)
	}
}

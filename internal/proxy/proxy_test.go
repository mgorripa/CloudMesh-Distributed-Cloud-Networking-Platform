package proxy

import (
	"cloudmesh/internal/pool"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPAndTCP(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			t.Error("path lost")
		}
		_, _ = io.WriteString(w, "backend-ok")
	}))
	defer backend.Close()
	p := &pool.Pool{}
	_ = p.Replace([]string{strings.TrimPrefix(backend.URL, "http://")})
	m := &Metrics{}
	px := New(p, "round-robin", m)
	defer px.Transport.CloseIdleConnections()
	w := httptest.NewRecorder()
	px.ServeHTTP(w, httptest.NewRequest("GET", "http://example.com/api", nil))
	if w.Code != 200 || w.Body.String() != "backend-ok" {
		t.Fatal(w.Code, w.Body.String())
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- px.ServeTCP(ctx, l) }()
	c, e := net.DialTimeout("tcp", l.Addr().String(), time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = io.WriteString(c, "GET /api HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
	body, e := io.ReadAll(c)
	if e != nil || !strings.Contains(string(body), "backend-ok") {
		t.Fatal(string(body), e)
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown hung")
	}
	p.Snapshot()[0].SetHealthy(false)
	w = httptest.NewRecorder()
	px.ServeHTTP(w, httptest.NewRequest("GET", "http://example.com/api", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	px.Admin().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), "cloudmesh_http_requests_total 2") {
		t.Fatal(w.Body.String())
	}
}
func TestHealthProbe(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := l.Addr().String()
	_ = l.Close()
	p := &pool.Pool{}
	_ = p.Replace([]string{addr})
	px := New(p, "round-robin", &Metrics{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go px.Probe(ctx, 10*time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for p.Ready() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if p.Ready() != 0 {
		t.Fatal("dead backend marked healthy")
	}
}

func TestIPv6Backend(t *testing.T) {
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ipv6-ok") })}
	go server.Serve(l)
	defer server.Close()
	p := &pool.Pool{}
	if err = p.Replace([]string{l.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	px := New(p, "hash", &Metrics{})
	defer px.Transport.CloseIdleConnections()
	w := httptest.NewRecorder()
	px.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/", nil))
	if w.Code != 200 || w.Body.String() != "ipv6-ok" {
		t.Fatal(w.Code, w.Body.String())
	}
}

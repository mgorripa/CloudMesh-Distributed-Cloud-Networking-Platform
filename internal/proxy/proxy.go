// Package proxy implements the user-space data plane.
package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"
	"sync/atomic"
	"time"

	"cloudmesh/internal/pool"
)

type Metrics struct {
	HTTP, TCP, Errors, Reconciles, DiscoveryErrors atomic.Uint64
	ActiveTCP                                      atomic.Int64
}
type Proxy struct {
	Pool      *pool.Pool
	Algorithm string
	Metrics   *Metrics
	Transport *http.Transport
}

func New(p *pool.Pool, algorithm string, m *Metrics) *Proxy {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.ResponseHeaderTimeout = 10 * time.Second
	t.MaxIdleConnsPerHost = 16
	return &Proxy{p, algorithm, m, t}
}
func key(r *http.Request) string {
	if v := r.Header.Get("X-CloudMesh-Key"); v != "" {
		return v
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return h
	}
	return r.RemoteAddr
}
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.Metrics.HTTP.Add(1)
	b, release, err := p.Pool.Acquire(p.Algorithm, key(r))
	if err != nil {
		p.Metrics.Errors.Add(1)
		http.Error(w, "no healthy backend", http.StatusServiceUnavailable)
		return
	}
	defer release()
	rp := httputil.ReverseProxy{Transport: p.Transport, Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.URL.Scheme = "http"
		pr.Out.URL.Host = b.Address
		pr.Out.Host = b.Address
		pr.SetXForwarded()
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		p.Metrics.Errors.Add(1)
		http.Error(w, "backend unavailable", http.StatusBadGateway)
	}}
	rp.ServeHTTP(w, r)
}

// ServeTCP forwards bytes in both directions. Cancellation closes open connections.
func (p *Proxy) ServeTCP(ctx context.Context, l net.Listener) error {
	var wg sync.WaitGroup
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()
	defer wg.Wait()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() { defer wg.Done(); p.forward(ctx, c) }()
	}
}
func (p *Proxy) forward(ctx context.Context, c net.Conn) {
	defer c.Close()
	p.Metrics.TCP.Add(1)
	p.Metrics.ActiveTCP.Add(1)
	defer p.Metrics.ActiveTCP.Add(-1)
	h, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	b, release, err := p.Pool.Acquire(p.Algorithm, h)
	if err != nil {
		p.Metrics.Errors.Add(1)
		return
	}
	defer release()
	d := net.Dialer{Timeout: 3 * time.Second}
	upstream, err := d.DialContext(ctx, "tcp", b.Address)
	if err != nil {
		p.Metrics.Errors.Add(1)
		return
	}
	defer upstream.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close(); _ = upstream.Close() })
	defer stop()
	// Bound the lifetime of abandoned connections in this lab implementation.
	deadline := time.Now().Add(2 * time.Minute)
	_ = c.SetDeadline(deadline)
	_ = upstream.SetDeadline(deadline)
	var wg sync.WaitGroup
	wg.Add(2)
	copyHalf := func(dst, src net.Conn) {
		defer wg.Done()
		_, err := io.Copy(dst, src)
		if err != nil {
			_ = dst.Close()
			_ = src.Close()
			return
		}
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}
	go copyHalf(upstream, c)
	go copyHalf(c, upstream)
	wg.Wait()
}

// Probe uses TCP reachability, not application-level health. Existing requests are not retried.
func (p *Proxy) Probe(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		for _, b := range p.Pool.Snapshot() {
			d := net.Dialer{Timeout: 500 * time.Millisecond}
			c, err := d.DialContext(ctx, "tcp", b.Address)
			b.SetHealthy(err == nil)
			if c != nil {
				_ = c.Close()
			}
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (p *Proxy) Admin() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if p.Pool.Ready() == 0 {
			http.Error(w, "no healthy backend", 503)
			return
		}
		_, _ = io.WriteString(w, "ready\n")
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m := p.Metrics
		fmt.Fprintf(w, "# TYPE cloudmesh_http_requests_total counter\ncloudmesh_http_requests_total %d\n# TYPE cloudmesh_tcp_connections_total counter\ncloudmesh_tcp_connections_total %d\n# TYPE cloudmesh_proxy_errors_total counter\ncloudmesh_proxy_errors_total %d\n# TYPE cloudmesh_active_tcp gauge\ncloudmesh_active_tcp %d\n# TYPE cloudmesh_ready_backends gauge\ncloudmesh_ready_backends %d\n# TYPE cloudmesh_reconciliations_total counter\ncloudmesh_reconciliations_total %d\n# TYPE cloudmesh_discovery_errors_total counter\ncloudmesh_discovery_errors_total %d\n", m.HTTP.Load(), m.TCP.Load(), m.Errors.Load(), m.ActiveTCP.Load(), p.Pool.Ready(), m.Reconciles.Load(), m.DiscoveryErrors.Load())
	})
	return mux
}

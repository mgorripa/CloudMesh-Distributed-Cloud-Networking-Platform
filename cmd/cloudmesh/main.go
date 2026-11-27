package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cloudmesh/internal/discovery"
	"cloudmesh/internal/pool"
	"cloudmesh/internal/proxy"
)

func main() {
	httpAddr := flag.String("http", ":8080", "HTTP data plane address")
	tcpAddr := flag.String("tcp", ":9000", "TCP data plane address")
	adminAddr := flag.String("admin", "127.0.0.1:9090", "metrics and health address")
	backends := flag.String("backends", "", "comma-separated host:port endpoints")
	algorithm := flag.String("algorithm", "least-connections", "round-robin, least-connections, hash")
	service := flag.String("service", "", "Kubernetes Service to watch (in cluster)")
	namespace := flag.String("namespace", "", "Kubernetes namespace (defaults to pod namespace)")
	portName := flag.String("port-name", "http", "named Kubernetes Service port")
	probe := flag.Duration("probe-interval", 2*time.Second, "backend TCP probe interval")
	flag.Parse()
	if (*service == "") == (*backends == "") {
		log.Fatal("specify exactly one of -service or -backends")
	}
	if *probe <= 0 {
		log.Fatal("probe interval must be positive")
	}
	switch *algorithm {
	case "round-robin", "least-connections", "hash":
	default:
		log.Fatal("invalid algorithm")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	p := &pool.Pool{}
	m := &proxy.Metrics{}
	px := proxy.New(p, *algorithm, m)
	apply := func(addrs []string) error {
		if err := p.Replace(addrs); err != nil {
			return err
		}
		m.Reconciles.Add(1)
		log.Printf("reconciled %d discovered endpoints", len(p.Snapshot()))
		return nil
	}
	if *service != "" {
		c, err := discovery.InCluster(*namespace, *service, *portName)
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			for ctx.Err() == nil {
				err := c.Sync(ctx, apply)
				if ctx.Err() != nil {
					return
				}
				if err != nil && !errors.Is(err, io.EOF) {
					m.DiscoveryErrors.Add(1)
					log.Printf("discovery: %v; retrying", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		}()
	} else {
		if err := apply(strings.Split(*backends, ",")); err != nil {
			log.Fatal(err)
		}
	}
	tcpListener, err := net.Listen("tcp", *tcpAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer tcpListener.Close()
	httpListener, err := net.Listen("tcp", *httpAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer httpListener.Close()
	adminListener, err := net.Listen("tcp", *adminAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer adminListener.Close()
	hs := &http.Server{Handler: px, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	as := &http.Server{Handler: px.Admin(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	errs := make(chan error, 3)
	go func() { errs <- hs.Serve(httpListener) }()
	go func() { errs <- as.Serve(adminListener) }()
	go func() { errs <- px.ServeTCP(ctx, tcpListener) }()
	go px.Probe(ctx, *probe)
	log.Printf("HTTP %s TCP %s admin %s algorithm %s", httpListener.Addr(), tcpListener.Addr(), adminListener.Addr(), *algorithm)
	select {
	case <-ctx.Done():
	case err := <-errs:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server: %v", err)
		}
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(shutdown)
	_ = as.Shutdown(shutdown)
	px.Transport.CloseIdleConnections()
}

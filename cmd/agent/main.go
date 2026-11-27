// Command agent is a single-worker reconciler for an isolated Linux lab.
package main

import (
	"cloudmesh/internal/agent"
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

func main() {
	path := flag.String("config", "/etc/cloudmesh/network.json", "desired state JSON")
	once := flag.Bool("once", false, "reconcile once and exit")
	interval := flag.Duration("interval", 3*time.Second, "reconciliation interval")
	flag.Parse()
	if runtime.GOOS != "linux" || os.Getenv("CLOUDMESH_ISOLATED_LAB") != "1" {
		log.Fatal("run only in the disposable Linux lab container (see docs/linux-agent.md)")
	}
	if *interval < time.Second {
		log.Fatal("interval must be at least one second")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	k := agent.Linux{Runner: agent.ExecRunner{}}
	for {
		s, err := agent.LoadSpec(*path)
		if err == nil {
			var n int
			n, err = agent.Reconcile(ctx, k, s)
			if err == nil {
				log.Printf("reconcile changes=%d vni=%d allowed_tcp_port=%d", n, s.VNI, s.AllowedTCPPort)
			} else {
				log.Printf("reconcile partial_changes=%d", n)
			}
		}
		if err != nil {
			log.Printf("reconcile error: %v", err)
			if *once {
				os.Exit(1)
			}
		}
		if *once {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(*interval):
		}
	}
}

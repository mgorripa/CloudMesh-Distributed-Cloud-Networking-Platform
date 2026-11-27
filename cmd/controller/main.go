package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"cloudmesh/internal/controller"
	"cloudmesh/internal/discovery"
)

func main() {
	namespace := flag.String("namespace", "", "namespace to manage (defaults to pod namespace)")
	image := flag.String("proxy-image", "cloudmesh:local", "trusted local proxy image")
	flag.Parse()
	// Reuse the existing in-cluster TLS and service account configuration.
	cfg, err := discovery.InCluster(*namespace, "", "")
	if err != nil {
		log.Fatal(err)
	}
	c := controller.Controller{API: &controller.API{BaseURL: cfg.BaseURL, TokenFile: cfg.TokenFile, HTTP: cfg.HTTP}, Namespace: cfg.Namespace, Image: *image}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("CloudMeshNetwork controller watching namespace %s", c.Namespace)
	c.Run(ctx)
}

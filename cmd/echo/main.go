// A tiny HTTP backend for both HTTP proxy and raw TCP forwarding demos.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := flag.String("listen", ":8081", "listen address")
	id := flag.String("id", "", "backend identity")
	flag.Parse()
	if *id == "" {
		*id, _ = os.Hostname()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"backend": *id, "path": r.URL.Path, "method": r.Method})
	})
	s := http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(s.ListenAndServe())
}

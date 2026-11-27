# Design notes

## Goal

Build a small infrastructure software system that discovers Kubernetes Service endpoints and forwards TCP connections and HTTP requests to healthy backends. The Linux lab exercises the kernel-side mechanisms separately. A laptop prototype is not Google Distributed Cloud or a production-ready multi-cloud platform.

## Control plane

The Kubernetes client lists EndpointSlices with the selected Service label, builds a snapshot, then watches from the returned resourceVersion. It tracks each slice by name, flattens ready TCP endpoints of the requested port, handles IPv4 and IPv6 addresses, and relists after a watch ends or errors. Snapshot replacement is synchronized; deduplication prevents duplicate backend reservations. Existing backend pointers are reused so health and active counts survive updates. New backend addresses are optimistic until the first TCP probe. A transient API failure leaves the last snapshot in use; TCP probes still run. This favors availability at the cost of potentially stale membership. In production use a staleness budget, backoff with jitter, client-go informers, leader election where writes are required, and stronger endpoint identity.

## Data plane

HTTP is proxied using the standard library's ReverseProxy and a shared transport. TCP forwarding uses two goroutines with half-close semantics and cancellation. The implementation balances entire TCP connections, not packets. Existing requests are never replayed to another backend because arbitrary requests may not be idempotent. Health probes are TCP connects, not application readiness checks. Each active TCP connection consumes two sockets and copy goroutines. The prototype imposes a two-minute TCP lifetime but has no configurable global connection budget.

## Algorithms

* Round robin: a monotonically increasing counter indexes the healthy candidate set. Selection scans O(n) to filter unhealthy entries.
* Least connections: scan active counts in O(n); rotate tie-breaking to distribute short-lived requests. Reservations are synchronized; releases use atomic counters.
* Rendezvous hashing: choose the backend with the highest SHA-256-derived score for a key, O(n). The key is the HTTP header X-CloudMesh-Key when supplied, otherwise source IP; TCP uses source IP. Removing an endpoint remaps only keys assigned to that endpoint. This is a form of consistent hashing, not a hash ring.
* IPAM: owner-to-address and address-to-owner maps ensure idempotent allocation. Bounded scanning is O(limit), with O(1) release. IPv4 network and broadcast addresses and the first IPv6 address are reserved. The allocator is in-memory; restart-safe, multi-controller allocation is outside scope.

## Security and production gaps

The Kubernetes Role is namespace-scoped and read-only. The proxy runs unprivileged; the Linux lab is a separate privileged executable. In-cluster API calls validate the cluster CA and reread the service account token on each request. The HTTP/TCP proxy is plaintext, has no authentication or authorization, and is intended for a trusted local lab. The admin interface binds to loopback by default. Do not expose this application to the public internet. There is no HA control plane, custom CRD, CNI, persistent IPAM, TLS termination, eBPF program or production network policy controller. BGP and actual GCP connectivity are extension work. No claim of GDC implementation or compatibility is made.

## Testing strategy

Unit tests cover scheduling, reconciliation, health probes, IPAM, generated namespace commands and mock Kubernetes list/watch. Integration tests use real loopback TCP/HTTP sockets, including IPv6 when available. The local shell demo asserts traffic distribution and failure handling. Go race detector checks concurrent access. Full kind and privileged Linux tests must be run separately on a suitable host.

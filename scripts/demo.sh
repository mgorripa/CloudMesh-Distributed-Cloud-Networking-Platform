#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d); pids=()
cleanup() { for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done; wait 2>/dev/null || true; rm -rf "$work"; }
trap cleanup EXIT
bin/echo -listen 127.0.0.1:18081 -id alpha >"$work/alpha.log" 2>&1 & pids+=("$!")
bin/echo -listen 127.0.0.1:18082 -id beta >"$work/beta.log" 2>&1 & pids+=("$!")
for port in 18081 18082; do for i in {1..50}; do curl -fsS "http://127.0.0.1:$port" >/dev/null 2>&1 && break; sleep .1; done; curl -fsS "http://127.0.0.1:$port" >/dev/null; done
bin/cloudmesh -http 127.0.0.1:18080 -tcp 127.0.0.1:19000 -admin 127.0.0.1:19090 -backends 127.0.0.1:18081,127.0.0.1:18082 -algorithm round-robin -probe-interval 200ms >"$work/proxy.log" 2>&1 & pids+=("$!")
for i in {1..50}; do curl -fsS http://127.0.0.1:19090/readyz >/dev/null 2>&1 && break; sleep .1; done
printf 'HTTP distribution:\n'; for i in {1..6}; do curl -fsS http://127.0.0.1:18080/api; done | tee "$work/responses"
grep -q alpha "$work/responses"; grep -q beta "$work/responses"
printf 'Raw TCP forwarding (HTTP carried through TCP):\n'; curl -fsS http://127.0.0.1:19000/api
kill "${pids[0]}"; wait "${pids[0]}" 2>/dev/null || true
for i in {1..50}; do if curl -fsS http://127.0.0.1:19090/metrics | grep -q '^cloudmesh_ready_backends 1$'; then break; fi; sleep .1; done
printf 'After backend alpha stops:\n'; for i in {1..5}; do curl -fsS http://127.0.0.1:18080/api; done | tee "$work/failover"
if grep -q alpha "$work/failover"; then echo 'failover failed'; exit 1; fi
grep -q beta "$work/failover"
printf 'Metrics:\n'; curl -fsS http://127.0.0.1:19090/metrics
printf 'PASS: distribution, TCP forwarding, backend failover\n'

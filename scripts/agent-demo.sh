#!/usr/bin/env bash
# This demo creates one disposable privileged container. No host mounts,
# host network, Docker socket, or Kubernetes resources are used.
set -euo pipefail
cd "$(dirname "$0")/.."
name="cloudmesh-agent-demo-$$"
image="cloudmesh-agent:local"
created=0
cleanup() {
    local rc="$1"
    if [[ "$created" == 1 ]]; then
        if [[ "$rc" != 0 ]]; then
            echo "Lab failed; collecting state before cleanup" >&2
            docker exec "$name" ip netns list >&2 || true
            docker exec "$name" ip -n cma-site-a -d link show >&2 || true
            docker exec "$name" ip -n cma-site-b -d link show >&2 || true
            docker exec "$name" cat /tmp/agent.log >&2 || true
        fi
        docker rm -f "$name" >/dev/null 2>&1 || true
    fi
}
trap 'cleanup "$?"' EXIT
# Fail early if the Docker engine is unavailable.
docker info >/dev/null
docker build -f Dockerfile.agent -t "$image" .
docker run --rm -d --privileged --network none --name "$name" --entrypoint /bin/sleep "$image" infinity >/dev/null
created=1
run() { docker exec "$name" "$@"; }
site() { run ip netns exec "$@"; }
request() { local ns="$1"; shift; site "$ns" curl --noproxy '*' -fsS --connect-timeout 2 --max-time 4 "$@"; }
wait_for() {
    local i
    for ((i=0;i<25;i++)); do
        if "$@" >/dev/null 2>&1; then return 0; fi
        sleep 1
    done
    echo "Timed out waiting for: $*" >&2
    return 1
}
expect_backend() { local out; out="$(request cma-site-a "$@")"; printf '%s\n' "$out"; [[ "$out" == *'"backend":"site-b"'* ]]; }
expect_blocked() {
    local rc=0
    request cma-site-a "$@" >/dev/null 2>&1 || rc=$?
    if [[ "$rc" != 28 ]]; then echo "Expected TCP timeout (curl 28), got $rc" >&2; return 1; fi
}

echo '== Initial reconciliation =='
run /agent -once
second="$(run /agent -once 2>&1)"
printf '%s\n' "$second"
[[ "$second" == *'changes=0 '* ]]
echo 'PASS: second reconciliation made zero mutations'

# Start two live listeners so denied traffic is not confused with a missing server.
docker exec -d "$name" ip netns exec cma-site-b /echo -listen :8081 -id site-b
docker exec -d "$name" ip netns exec cma-site-b /echo -listen :8082 -id site-b
wait_for request cma-site-b http://127.0.0.1:8081/api
wait_for request cma-site-b http://127.0.0.1:8082/api
wait_for request cma-site-b 'http://[::1]:8082/api'

echo '== IPv4 and IPv6 overlay connectivity =='
site cma-site-a ping -c 2 -W 2 10.44.0.2
site cma-site-a ping -6 -c 2 -W 2 fd44:44::2
expect_backend http://10.44.0.2:8081/api
expect_backend 'http://[fd44:44::2]:8081/api'
echo 'PASS: IPv4 and IPv6 TCP/HTTP over VXLAN'

echo '== IPv4 virtual service / DNAT =='
# Explicit source ensures the lab tests the overlay path, not loopback source selection.
expect_backend --interface 10.44.0.1 http://10.44.0.100:8081/api
echo 'PASS: virtual service translated to remote backend'

echo '== Firewall isolation =='
expect_blocked http://10.44.0.2:8082/api
expect_blocked 'http://[fd44:44::2]:8082/api'
echo 'PASS: live port 8082 is blocked over both address families'

echo '== Continuous drift recovery =='
# Delete an owned overlay device, then start the reconciliation loop.
run ip -n cma-site-a link del cma-vxlan
if run ip -n cma-site-a link show dev cma-vxlan >/dev/null 2>&1; then echo 'Interface unexpectedly exists' >&2; exit 1; fi
# The shell redirects agent logs inside the disposable container.
docker exec -d "$name" sh -c 'exec /agent -interval=2s >/tmp/agent.log 2>&1'
wait_for request cma-site-a http://10.44.0.2:8081/api
expect_backend http://10.44.0.2:8081/api
expect_backend 'http://[fd44:44::2]:8081/api'
echo 'PASS: deleted VXLAN interface recreated and traffic restored'

echo '== Desired-state update (port 8082) =='
# Atomic replacement prevents the agent from reading a half-written JSON file.
run sh -c 'printf "%s\n" "{\"vni\":44,\"allowedTCPPort\":8082}" > /etc/cloudmesh/network.tmp && mv /etc/cloudmesh/network.tmp /etc/cloudmesh/network.json'
wait_for site cma-site-b iptables -C CMA-FILTER -p tcp --dport 8082 -j ACCEPT
wait_for site cma-site-b ip6tables -C CMA-FILTER -p tcp --dport 8082 -j ACCEPT
wait_for site cma-site-a iptables -C CMA-FILTER -p tcp --dport 8082 -j ACCEPT
wait_for site cma-site-a ip6tables -C CMA-FILTER -p tcp --dport 8082 -j ACCEPT
expect_backend http://10.44.0.2:8082/api
expect_backend 'http://[fd44:44::2]:8082/api'
expect_blocked http://10.44.0.2:8081/api
expect_blocked 'http://[fd44:44::2]:8081/api'
echo 'PASS: policy update applied without restarting the agent'

echo '== Kernel state and logs =='
run ip -n cma-site-a -d link show dev cma-vxlan
run ip -n cma-site-a addr show dev cma-vxlan
site cma-site-b iptables -S CMA-FILTER
site cma-site-b ip6tables -S CMA-FILTER
site cma-site-a iptables -t nat -S CMA-DNAT
run cat /tmp/agent.log
echo 'PASS: Linux agent reconciliation, VXLAN, IPv4/IPv6, firewall, DNAT and drift recovery'

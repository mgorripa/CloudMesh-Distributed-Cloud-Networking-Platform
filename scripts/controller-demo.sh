#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
# Use only the explicitly named local cluster; never the current context.
cluster=cloudmesh-controller
k() { kubectl --context "kind-$cluster" "$@"; }
if ! kind get clusters | grep -Fxq "$cluster"; then
  kind create cluster --name "$cluster" --config deploy/kubernetes/kind.yaml
fi
make image
docker build -f Dockerfile.controller -t cloudmesh-controller:local .
kind load docker-image cloudmesh:local cloudmesh-controller:local --name "$cluster"
k apply -f deploy/kubernetes/cloudmesh.yaml
k -n cloudmesh rollout status deployment/echo --timeout=180s
k apply -f deploy/controller/crd.yaml
k wait --for=condition=Established crd/cloudmeshnetworks.networking.cloudmesh.io --timeout=60s
k apply -f deploy/controller/controller.yaml
# Recreate controller pod when rerunning after local image changes.
k -n cloudmesh rollout restart deployment/cloudmesh-controller
k -n cloudmesh rollout status deployment/cloudmesh-controller --timeout=120s
k apply -f examples/application-network.yaml
k -n cloudmesh wait --for=jsonpath='{.status.phase}'=Ready cmn/application-network --timeout=180s
k -n cloudmesh get cmn,deploy,svc
printf '\nRun in a separate terminal:\n kubectl --context kind-%s -n cloudmesh port-forward svc/cm-application-network 18080:8080 19000:9000\n' "$cluster"

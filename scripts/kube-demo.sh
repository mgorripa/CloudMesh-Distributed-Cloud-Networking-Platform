#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
kind create cluster --name cloudmesh --config deploy/kubernetes/kind.yaml
make image
kind load docker-image cloudmesh:local --name cloudmesh
kubectl --context kind-cloudmesh apply -f deploy/kubernetes/cloudmesh.yaml
kubectl --context kind-cloudmesh -n cloudmesh rollout status deployment/echo --timeout=180s
kubectl --context kind-cloudmesh -n cloudmesh rollout status deployment/cloudmesh --timeout=180s
kubectl --context kind-cloudmesh -n cloudmesh get endpointslices
printf '\nRun: kubectl --context kind-cloudmesh -n cloudmesh port-forward deployment/cloudmesh 18080:8080 19000:9000 19090:9090\n'

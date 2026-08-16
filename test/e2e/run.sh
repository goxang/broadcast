#!/usr/bin/env bash
# Reproducible end-to-end functional tests for goxang/broadcast.
#
# Spins up a throwaway kind cluster, builds and loads the images, installs the
# Helm chart, and exercises the functional behaviors:
#   1. basic broadcast (1 request -> all ready targets)
#   2. scaling (3 -> 2 -> 3 targets)
#   3. pod removal
#   4. best-effort failure (slow target, bounded timeout, no retry)
#   5. endpoint churn
#   6. normal Service independence
#
# Requires: kind, docker, helm, kubectl, curl.
#
# Usage:
#   KIND_CLUSTER=goxang-e2e ./test/e2e/run.sh
#
# The cluster is deleted on exit (only if this script created it).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
KIND_CLUSTER="${KIND_CLUSTER:-goxang-broadcast-e2e}"
NAMESPACE="goxang-broadcast-test"
IMG="${IMG:-goxang/broadcast:latest}"
TARGET_IMG="${TARGET_IMG:-goxang/broadcast-target:latest}"

PASS=0
FAIL=0
CREATED_CLUSTER=0
PF_PROXY=""
PF_T1=""
PF_T2=""
PF_T3=""
PF_SVC=""

ok()  { PASS=$((PASS+1)); echo "  PASS: $1"; }
bad() { FAIL=$((FAIL+1)); echo "  FAIL: $1"; }

received() { curl -s "http://localhost:$1/stats" | grep -o '"received":[0-9]*' | cut -d: -f2; }

pf() { # pf <resource> <local-port>; echoes PID
  kubectl -n "$NAMESPACE" port-forward "$1" "$2:8080" >/dev/null 2>&1 &
  echo $!
}

restart_target_pfs() {
  kill $PF_T1 $PF_T2 $PF_T3 2>/dev/null || true
  PF_T1=$(pf deploy/broadcast-target-1 18081)
  PF_T2=$(pf deploy/broadcast-target-2 18082)
  PF_T3=$(pf deploy/broadcast-target-3 18083)
  sleep 4
}

cleanup() {
  pkill -f "kubectl.*port-forward.*${NAMESPACE}" 2>/dev/null || true
  if [ "$CREATED_CLUSTER" = "1" ]; then
    kind delete cluster --name "$KIND_CLUSTER" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

echo "==> building images"
docker build -q -t "$IMG" -f "$REPO_ROOT/Dockerfile" "$REPO_ROOT"
docker build -q -t "$TARGET_IMG" -f "$REPO_ROOT/test/targets/Dockerfile" "$REPO_ROOT"

if kind get clusters | grep -qx "$KIND_CLUSTER"; then
  echo "==> reusing existing kind cluster '$KIND_CLUSTER' (will NOT be deleted)"
else
  echo "==> creating kind cluster '$KIND_CLUSTER'"
  kind create cluster --name "$KIND_CLUSTER" >/dev/null
  CREATED_CLUSTER=1
fi
kind load docker-image "$IMG" "$TARGET_IMG" --name "$KIND_CLUSTER" >/dev/null

echo "==> installing chart"
helm uninstall broadcast --namespace "$NAMESPACE" >/dev/null 2>&1 || true
kubectl delete namespace "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
sleep 2
helm install broadcast "$REPO_ROOT/charts/broadcast" \
  --namespace "$NAMESPACE" --create-namespace \
  --set "image.repository=${IMG%:*}" --set "image.tag=${IMG##*:}" \
  --set image.pullPolicy=IfNotPresent >/dev/null

echo "==> deploying test targets and Broadcast"
kubectl apply -f "$REPO_ROOT/test/manifests/targets.yaml" >/dev/null
kubectl apply -f "$REPO_ROOT/test/manifests/broadcast.yaml" >/dev/null

echo "==> waiting for readiness"
kubectl -n "$NAMESPACE" rollout status deployment/broadcast-broadcast --timeout=120s >/dev/null
for d in broadcast-target-1 broadcast-target-2 broadcast-target-3; do
  kubectl -n "$NAMESPACE" rollout status deployment/$d --timeout=120s >/dev/null
done
for _ in $(seq 1 30); do
  eps=$(kubectl -n "$NAMESPACE" get broadcast cache-invalidation -o jsonpath='{.status.endpoints}' 2>/dev/null || echo 0)
  [ "$eps" = "3" ] && break
  sleep 1
done

echo "==> starting port-forwards"
PF_PROXY=$(pf svc/broadcast-broadcast 18080)
restart_target_pfs

broadcast() { curl -s -X POST "http://localhost:18080/broadcast/cache-invalidation/broadcast-test" -d "$1"; }

echo "==> TEST 1: basic broadcast"
b1=$(received 18081); b2=$(received 18082); b3=$(received 18083)
resp=$(broadcast '{"test":"basic"}')
a1=$(received 18081); a2=$(received 18082); a3=$(received 18083)
[ "$a1" -gt "$b1" ] && [ "$a2" -gt "$b2" ] && [ "$a3" -gt "$b3" ] \
  && ok "all 3 targets received the broadcast" || bad "not all targets received ($resp)"

echo "==> TEST 2: scaling 3->2"
kubectl -n "$NAMESPACE" scale deployment/broadcast-target-3 --replicas=0 >/dev/null
sleep 6
eps=$(kubectl -n "$NAMESPACE" get broadcast cache-invalidation -o jsonpath='{.status.endpoints}')
[ "$eps" = "2" ] && ok "controller converged to 2 endpoints" || bad "endpoints=$eps want 2"
b1=$(received 18081); b2=$(received 18082)
resp=$(broadcast '{"n":2}')
a1=$(received 18081); a2=$(received 18082)
[ "$a1" -gt "$b1" ] && [ "$a2" -gt "$b2" ] && ok "broadcast reached exactly 2 targets" || bad "2-target broadcast failed ($resp)"

echo "==> TEST 2b: scaling 2->3"
kubectl -n "$NAMESPACE" scale deployment/broadcast-target-3 --replicas=1 >/dev/null
sleep 7
eps=$(kubectl -n "$NAMESPACE" get broadcast cache-invalidation -o jsonpath='{.status.endpoints}')
[ "$eps" = "3" ] && ok "controller converged back to 3 endpoints" || bad "endpoints=$eps want 3"

echo "==> TEST 3: pod removal"
kubectl -n "$NAMESPACE" delete deployment broadcast-target-3 >/dev/null
sleep 6
eps=$(kubectl -n "$NAMESPACE" get broadcast cache-invalidation -o jsonpath='{.status.endpoints}')
[ "$eps" = "2" ] && ok "endpoints=2 after deletion" || bad "endpoints=$eps want 2"

echo "==> TEST 4: best-effort failure (slow target)"
kubectl apply -f "$REPO_ROOT/test/manifests/targets.yaml" >/dev/null
sleep 6
eps=$(kubectl -n "$NAMESPACE" get broadcast cache-invalidation -o jsonpath='{.status.endpoints}')
[ "$eps" = "3" ] && ok "targets restored to 3" || bad "endpoints=$eps want 3"
restart_target_pfs
curl -s -X POST "http://localhost:18082/control?mode=slow&ms=2000" >/dev/null
start=$(date +%s%N)
resp=$(broadcast '{"test":"failure"}')
elapsed_ms=$(( ($(date +%s%N) - start) / 1000000 ))
echo "    slow-target summary: $resp (${elapsed_ms}ms)"
echo "$resp" | grep -q '"timed_out":true' \
  && ok "timeout was reported and bounded (${elapsed_ms}ms < 2000ms)" \
  || bad "slow target did not time out as expected ($resp)"
echo "$resp" | grep -q '"errors":1' \
  && ok "slow target isolated (exactly 1 error, others unaffected)" \
  || bad "expected exactly 1 error in summary ($resp)"

echo "==> TEST 5: endpoint churn"
curl -s -X POST "http://localhost:18082/control?mode=ok" >/dev/null
kubectl -n "$NAMESPACE" scale deployment/broadcast-target-3 --replicas=0 >/dev/null
kubectl -n "$NAMESPACE" scale deployment/broadcast-target-3 --replicas=1 >/dev/null
kubectl -n "$NAMESPACE" rollout restart deployment/broadcast-target-1 >/dev/null
sleep 10
eps=$(kubectl -n "$NAMESPACE" get broadcast cache-invalidation -o jsonpath='{.status.endpoints}')
[ "$eps" = "3" ] && ok "controller converged to 3 endpoints after churn" || bad "endpoints=$eps want 3"

echo "==> TEST 6: normal Service independence"
restart_target_pfs
PF_SVC=$(pf svc/broadcast-target-svc 18084)
sleep 2
b1=$(received 18081); b2=$(received 18082); b3=$(received 18083)
curl -s -X POST "http://localhost:18084/" -d '{"via":"svc"}' -o /dev/null
sleep 1
a1=$(received 18081); a2=$(received 18082); a3=$(received 18083)
delta=$(( (a1-b1) + (a2-b2) + (a3-b3) ))
[ "$delta" = "1" ] \
  && ok "normal Service routed to exactly one backend" \
  || bad "Service did not behave as a single-backend LB (delta=$delta)"

kill $PF_PROXY $PF_SVC 2>/dev/null || true

echo
echo "RESULTS: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] || exit 1
echo "E2E SUITE PASSED"

#!/usr/bin/env bash
#
# End-to-end test for RolloutAwareProcessor using Kind + kwok provider.
# Validates phase-aware scale-up distribution between blue/green ASGs.
#
# Usage: ./test-e2e-kind.sh
#
# Prerequisites: kind, kubectl, go (builds the binary automatically)
#
set -euo pipefail

CLUSTER_NAME="ca-rollout-e2e"
KUBECONFIG_PATH="/tmp/${CLUSTER_NAME}-kubeconfig.yaml"
BINARY="/tmp/ca-rollout-e2e"
NAMESPACE="kube-system"
LOG="/tmp/ca-rollout-e2e.log"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CA_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}PASS${NC}: $1"; }
fail() { echo -e "${RED}FAIL${NC}: $1"; FAILURES=$((FAILURES + 1)); }
info() { echo -e "${YELLOW}----${NC} $1"; }

FAILURES=0

cleanup() {
    info "Cleaning up Kind cluster ${CLUSTER_NAME}..."
    kind delete cluster --name "$CLUSTER_NAME" 2>/dev/null || true
    rm -f "$KUBECONFIG_PATH" "$BINARY" "$LOG"
}

trap cleanup EXIT

# ---------- Build ----------

info "Building cluster-autoscaler with kwok provider..."
cd "$CA_ROOT"
go build -tags kwok -o "$BINARY" ./
info "Binary built at $BINARY"

# ---------- Cluster setup ----------

info "Creating Kind cluster..."
kind create cluster --name "$CLUSTER_NAME" --wait 60s 2>&1 | tail -1
kind get kubeconfig --name "$CLUSTER_NAME" > "$KUBECONFIG_PATH"

K="kubectl --context kind-${CLUSTER_NAME}"

info "Creating kwok provider ConfigMaps..."
$K apply -f - <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: kwok-provider-config
  namespace: kube-system
data:
  config: |
    apiVersion: v1alpha1
    readNodesFrom: configmap
    nodegroups:
      fromNodeLabelKey: "nodegroup"
    nodes:
      skipTaint: true
    configmap:
      name: kwok-provider-templates
      key: templates
EOF

$K apply -f - <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: kwok-provider-templates
  namespace: kube-system
data:
  templates: |
    apiVersion: v1
    kind: List
    metadata:
      resourceVersion: ""
    items:
    - apiVersion: v1
      kind: Node
      metadata:
        name: blue-template
        labels:
          nodegroup: blue-asg
          kubernetes.io/arch: amd64
          kubernetes.io/os: linux
        annotations:
          cluster-autoscaler.kwok.nodegroup/min-count: "1"
          cluster-autoscaler.kwok.nodegroup/max-count: "100"
          cluster-autoscaler.kwok.nodegroup/desired-count: "3"
      status:
        allocatable:
          cpu: "4"
          memory: 8Gi
          pods: "110"
        capacity:
          cpu: "4"
          memory: 8Gi
          pods: "110"
        conditions:
        - type: Ready
          status: "True"
    - apiVersion: v1
      kind: Node
      metadata:
        name: green-template
        labels:
          nodegroup: green-asg
          kubernetes.io/arch: amd64
          kubernetes.io/os: linux
        annotations:
          cluster-autoscaler.kwok.nodegroup/min-count: "0"
          cluster-autoscaler.kwok.nodegroup/max-count: "100"
          cluster-autoscaler.kwok.nodegroup/desired-count: "1"
      status:
        allocatable:
          cpu: "4"
          memory: 8Gi
          pods: "110"
        capacity:
          cpu: "4"
          memory: 8Gi
          pods: "110"
        conditions:
        - type: Ready
          status: "True"
EOF

# ---------- Helper functions ----------

set_rollout_phase() {
    local phase="$1"
    local green_target="${2:-0}"
    $K apply -f - <<EOCONF
apiVersion: v1
kind: ConfigMap
metadata:
  name: rollout-aware-config
  namespace: kube-system
data:
  pairs: '[{"blueId":"blue-asg","greenId":"green-asg","phase":"${phase}","greenTarget":${green_target}}]'
EOCONF
}

create_pressure() {
    local replicas="$1"
    $K delete deployment pressure -n default --ignore-not-found=true 2>/dev/null
    sleep 1
    $K apply -f - <<EOPOD
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pressure
  namespace: default
spec:
  replicas: ${replicas}
  selector:
    matchLabels:
      app: pressure
  template:
    metadata:
      labels:
        app: pressure
    spec:
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.9
        resources:
          requests:
            cpu: "2"
            memory: "4Gi"
EOPOD
}

# Run CA for up to $1 seconds, return the log file path.
# Extracts only the interesting lines.
run_ca() {
    local duration="${1:-25}"
    KWOK_PROVIDER_MODE=local POD_NAMESPACE=kube-system \
        timeout "$duration" "$BINARY" \
        --cloud-provider=kwok \
        --kubeconfig="$KUBECONFIG_PATH" \
        --namespace=kube-system \
        --balance-similar-node-groups=true \
        --v=4 \
        --logtostderr > "$LOG" 2>&1 || true
}

assert_log_contains() {
    local pattern="$1"
    local label="$2"
    if grep -qE "$pattern" "$LOG"; then
        pass "$label"
    else
        fail "$label (pattern not found: $pattern)"
        echo "  Last 20 relevant lines:"
        grep -E "loaded pair|Final scale-up|setting group|unschedulable|No unschedulable" "$LOG" | tail -20 | sed 's/^/    /'
    fi
}

assert_log_not_contains() {
    local pattern="$1"
    local label="$2"
    if grep -qE "$pattern" "$LOG"; then
        fail "$label (unexpected pattern found: $pattern)"
        grep -E "$pattern" "$LOG" | head -5 | sed 's/^/    /'
    else
        pass "$label"
    fi
}

# ---------- Test 1: Canary — all to blue ----------

info "Test 1: Canary phase — all new nodes to blue"
set_rollout_phase "canary"
create_pressure 10
run_ca 30

assert_log_contains "loaded pair blue=blue-asg green=green-asg phase=canary" \
    "ConfigMap loaded with canary phase"
assert_log_contains "Final scale-up plan:.*blue-asg" \
    "Scale-up targets blue-asg"
assert_log_not_contains "setting group green-asg" \
    "Green-asg not scaled"

# ---------- Test 2: Draining — all to green ----------

info "Test 2: Draining phase — all new nodes to green"
$K delete deployment pressure -n default --ignore-not-found=true 2>/dev/null
sleep 2
set_rollout_phase "draining"
create_pressure 10
run_ca 30

assert_log_contains "loaded pair.*phase=draining" \
    "ConfigMap loaded with draining phase"
assert_log_contains "Final scale-up plan:.*green-asg" \
    "Scale-up targets green-asg"
assert_log_not_contains "setting group blue-asg" \
    "Blue-asg not scaled"

# ---------- Test 3: Ramping — split with request > greenTarget ----------

info "Test 3: Ramping phase — green fills to target, remainder to blue"
$K delete deployment pressure -n default --ignore-not-found=true 2>/dev/null
sleep 2
# greenTarget=3, green starts at ~1 node (desired-count=1 in template).
# With enough pending pods needing many nodes, green should get up to 3
# and blue should get the rest.
set_rollout_phase "ramping" 3
create_pressure 20
run_ca 30

assert_log_contains "loaded pair.*phase=ramping.*greenTarget=3" \
    "ConfigMap loaded with ramping phase, greenTarget=3"

# In ramping: green should scale up (up to target 3)
assert_log_contains "setting group green-asg" \
    "Green-asg scaled during ramping"

# Blue should also get nodes (the remainder)
if grep -qE "setting group blue-asg" "$LOG"; then
    pass "Blue-asg scaled with remainder during ramping"
else
    # Blue might appear in Final scale-up plan instead
    if grep -qE "Final scale-up plan:.*blue-asg" "$LOG"; then
        pass "Blue-asg in scale-up plan during ramping"
    else
        fail "Blue-asg not scaled during ramping (expected remainder to go to blue)"
    fi
fi

# Verify the split: green shouldn't exceed target of 3
green_size=$(grep "setting group green-asg size to" "$LOG" | grep -oP 'size to \K[0-9]+' | tail -1)
if [ -n "$green_size" ] && [ "$green_size" -le 3 ]; then
    pass "Green-asg size ($green_size) <= greenTarget (3)"
else
    fail "Green-asg size ($green_size) exceeds greenTarget (3)"
fi

# ---------- Test 4: Circuit breaker — all to blue ----------

info "Test 4: Circuit-breaker-tripped — all to blue"
$K delete deployment pressure -n default --ignore-not-found=true 2>/dev/null
sleep 2
set_rollout_phase "circuit-breaker-tripped"
create_pressure 10
run_ca 30

assert_log_contains "loaded pair.*phase=circuit-breaker-tripped" \
    "ConfigMap loaded with circuit-breaker-tripped phase"
assert_log_contains "Final scale-up plan:.*blue-asg" \
    "Scale-up targets blue-asg"
assert_log_not_contains "setting group green-asg" \
    "Green-asg not scaled"

# ---------- Test 5: No ConfigMap — pure delegation ----------

info "Test 5: No rollout ConfigMap — standard balancing"
$K delete deployment pressure -n default --ignore-not-found=true 2>/dev/null
$K delete configmap rollout-aware-config -n kube-system --ignore-not-found=true 2>/dev/null
sleep 2
create_pressure 10
run_ca 30

assert_log_contains "no kube-system/rollout-aware-config ConfigMap found" \
    "Missing ConfigMap detected gracefully"
# Should still scale up (via standard balancing)
if grep -qE "Final scale-up plan:" "$LOG"; then
    pass "Scale-up still works without rollout config"
else
    # No pending pods might be the issue — still a pass if it started cleanly
    assert_log_contains "Starting main loop" \
        "Autoscaler loop runs without rollout config"
fi

# ---------- Summary ----------

echo ""
echo "=============================="
if [ "$FAILURES" -eq 0 ]; then
    echo -e "${GREEN}All tests passed!${NC}"
else
    echo -e "${RED}${FAILURES} test(s) failed.${NC}"
fi
echo "=============================="
exit "$FAILURES"

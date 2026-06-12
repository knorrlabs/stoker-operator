#!/usr/bin/env bash
# Sets up the e2e test environment:
#   1. Creates a kind cluster
#   2. Pre-pulls test fixture images
#   3. Builds and loads both operator + agent images
#   4. Installs cert-manager (webhook TLS)
#   5. Deploys the operator via Helm with local images
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

KIND_CLUSTER="${KIND_CLUSTER:-stoker-e2e}"
CONTROLLER_IMG="${CONTROLLER_IMG:-stoker-operator:e2e}"
AGENT_IMG="${AGENT_IMG:-stoker-agent:e2e}"

echo "=== E2E Test Setup ==="

# kind_load loads an image into the kind cluster. `kind load docker-image`
# fails with "ctr: content digest ... not found" when Docker uses the
# containerd image store (default on newer Docker Desktop): docker's export
# references multi-platform manifests whose blobs aren't present locally.
# Fall back to a single-platform archive load, which always has all blobs.
kind_load() {
    local img="$1"
    if kind load docker-image "${img}" --name "${KIND_CLUSTER}" 2>/dev/null; then
        return 0
    fi
    echo "  kind load docker-image failed for ${img}; falling back to image-archive"
    local arch tar
    arch="$(docker version --format '{{.Server.Arch}}')"
    tar="$(mktemp -t kind-img-XXXXXX).tar"
    docker save --platform "linux/${arch}" "${img}" -o "${tar}"
    kind load image-archive "${tar}" --name "${KIND_CLUSTER}"
    rm -f "${tar}"
}

# 1. Create kind cluster (if not exists)
echo "-> Checking kind cluster '${KIND_CLUSTER}'..."
if ! kind get clusters 2>/dev/null | grep -q "^${KIND_CLUSTER}$"; then
    echo "  Creating kind cluster..."
    kind create cluster --name "${KIND_CLUSTER}" --wait 60s
else
    echo "  Cluster already exists."
fi

kubectl cluster-info --context "kind-${KIND_CLUSTER}" >/dev/null 2>&1

# 2. Pre-pull test fixture images to avoid pull contention during parallel tests
echo "-> Pre-pulling test fixture images..."
docker pull alpine:3.20
docker pull curlimages/curl:latest
kind_load alpine:3.20
kind_load curlimages/curl:latest

# 3. Build both images
echo "-> Building controller image '${CONTROLLER_IMG}'..."
cd "${PROJECT_ROOT}"
docker build -t "${CONTROLLER_IMG}" .

echo "-> Building agent image '${AGENT_IMG}'..."
docker build -t "${AGENT_IMG}" -f Dockerfile.agent .

# 4. Load images into kind
echo "-> Loading images into kind..."
kind_load "${CONTROLLER_IMG}"
kind_load "${AGENT_IMG}"

# 5. Install cert-manager (required for webhook TLS)
echo "-> Installing cert-manager..."
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
echo "  Waiting for cert-manager deployments..."
kubectl wait --for=condition=Available deployment --all -n cert-manager --timeout=120s

# 6. Deploy operator via Helm with local images
echo "-> Deploying operator via Helm..."
helm upgrade --install stoker-operator "${PROJECT_ROOT}/charts/stoker-operator" \
  --namespace stoker-system --create-namespace \
  --set image.repository=stoker-operator \
  --set image.tag=e2e \
  --set image.pullPolicy=Never \
  --set agentImage.repository=stoker-agent \
  --set agentImage.tag=e2e \
  --set leaderElection.enabled=false \
  --set webhookReceiver.enabled=true \
  --wait --timeout 180s

# 7. Wait for controller readiness
echo "-> Waiting for controller readiness..."
kubectl wait --for=condition=Available deployment -l app.kubernetes.io/name=stoker-operator \
  -n stoker-system --timeout=120s

echo ""
echo "=== Setup Complete ==="
echo "  Cluster:         ${KIND_CLUSTER}"
echo "  Controller image: ${CONTROLLER_IMG}"
echo "  Agent image:      ${AGENT_IMG}"

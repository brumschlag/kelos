#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kind}"
REGISTRY="${REGISTRY:-ghcr.io/kelos-dev}"
LOCAL_IMAGE_TAG="${LOCAL_IMAGE_TAG:-local-dev}"
KELOS_CONSOLE_TOKEN="${KELOS_CONSOLE_TOKEN:-local-dev}"
if ! command -v kind >/dev/null 2>&1; then
  echo "Kind CLI not found in PATH" >&2
  exit 1
fi

if ! kind get clusters | grep -Fxq "${KIND_CLUSTER_NAME}"; then
  echo "Kind cluster ${KIND_CLUSTER_NAME} not found" >&2
  exit 1
fi

make image REGISTRY="${REGISTRY}" VERSION="${LOCAL_IMAGE_TAG}"

images=(
  "${REGISTRY}/kelos-controller:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/kelos-spawner:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/kelos-worker-runner:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/kelos-session-runtime:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/kelos-console-server:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/ghproxy:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/kelos-webhook-server:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/claude-code:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/codex:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/gemini:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/opencode:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/cursor:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/kelos-slack-server:${LOCAL_IMAGE_TAG}"
)

for image in "${images[@]}"; do
  kind load docker-image --name "${KIND_CLUSTER_NAME}" "${image}"
done

make build WHAT=cmd/kelos

kubectl create namespace kelos-system --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic kelos-console-server-auth \
  --namespace kelos-system \
  --from-file=token=<(printf '%s' "${KELOS_CONSOLE_TOKEN}") \
  --dry-run=client -o yaml | kubectl apply -f -

bin/kelos install --version "${LOCAL_IMAGE_TAG}" --image-pull-policy Never --values - <<EOF
controllerImage: "${REGISTRY}/kelos-controller"
ghproxy:
  image: "${REGISTRY}/ghproxy"
claudeCodeImage: "${REGISTRY}/claude-code"
codexImage: "${REGISTRY}/codex"
geminiImage: "${REGISTRY}/gemini"
opencodeImage: "${REGISTRY}/opencode"
cursorImage: "${REGISTRY}/cursor"
spawner:
  image: "${REGISTRY}/kelos-spawner"
workerRunner:
  image: "${REGISTRY}/kelos-worker-runner"
sessionRuntime:
  image: "${REGISTRY}/kelos-session-runtime"
consoleServer:
  enabled: true
  image: "${REGISTRY}/kelos-console-server"
  secretName: kelos-console-server-auth
webhookServer:
  image: "${REGISTRY}/kelos-webhook-server"
slackServer:
  image: "${REGISTRY}/kelos-slack-server"
EOF

kubectl rollout restart deployment/kelos-controller-manager -n kelos-system
kubectl rollout restart deployment/kelos-console-server -n kelos-system
kubectl rollout status deployment/kelos-controller-manager -n kelos-system
kubectl rollout status deployment/kelos-console-server -n kelos-system

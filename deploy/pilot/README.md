# Pilot deployment (software-engineering EKS)

Records the state of the `kelos-pilot` deployment on the
`software-engineering` cluster (account 565715328522, us-east-1), which runs this
fork rather than upstream kelos.

Everything here was applied by hand while developing the Foreman integration;
these files exist so the cluster can be reproduced or audited. They are not
wired to a GitOps controller — applying them is still a manual step.

## Why this fork

Foreman dispatches each pipeline phase as a kelos Task. Three fork additions are
required and are not in upstream (see kelos-dev/kelos#1566):

| Field | Why Foreman needs it |
| --- | --- |
| `Task.spec.envOverrides` | Per-phase model routing. A pooled Task cannot use `podOverrides`, and a long-lived worker's pod env cannot hold a per-Task value. |
| `Task.spec.preCommands` | Records a `git stash create` baseline before the agent runs, so the phase's patch excludes changes left by earlier tasks on the same worker. |
| `Task.spec.postCommands` | Uploads the phase's patch after the agent exits, without asking the model to run the transfer. |

Turn and tool-call counts in `TaskStatus.Results` are also fork-only; Foreman
reports them as phase metrics.

## Apply

```bash
# 1. CRDs (includes the fork-only Task fields)
kubectl apply --server-side --force-conflicts -f internal/manifests/install-crd.yaml

# 2. Controller. --force-conflicts is needed because the CRDs were first applied
#    by upstream kelos, which still owns spec.versions as a field manager.
helm upgrade --install kelos internal/manifests/charts/kelos \
  --namespace kelos-system \
  --values deploy/pilot/values.yaml

# 3. Pilot workloads
kubectl apply -f deploy/pilot/workloads.yaml
```

## Images

Built from this fork and pushed to both GHCR and ECR; EKS nodes pull from
same-account ECR via the node role, so no pull secret is needed. Both are
multi-arch (amd64 + arm64) because the cluster has Graviton nodes — an amd64-only
image lands on arm64 and fails with `exec format error`.

```bash
make image WHAT=cmd/kelos-controller     IMAGE_PLATFORMS=linux/amd64,linux/arm64 \
  REGISTRY=ghcr.io/brumschlag VERSION=<tag> PUSH=true
docker buildx imagetools create \
  --tag 565715328522.dkr.ecr.us-east-1.amazonaws.com/<name>:<tag> \
  ghcr.io/brumschlag/<name>:<tag>
```

ECR access needs the break-glass profile; the default Bedrock role is denied
`ecr:GetAuthorizationToken`.

## Gateway dependency

Agents reach Bedrock through the in-cluster LiteLLM gateway at
`bedrock-gateway.honcho.svc.cluster.local`, so no AWS credentials live in agent
pods. Foreman maps workflow model ids to the gateway's names via
`KELOS_MODEL_MAP`; see `gateway-models.md` for the `minimax` entry added for the
Foreman `documentation` phase.

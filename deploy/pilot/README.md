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

# 2. Controller. `kelos install` renders this same chart and applies it, which
#    is what produced the live state: there is no Helm release for kelos, no
#    release secret in kelos-system, and the controller Deployment carries no
#    Helm ownership metadata. A `helm upgrade --install` would therefore not
#    upgrade anything -- it would try to create a first release and be rejected
#    for adopting resources that lack `app.kubernetes.io/managed-by: Helm` and
#    `meta.helm.sh/release-name`. Use Helm only after deliberately adopting.
kelos install --namespace kelos-system --values deploy/pilot/values.yaml

# 3. Pilot workloads
kubectl apply -f deploy/pilot/workloads.yaml

# 4. The autonomous GitHub-issue spawner. Kept in its own file because its
#    promptTemplate is ~9k characters. `kelos install` must run first: the
#    spawner's Tasks inherit the controller's --claude-code-image.
kubectl apply -f deploy/pilot/taskspawner-inpulse-issues.yaml

# 5. Optional: an interactive Session. Requires the v0.55.0 CRDs from step 1,
#    since spec.idlePolicy did not exist in v0.49.0.
kubectl apply -f deploy/pilot/session.yaml
```

Note that `kelos install` needs a `kelos` binary built from this fork, not the
one on `PATH`: the chart is compiled into the CLI, so an older binary silently
installs its own older chart and drops values it does not recognise. Build it
with `make build WHAT=cmd/kelos` and run `./bin/kelos`.

Applying CRDs needs cluster-admin. The kubeconfig's exec block pins
`AWS_PROFILE=tellihealth-dev-bedrock`, and that role (group `kelos-operators`)
cannot create or patch CRDs, so step 1 requires the break-glass profile. Steps
3-5 work as the normal role.

## Images

Built from this fork and pushed to ECR; EKS nodes pull from same-account ECR via
the node role, so no pull secret is needed.

The images are **amd64-only**, and the cluster is mixed: 10 amd64 nodes plus 2
arm64 Graviton (`c6g.large`, added 2026-08-30). An amd64-only image landing on a
Graviton node fails with `exec format error`, so every kelos workload is pinned
to `kubernetes.io/arch: amd64` — the chart's `nodeSelector` for the controller
and telemetry CronJob, and `podOverrides.nodeSelector` for agent pods. Publishing
multi-arch images would remove that constraint and reclaim the Graviton capacity.

The three fork images share one tag rather than a per-image suffix, since they
are cut from the same commit and only deployed together.

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

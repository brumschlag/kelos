# bedrock-gateway restarts (open issue, not yet diagnosed)

The shared LiteLLM gateway in `honcho` restarts periodically: 26 restarts over
roughly 7 days before the Foreman work began, and 2 more during it. Every restart
makes the Service endpoint list empty for ~45-60s, so all consumers — Foreman
phases included — fail with `ConnectionRefused` until it recovers.

## What is known

- Terminations report `exitCode: 137` with `reason: Error`.
- Steady-state memory is ~1028Mi against `requests.memory: 1Gi` and
  `limits.memory: 2Gi`. It sits permanently at its request.
- `replicas: 1`.

## What is NOT the cause

Two plausible-sounding explanations were checked and ruled out:

- **Slow startup outrunning a probe.** There is a `startupProbe` with
  `failureThreshold: 60` and `periodSeconds: 5` — five minutes of grace.
- **Rollout gap from the update strategy.** `maxUnavailable: 25%` of 1 replica
  rounds *down* to 0 and `maxSurge` rounds *up* to 1, so a rolling update starts the
  replacement before removing the old pod. A deliberate `kubectl rollout restart` is
  safe by design.

The outage comes from unplanned restarts of the single replica, not from planned
rollouts.

## Remaining candidates

Exit 137 is SIGKILL, which fits either:

1. **OOM.** Memory sits at 100% of its request, which makes the pod a first
   eviction candidate under node pressure, though it is under the 2Gi limit.
2. **Liveness probe failure.** `failureThreshold: 3` × `periodSeconds: 15` = 45s,
   which matches the observed outage length closely enough to be suspicious.

Pod events had aged out before this was investigated, so the two could not be
distinguished. Distinguishing them needs `reason: OOMKilled` on a fresh
termination, or a `Liveness probe failed` event.

## Suggested next step

Do not change probes on a guess. The defensible change on current evidence is
raising `requests.memory` to ~1.5Gi so the pod is not permanently at its request,
then watching whether restarts continue. If they do, capture the termination reason
and events from a fresh restart before touching probe timings.

A second replica would remove the outage regardless of cause, since the Service
would always have a ready endpoint — worth considering for a dependency this many
services share.

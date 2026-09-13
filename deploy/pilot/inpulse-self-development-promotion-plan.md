# Inpulse Self-Development Promotion Plan

## Decision

Adopt the **shape** of the `inpulse-sandbox` self-development loop in the
main Inpulse lane, but do not give the main lane the sandbox's autonomous
authority. Initially, Kelos may discover, implement, review, and revise
low-risk factory improvements; a human remains the only actor able to approve
factory-control changes or merge the resulting PRs.

This is a staged promotion plan. Completing one phase is evidence for the next
phase, not permission to skip a control.

## Scope and non-goals

The first main-repo mission is limited to changes with a machine-checkable
oracle:

- ratchet and allowlist reductions;
- removal of proved-unnecessary test exclusions;
- regression tests for already-understood defects;
- correction of proven stale engineering guidance; and
- CI or test reliability fixes with a deterministic reproduction.

It explicitly excludes billing rules, clinical behavior, product features,
schema migrations, data changes, deployments, dependencies, RBAC, credentials,
and workflow changes. Those require a separately approved mission and remain
human-owned.

## Required safety boundary

Before enabling a main-lane self-development spawner, make these restrictions
properties enforced outside the agent prompt:

1. Protect a `factory-control` path set: Kelos deployment manifests, agent
   prompts, task spawners, workflow files, budgets, RBAC, credentials, and
   branch/ruleset configuration.
2. Reject a PR from a `kelos/*` branch if it changes a protected path. Require
   an approved human-owned PR and a dedicated label for such a change.
3. Use separate credentials. The worker credential may create ordinary PRs,
   but must not change workflows, protected branch settings, factory controls,
   or merge policy.
4. Keep protected branches protected from the worker identity. A required check
   is not sufficient if the worker identity can bypass it.
5. Cover every cost- or write-producing lane with one circuit breaker. It must
   stop authors, revisers, and CI fixers together when a budget, error-rate, or
   WIP threshold is breached.

The existing main issue spawner's instruction not to touch `.github/` is useful
guidance, but is not an adequate replacement for these controls.

## Workflow to introduce

```
mission -> discover/triage -> draft PR -> independent review
        -> CI diagnosis/revision -> human approval -> merge
```

- **Mission** is a small, versioned data file defining authorized work classes,
  exclusions, evidence requirements, and stop conditions. It is distinct from
  prompts so scope changes are easy to review.
- **Discovery/triage** maintains a bounded queue of evidence-backed candidates;
  it does not create work merely to keep agents busy.
- **Author** verifies the untouched baseline, produces a narrow change, and
  records its evidence in the PR body.
- **Reviewer** is an independently configured, read-only agent. Its verdict is
  a structured PR artifact, not a task exit status.
- **Reviser** may respond only to a named reviewer finding and is bounded by a
  revision-round cap.
- **CI fixer** diagnoses a red required check. It may fix an attributable change
  or label/escalate a flake or ambiguous failure; it may not blindly re-run CI
  or make a red signal disappear.
- **Human approval and merge** remain mandatory throughout this plan.

Every PR must carry a factory receipt linking: source issue, Kelos task ID,
mission class, baseline result, PR/commit SHA, required-check outcome, reviewer
verdict, revision task(s), final human decision, and final disposition
(`merged`, `parked`, `blocked`, `rejected`, or `superseded`).

## Phases

### Phase 0 — establish controls and observability

Implement the protected-path guard, least-privilege credentials, all-lane
circuit breaker, and factory receipt format. Add a dashboard or queryable view
that joins task, issue, PR, checks, review, and disposition.

**Exit criteria:** controlled tests prove that a Kelos branch cannot change its
mission/prompt, a workflow, protected configuration, or merge policy; and a
sample PR can be traced end to end without reading pod logs.

### Phase 1 — shadow discovery

Run the mission and triage lanes against the main repo, but permit only issue
comments or a small capped set of labeled candidate issues. No code PRs are
created automatically. A human evaluates whether candidates are well-scoped,
non-duplicative, and backed by real evidence.

**Exit criteria:** at least 20 candidate assessments with no unauthorized
scope, no duplicate active issue, and at least 80% accepted by a human as
actionable without material rewrite.

### Phase 2 — supervised implementation

Allow authors to open draft PRs only for the initial mission classes. Enable the
read-only reviewer and revision/CI-diagnosis lanes. Keep a low WIP cap: one
author, at most two reviewers, and no new author work when the human review
queue has five open factory PRs.

**Exit criteria:** at least 10 merged PRs, no material post-merge regression
attributable to the loop, at least 90% of review findings linked to a revision
or explicit human disposition, and no circuit-breaker escape.

### Phase 3 — supervised continuous operation

Run nightly within a fixed budget and WIP cap. The human owns daily mission
priority and merges. Review the factory receipt dashboard daily; treat rising
PR age, repeat failures, and growing parked work as reasons to reduce dispatch,
not increase concurrency.

**Exit criteria:** four weeks of stable operation, sustained human acceptance,
bounded PR age, and demonstrated rollback drills for an agent configuration
change and a stuck lane.

### Phase 4 — selective autonomy proposal

Only after the preceding evidence, consider auto-merge for one explicitly
defined, reversible class of change. The proposal must name the exact class,
required checks, independent reviewer, rollback procedure, maximum daily
change count, and an automatic kill switch. It must not include billing or
factory-control changes.

No automatic promotion occurs at the end of Phase 3; this phase requires a new
owner decision.

## Operating rules

- Success means an accepted, correctly merged result, not a Kubernetes Task in
  `Succeeded` state.
- A red baseline, ambiguous CI failure, missing evidence, or scope conflict is
  a successful stop-and-report outcome, not an invitation to improvise.
- The dispatcher is controlled by review capacity. Open PRs and their age are
  first-class limits.
- Parked work stays visible with its owner decision and unpark condition.
- Any agent change to the mission, prompts, budgets, controls, or permissions
  is treated as a security-sensitive production change and requires human
  review.

## Initial implementation checklist

1. Define and enforce the protected `factory-control` path set in the Inpulse
   repository.
2. Create a least-privilege worker identity and independently verify that it
   cannot modify protected paths or bypass branch protection.
3. Add a versioned main-repo mission containing only the initial low-risk
   classes above.
4. Deploy discovery, author, reviewer, reviser, and CI-diagnosis lanes in
   shadow/supervised mode with explicit WIP, budget, and stop thresholds.
5. Add the factory receipt to issue comments and PRs, plus a queryable summary.
6. Run the Phase 0 teeth tests, then begin Phase 1 only after a human records
   the result.

## Rollback

The first response to a control failure is to suspend every write-capable
spawner via the shared circuit breaker, preserve task/PR evidence, and leave
existing PRs unmerged. Revert the last human-approved configuration change by
PR; do not repair a safety failure through an unreviewed imperative cluster
edit. Resume only after the failure has an owner, a written root cause, a
verified guard, and a human approval.

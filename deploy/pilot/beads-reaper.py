#!/usr/bin/env python3
"""Release beads whose Kelos Task is gone or failed.

WHY THIS EXISTS. The claim-bead initContainer marks a bead in_progress the moment
its Task starts, which is what keeps it out of `bd ready` and stops it being
respawned. That is the right default, but it is one-way: if the Task then fails,
or is deleted before it finishes, the bead stays in_progress forever. Nothing
retries it and nothing says why — the mirror image of the triage-dispatch defect
where a bead was retired on dispatch rather than on success.

WHAT IT DOES NOT DO. It never closes anything, and it never releases a bead whose
Task SUCCEEDED. A finished Task means an agent did the work; whether that work
resolved the bead is a human judgement (or the close-on-merge workflow's). Those
are reported and left alone.

RETRY IS BOUNDED. Releasing a bead makes it ready again, so a bead that fails
every time would loop forever and burn an agent run each cycle. Each release adds
a kelos-attempt-<n> label; once MAX_ATTEMPTS is reached the bead is labelled
kelos-blocked and left in_progress, so it stays out of `bd ready` and a human has
to look at it.
"""

import json
import os
import re
import subprocess
import sys

SPAWNER = os.environ["SPAWNER"]
TASK_NAMESPACE = os.environ["TASK_NAMESPACE"]
ACTOR = os.environ["BEADS_ACTOR"]
MAX_ATTEMPTS = int(os.environ.get("MAX_ATTEMPTS", "3"))
DRY_RUN = os.environ.get("DRY_RUN", "0") == "1"

# Same shape the spawner and the close workflow validate against. A malformed id
# must never reach `bd update`, which with no id updates "the last touched issue".
ID_RE = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)+$")
ATTEMPT_RE = re.compile(r"^kelos-attempt-(\d+)$")

BLOCKED_LABEL = "kelos-blocked"


def bd(*args, check=True):
    return subprocess.run(["bd", *args], capture_output=True, text=True, check=check)


def claimed_beads():
    out = bd("list", "--status", "in_progress", "--assignee", ACTOR,
             "--json", "--limit", "0").stdout.strip()
    return json.loads(out) if out else []


def task_phases():
    out = subprocess.run(
        ["kubectl", "get", "tasks", "-n", TASK_NAMESPACE,
         "-l", f"kelos.dev/taskspawner={SPAWNER}", "-o", "json"],
        capture_output=True, text=True, check=True).stdout
    items = json.loads(out).get("items", [])
    return {t["metadata"]["name"]: (t.get("status") or {}).get("phase", "") for t in items}


def task_records():
    out = subprocess.run(
        ["kubectl", "get", "taskrecords", "-n", TASK_NAMESPACE, "-o", "json"],
        capture_output=True, text=True, check=True).stdout
    return latest_record_phase(json.loads(out))


def latest_record_phase(records):
    """Map task name -> phase of its most recently COMPLETED TaskRecord.

    Several TaskRecords can share one taskRef.name, one per run, so "the"
    record's phase is not well defined. Measured in production:
    beads-ready-beadseed-31w has two, completing 00:21:05Z and 00:27:50Z. The
    latest completion is the current state.
    """
    latest = {}
    for record in (records or {}).get("items") or []:
        spec = record.get("spec") or {}
        name = (spec.get("taskRef") or {}).get("name")
        if not name:
            continue
        # RFC3339 in a fixed zone sorts lexicographically. A record missing
        # completionTime (the CRD requires it, but a malformed one must not take
        # the sweep down) sorts below any dated record instead of raising.
        completion = spec.get("completionTime") or ""
        if name not in latest or completion >= latest[name][0]:
            latest[name] = (completion, spec.get("phase"))
    return {name: phase for name, (_, phase) in latest.items()}


def classify_bead(task_phase, record_phase, task_name="its Task"):
    """Return (action, reason) for one claimed bead. action is reap or leave.

    A bead is reapable iff its claiming Task will never make further progress.
    task_phase is None only when no live Task object exists.
    """
    if task_phase is None:
        # An absent Task does NOT mean the work never ran. A terminal Task is
        # deleted by ttlSecondsAfterFinished, so a SUCCESS that has since been
        # garbage-collected looks identical to a Task that never existed -- and
        # releasing in that case re-runs work that already succeeded. TaskRecords
        # retain 30 days and are what distinguishes the two.
        #
        # This spawner sets no ttlSecondsAfterFinished today, so the case cannot
        # arise here yet. That is an accident of configuration that anyone can
        # remove, and it is the same inversion (a permanent claim guarded by an
        # expiring record) that the issues path paid $1,527 for. Guard it here
        # rather than relying on a sibling manifest's comment.
        if record_phase == "Succeeded":
            return "leave", f"{task_name} succeeded and was then garbage-collected"
        if record_phase == "Failed":
            return "reap", f"{task_name} failed and was then garbage-collected"
        return "reap", f"{task_name} no longer exists"

    if task_phase == "Failed":
        return "reap", f"{task_name} failed"

    # Running/Pending/Waiting: still working. Succeeded: an agent finished, so
    # whether that resolved the bead is a human judgement, not ours. An
    # unrecognised phase fails safe to leave -- never read "unknown" as "dead".
    # A live Task is authoritative; a stale record must not override it.
    return "leave", task_phase or "<no phase>"


def attempts_so_far(labels):
    seen = [int(m.group(1)) for m in (ATTEMPT_RE.match(l) for l in labels) if m]
    return max(seen) if seen else 0


def main():
    beads = claimed_beads()
    phases = task_phases()
    records = task_records()
    # Report the DENOMINATORS, not just the actions taken. Every run of this job
    # so far has printed 0 claimed beads and taken no action, and a real zero is
    # indistinguishable from a broken query -- claimed_beads() turns empty stdout
    # into [] silently. Printing what each source returned is what makes a future
    # zero interpretable instead of merely reassuring.
    print(f"claimed by {ACTOR}: {len(beads)} | tasks for {SPAWNER}: {len(phases)}"
          f" | taskrecords: {len(records)}")

    released, blocked, left = [], [], []
    changed = False

    for bead in beads:
        bid = bead.get("id", "")
        if not ID_RE.match(bid):
            print(f"  SKIP {bid!r}: not a well-formed bead id", file=sys.stderr)
            continue

        task_name = f"{SPAWNER}-{bid}"
        action, reason = classify_bead(
            phases.get(task_name), records.get(task_name), f"its Task {task_name}")

        if action == "leave":
            left.append((bid, reason))
            continue

        labels = bead.get("labels") or []
        n = attempts_so_far(labels) + 1

        if n > MAX_ATTEMPTS:
            if BLOCKED_LABEL in labels:
                left.append((bid, f"already {BLOCKED_LABEL}"))
                continue
            print(f"  BLOCK {bid}: {reason}; {n - 1} attempts already, at the ceiling")
            if not DRY_RUN:
                bd("update", bid, "--add-label", BLOCKED_LABEL, "--append-notes",
                   f"kelos reaper: {reason}. {n - 1} attempts is the ceiling "
                   f"(MAX_ATTEMPTS={MAX_ATTEMPTS}); left in_progress and labelled "
                   f"{BLOCKED_LABEL} so it stays out of bd ready pending a human.")
                changed = True
            blocked.append(bid)
            continue

        print(f"  RELEASE {bid}: {reason} (attempt {n}/{MAX_ATTEMPTS})")
        if not DRY_RUN:
            bd("update", bid, "--status", "open",
               "--add-label", f"kelos-attempt-{n}",
               "--append-notes", f"kelos reaper: released because {reason}. "
                                 f"Attempt {n} of {MAX_ATTEMPTS}.")
            changed = True
        released.append(bid)

    if changed and not DRY_RUN:
        print("pushing to the hub")
        bd("dolt", "push")
    elif DRY_RUN:
        print("DRY_RUN=1 — nothing was written or pushed")

    print(f"summary: released={len(released)} blocked={len(blocked)} left={len(left)}")
    for bid, why in left:
        print(f"  left {bid}: {why}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

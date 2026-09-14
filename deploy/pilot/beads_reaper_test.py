#!/usr/bin/env python3
"""Tests for beads-reaper.py.

WHY THESE EXIST AT ALL. The reaper has run hourly in production since
2026-09-10 and its write paths have NEVER executed against a non-empty input:
every run reports `claimed by kelos:beads-ready: 0` -> `released=0 blocked=0
left=0`, because there has been exactly one beads-ready Task ever and it
Succeeded, which the reaper is contractually required to leave alone. So
"running in production" is true and "validated in production" is false, and
these tests are the only oracle the release and block decisions have ever had.

They are deliberately pure: the decision logic is extracted into
`classify_bead` and `latest_record_phase` so it can be exercised exhaustively
without a cluster, a beads remote, or subprocess mocking.
"""

import os
import unittest
from pathlib import Path

# beads-reaper.py reads its configuration at import time, so these must be set
# before it is imported. Values are irrelevant to the pure functions under test.
os.environ.setdefault("SPAWNER", "beads-ready")
os.environ.setdefault("TASK_NAMESPACE", "kelos-pilot")
os.environ.setdefault("BEADS_ACTOR", "kelos:beads-ready")

import importlib.util

_HERE = Path(__file__).parent
_SPEC = importlib.util.spec_from_file_location("beads_reaper", _HERE / "beads-reaper.py")
reaper = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(reaper)


class TestClassifyBead(unittest.TestCase):
    """`a bead is reapable iff its claiming Task will never make further progress`.

    That is the predicate the reaper is trying to express. Each case below names
    which side of it the input falls on and why.
    """

    def test_failed_live_task_is_reapable(self):
        action, reason = reaper.classify_bead("Failed", None)
        self.assertEqual(action, "reap")
        self.assertIn("failed", reason)

    def test_running_task_is_left_alone(self):
        # Still working. Reaping here would kill live work.
        self.assertEqual(reaper.classify_bead("Running", None)[0], "leave")

    def test_pending_task_is_left_alone(self):
        self.assertEqual(reaper.classify_bead("Pending", None)[0], "leave")

    def test_waiting_task_is_left_alone(self):
        self.assertEqual(reaper.classify_bead("Waiting", None)[0], "leave")

    def test_unknown_phase_is_left_alone(self):
        # Fail safe: an unrecognised phase must not be read as "dead".
        self.assertEqual(reaper.classify_bead("SomeFuturePhase", None)[0], "leave")

    def test_empty_phase_is_left_alone_and_is_not_confused_with_absent(self):
        # task_phases() maps a Task with no status yet to "", not None. Empty
        # string is falsy, so a `if not task_phase` check here would silently
        # treat a freshly-created Task as a deleted one and reap live work.
        # Only None means "no live Task object".
        action, reason = reaper.classify_bead("", None)
        self.assertEqual(action, "leave")
        self.assertEqual(reason, "<no phase>")

    def test_succeeded_live_task_is_left_alone(self):
        # An agent finished; whether the work resolved the bead is a human call.
        self.assertEqual(reaper.classify_bead("Succeeded", None)[0], "leave")

    # ---- the fix -------------------------------------------------------

    def test_absent_task_with_no_record_is_reapable(self):
        # Genuinely never ran (or its record aged out) -> release it.
        action, reason = reaper.classify_bead(None, None)
        self.assertEqual(action, "reap")
        self.assertIn("no longer exists", reason)

    def test_absent_task_with_succeeded_record_is_LEFT_ALONE(self):
        """The defect this fix closes.

        A Task that SUCCEEDED and was then garbage-collected is indistinguishable
        from one that never existed if you only look at live Task objects — and
        the reaper looked only at live Task objects. It would therefore RELEASE a
        bead whose work had already succeeded, re-running it. Same TTL-vs-
        permanent-state inversion that cost $1,527 on the issues path.
        """
        action, reason = reaper.classify_bead(None, "Succeeded")
        self.assertEqual(action, "leave")
        self.assertIn("succeeded", reason.lower())

    def test_absent_task_with_failed_record_is_reapable(self):
        # Failed then GC'd is still a failure: retry it, subject to the ceiling.
        action, reason = reaper.classify_bead(None, "Failed")
        self.assertEqual(action, "reap")
        self.assertIn("failed", reason)

    def test_live_phase_wins_over_record(self):
        # A live Task is authoritative; a stale record must not override it. A
        # Running task with an old Succeeded record is still running.
        self.assertEqual(reaper.classify_bead("Running", "Succeeded")[0], "leave")
        self.assertEqual(reaper.classify_bead("Failed", "Succeeded")[0], "reap")


class TestLatestRecordPhase(unittest.TestCase):
    """Several TaskRecords can share one taskRef.name — measured in production:
    `beads-ready-beadseed-31w` has two, completing 00:21:05Z and 00:27:50Z. So
    the phase of "the" record is not well defined; the LATEST one governs.
    """

    def _records(self, *triples):
        return {
            "items": [
                {
                    "spec": {
                        "taskRef": {"name": name},
                        "phase": phase,
                        "completionTime": completion,
                    }
                }
                for name, phase, completion in triples
            ]
        }

    def test_single_record(self):
        got = reaper.latest_record_phase(self._records(("t1", "Succeeded", "2026-09-10T00:27:50Z")))
        self.assertEqual(got, {"t1": "Succeeded"})

    def test_latest_completion_wins_regardless_of_order(self):
        # Failed first, then Succeeded: the retry worked, so leave it alone.
        got = reaper.latest_record_phase(
            self._records(
                ("t1", "Succeeded", "2026-09-10T00:27:50Z"),
                ("t1", "Failed", "2026-09-10T00:21:05Z"),
            )
        )
        self.assertEqual(got, {"t1": "Succeeded"})

    def test_latest_completion_wins_when_newest_failed(self):
        # Succeeded first, then Failed on a later run: the current state is failed.
        got = reaper.latest_record_phase(
            self._records(
                ("t1", "Succeeded", "2026-09-10T00:21:05Z"),
                ("t1", "Failed", "2026-09-10T00:27:50Z"),
            )
        )
        self.assertEqual(got, {"t1": "Failed"})

    def test_production_shape_two_succeeded_records_same_name(self):
        got = reaper.latest_record_phase(
            self._records(
                ("beads-ready-beadseed-31w", "Succeeded", "2026-09-10T00:27:50Z"),
                ("beads-ready-beadseed-31w", "Succeeded", "2026-09-10T00:21:05Z"),
            )
        )
        self.assertEqual(got, {"beads-ready-beadseed-31w": "Succeeded"})

    def test_distinct_names_are_kept_separate(self):
        got = reaper.latest_record_phase(
            self._records(
                ("t1", "Succeeded", "2026-09-10T00:27:50Z"),
                ("t2", "Failed", "2026-09-10T00:21:05Z"),
            )
        )
        self.assertEqual(got, {"t1": "Succeeded", "t2": "Failed"})

    def test_empty(self):
        self.assertEqual(reaper.latest_record_phase({"items": []}), {})
        self.assertEqual(reaper.latest_record_phase({}), {})

    def test_missing_completion_time_does_not_crash_and_loses_to_a_dated_one(self):
        # completionTime is required by the CRD, but a malformed record must not
        # take the whole sweep down with a TypeError.
        got = reaper.latest_record_phase(
            self._records(
                ("t1", "Failed", None),
                ("t1", "Succeeded", "2026-09-10T00:27:50Z"),
            )
        )
        self.assertEqual(got, {"t1": "Succeeded"})


class TestAttemptsSoFar(unittest.TestCase):
    def test_none(self):
        self.assertEqual(reaper.attempts_so_far(["kelos-auto", "P1"]), 0)

    def test_single(self):
        self.assertEqual(reaper.attempts_so_far(["kelos-attempt-1"]), 1)

    def test_highest_wins_regardless_of_order(self):
        self.assertEqual(reaper.attempts_so_far(["kelos-attempt-3", "kelos-attempt-1"]), 3)

    def test_double_digits_are_not_string_compared(self):
        self.assertEqual(reaper.attempts_so_far(["kelos-attempt-9", "kelos-attempt-10"]), 10)

    def test_suffix_must_be_entirely_numeric(self):
        self.assertEqual(reaper.attempts_so_far(["kelos-attempt-1-retry", "kelos-attempt-2x"]), 0)


class TestEmbeddedCopyIsInSync(unittest.TestCase):
    """beads-reaper.py exists TWICE: as this file's sibling, and embedded as a
    string in the ConfigMap inside beads-reaper.yaml. Nothing in the repo keeps
    them in sync — no generator, no gate. So an edit to the readable .py leaves
    the DEPLOYED script untouched, and the fix reads as done while production
    runs the old code. That is the failure mode this whole PR is about, one level
    up, so it gets a gate rather than a convention.
    """

    def test_configmap_copy_is_byte_identical_to_the_standalone_file(self):
        try:
            import yaml
        except ImportError:  # pragma: no cover
            self.skipTest("PyYAML not available")

        standalone = (_HERE / "beads-reaper.py").read_text()

        embedded = None
        with open(_HERE / "beads-reaper.yaml") as fh:
            for doc in yaml.safe_load_all(fh):
                if doc and doc.get("kind") == "ConfigMap":
                    embedded = doc.get("data", {}).get("beads-reaper.py")

        self.assertIsNotNone(
            embedded,
            "no ConfigMap in beads-reaper.yaml carries a beads-reaper.py key — "
            "if the delivery mechanism changed, this gate needs updating, not deleting",
        )
        self.assertEqual(
            embedded,
            standalone,
            "deploy/pilot/beads-reaper.py and the copy embedded in "
            "beads-reaper.yaml have diverged. The ConfigMap copy is what actually "
            "runs, so the divergence means the deployed reaper is NOT the script "
            "in this directory. Update both.",
        )


class TestRbacCoversTaskRecords(unittest.TestCase):
    """The fix reads TaskRecords. A kubectl call the ServiceAccount is not
    permitted to make fails at runtime, and `bd(..., check=True)`-style
    subprocess checking turns that into a crash rather than a degraded sweep. The
    Role granted only `tasks`, so this is exactly the "gate cannot fire" shape:
    the code would be correct and the deployment would forbid it.
    """

    def test_role_grants_read_on_taskrecords(self):
        try:
            import yaml
        except ImportError:  # pragma: no cover
            self.skipTest("PyYAML not available")

        rules = []
        with open(_HERE / "beads-reaper.yaml") as fh:
            for doc in yaml.safe_load_all(fh):
                if doc and doc.get("kind") == "Role" and doc["metadata"]["name"] == "beads-reaper":
                    rules = doc.get("rules") or []

        self.assertTrue(rules, "no beads-reaper Role found in beads-reaper.yaml")

        covered = any(
            "kelos.dev" in (r.get("apiGroups") or [])
            and "taskrecords" in (r.get("resources") or [])
            and {"get", "list"} <= set(r.get("verbs") or [])
            for r in rules
        )
        self.assertTrue(
            covered,
            "the beads-reaper Role must grant get+list on taskrecords.kelos.dev — "
            "the reaper consults TaskRecords to tell a garbage-collected SUCCESS "
            f"from a Task that never ran. Rules found: {rules}",
        )


if __name__ == "__main__":
    unittest.main()

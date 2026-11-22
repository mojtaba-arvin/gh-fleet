from __future__ import annotations

import json
import os
import stat
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))
from gh_fleet_telemetry import AgentTelemetry, TelemetryError


class AgentTelemetryTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name) / "private"
        self.spool = self.directory / "agent-events.jsonl"

    def emitter(self, **kwargs: object) -> AgentTelemetry:
        options = {
            "agent_id": "runner-01",
            "task_id": "task-01",
            "repository": "owner/service",
            "source": "python-runner/1.0",
            "spool_path": self.spool,
            "branch": "agent/change",
            "commit_sha": "abcdef0123456",
            "pull_request": 123,
            "workflow_run_id": 456,
            "heartbeat_interval_seconds": 5,
        }
        options.update(kwargs)
        return AgentTelemetry(**options)  # type: ignore[arg-type]

    def events(self) -> list[dict[str, object]]:
        return [json.loads(line) for line in self.spool.read_text(encoding="utf-8").splitlines()]

    def test_context_manager_emits_start_progress_and_completion(self) -> None:
        telemetry = self.emitter()
        with telemetry.task(heartbeat=False):
            telemetry.progress("Loading approved work item")
        events = self.events()
        self.assertEqual([e["event_type"] for e in events], ["agent.started", "agent.progress", "agent.completed"])
        self.assertEqual([e["sequence"] for e in events], [1, 2, 3])
        self.assertEqual(events[-1]["state"], "completed")
        self.assertEqual(events[0]["workflow_run_id"], 456)
        self.assertEqual(events[0]["pull_request"], 123)

    def test_waiting_heartbeat_and_resume_keep_contract_shape(self) -> None:
        telemetry = self.emitter()
        telemetry.start()
        telemetry.waiting("ci", "Waiting for checks")
        telemetry.heartbeat()
        telemetry.resumed("Checks passed")
        telemetry.completed()
        events = self.events()
        self.assertEqual([e["event_type"] for e in events], [
            "agent.started", "agent.waiting", "agent.heartbeat", "agent.resumed", "agent.completed"
        ])
        self.assertNotIn("state", events[2])
        self.assertEqual(events[1]["reason"], "ci")
        self.assertEqual([e["sequence"] for e in events], [1, 2, 3, 4, 5])

    def test_exception_emits_failed_without_exception_message(self) -> None:
        telemetry = self.emitter()
        with self.assertRaisesRegex(RuntimeError, "private token value"):
            with telemetry.task(heartbeat=False):
                raise RuntimeError("private token value")
        events = self.events()
        self.assertEqual(events[-1]["event_type"], "agent.failed")
        self.assertEqual(events[-1]["summary"], "Raised RuntimeError")
        self.assertNotIn("private token value", self.spool.read_text(encoding="utf-8"))

    def test_terminal_task_rejects_late_heartbeat(self) -> None:
        telemetry = self.emitter()
        telemetry.start()
        telemetry.completed()
        with self.assertRaises(TelemetryError):
            telemetry.heartbeat()
        self.assertEqual(len(self.events()), 2)

    def test_invalid_reason_and_unsafe_summary_are_rejected(self) -> None:
        telemetry = self.emitter()
        telemetry.start()
        with self.assertRaises(ValueError):
            telemetry.waiting("unknown")
        with self.assertRaises(ValueError):
            telemetry.progress("contains\nnewline")
        self.assertEqual(len(self.events()), 1)

    def test_metadata_is_validated(self) -> None:
        with self.assertRaises(ValueError):
            self.emitter(repository="owner")
        with self.assertRaises(ValueError):
            self.emitter(heartbeat_interval_seconds=4)
        with self.assertRaises(ValueError):
            self.emitter(branch="../secret")

    @unittest.skipUnless(os.name == "posix", "POSIX permission bits are not portable")
    def test_spool_and_parent_are_private(self) -> None:
        telemetry = self.emitter()
        telemetry.start()
        self.assertEqual(stat.S_IMODE(self.directory.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE(self.spool.stat().st_mode), 0o600)

    def test_symlink_spool_is_rejected(self) -> None:
        self.directory.mkdir(mode=0o700)
        target = self.directory / "target.jsonl"
        target.write_text("", encoding="utf-8")
        link = self.directory / "link.jsonl"
        link.symlink_to(target)
        with self.assertRaises(TelemetryError):
            self.emitter(spool_path=link)

    def test_spool_size_limit_fails_closed(self) -> None:
        telemetry = self.emitter()
        self.spool.write_bytes(b"x" * (4 << 20))
        with self.assertRaises(TelemetryError):
            telemetry.start()

    def test_multiple_tasks_append_to_one_spool(self) -> None:
        first = self.emitter(task_id="task-a")
        second = self.emitter(task_id="task-b", agent_id="runner-02")
        first.start()
        second.start()
        first.completed()
        second.failed("Runner exited")
        events = self.events()
        self.assertEqual(len(events), 4)
        self.assertEqual({event["task_id"] for event in events}, {"task-a", "task-b"})


if __name__ == "__main__":
    unittest.main()

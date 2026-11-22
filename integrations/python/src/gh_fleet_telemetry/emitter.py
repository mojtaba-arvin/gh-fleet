"""Dependency-free writer for gh-fleet's local agent telemetry spool."""
from __future__ import annotations

import json
import os
import re
import stat
import threading
import uuid
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterator

SCHEMA_VERSION = "1"
MAX_SPOOL_BYTES = 4 << 20
MAX_EVENT_LINE_BYTES = 16 << 10
MAX_SUMMARY_BYTES = 240
DEFAULT_HEARTBEAT_SECONDS = 30
_ALLOWED_REASONS = {"ci", "review", "approval", "user", "dependency", "rate_limit", "external_tool"}
_ACTIVE = {"starting", "working", "waiting", "blocked"}
_TERMINAL = {"completed", "failed", "cancelled"}
_TOKEN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
_REPO = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$")
_BRANCH = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$")
_SHA = re.compile(r"^[A-Fa-f0-9]{7,64}$")
_RUN_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$")


class TelemetryError(Exception):
    """A telemetry event could not be safely appended."""


class AgentTelemetry:
    """Emit ordered events for one agent/task attempt."""

    def __init__(
        self,
        *,
        agent_id: str,
        task_id: str,
        repository: str,
        source: str,
        spool_path: str | os.PathLike[str] | None = None,
        branch: str | None = None,
        commit_sha: str | None = None,
        pull_request: int | None = None,
        workflow_run_id: int | str | None = None,
        heartbeat_interval_seconds: int = DEFAULT_HEARTBEAT_SECONDS,
    ) -> None:
        self.agent_id = self._token("agent_id", agent_id)
        self.task_id = self._token("task_id", task_id)
        if not isinstance(repository, str) or not _REPO.fullmatch(repository):
            raise ValueError("repository must use owner/name format")
        if not isinstance(source, str) or not source or len(source) > 128 or not _TOKEN.fullmatch(source.replace("/", ":")):
            raise ValueError("source is missing or invalid")
        if branch is not None and (not _BRANCH.fullmatch(branch) or ".." in branch or "//" in branch):
            raise ValueError("branch contains unsupported characters")
        if commit_sha is not None and not _SHA.fullmatch(commit_sha):
            raise ValueError("commit_sha must be 7–64 hexadecimal characters")
        if pull_request is not None and (isinstance(pull_request, bool) or not isinstance(pull_request, int) or not 1 <= pull_request <= 100_000_000):
            raise ValueError("pull_request must be a positive integer")
        if workflow_run_id is not None:
            if isinstance(workflow_run_id, bool) or not isinstance(workflow_run_id, (int, str)):
                raise ValueError("workflow_run_id must be a positive integer or safe string")
            if isinstance(workflow_run_id, int) and workflow_run_id <= 0:
                raise ValueError("workflow_run_id must be positive")
            if isinstance(workflow_run_id, str) and not _RUN_ID.fullmatch(workflow_run_id):
                raise ValueError("workflow_run_id contains unsupported characters")
        if isinstance(heartbeat_interval_seconds, bool) or not isinstance(heartbeat_interval_seconds, int) or not 5 <= heartbeat_interval_seconds <= 3600:
            raise ValueError("heartbeat_interval_seconds must be between 5 and 3600")

        self.repository = repository
        self.source = source
        self.branch = branch
        self.commit_sha = commit_sha
        self.pull_request = pull_request
        self.workflow_run_id = workflow_run_id
        self.heartbeat_interval_seconds = heartbeat_interval_seconds
        configured = spool_path or os.environ.get("GH_FLEET_AGENT_EVENTS")
        self.spool_path = Path(configured).expanduser() if configured else Path.home() / ".cache" / "gh-fleet" / "agent-events.jsonl"
        self._sequence = 0
        self._state: str | None = None
        self._lock = threading.RLock()
        self._ensure_spool()

    @staticmethod
    def _token(field: str, value: str) -> str:
        if not isinstance(value, str) or not _TOKEN.fullmatch(value):
            raise ValueError(f"{field} is missing or invalid")
        return value

    def _ensure_spool(self) -> None:
        parent = self.spool_path.parent
        try:
            parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            parent_info = parent.lstat()
        except OSError as exc:
            raise TelemetryError(f"cannot prepare telemetry directory: {exc}") from exc
        if not stat.S_ISDIR(parent_info.st_mode):
            raise TelemetryError("telemetry parent must be a real directory, not a symlink")
        if os.name == "posix" and parent_info.st_mode & 0o077:
            raise TelemetryError("telemetry directory must have mode 0700 or stricter")
        try:
            existing = self.spool_path.lstat()
        except FileNotFoundError:
            existing = None
        if existing is not None and stat.S_ISLNK(existing.st_mode):
            raise TelemetryError("telemetry spool must not be a symlink")

        flags = os.O_WRONLY | os.O_APPEND | os.O_CREAT | getattr(os, "O_NOFOLLOW", 0)
        try:
            fd = os.open(self.spool_path, flags, 0o600)
        except OSError as exc:
            raise TelemetryError(f"cannot securely open telemetry spool: {exc}") from exc
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode):
                raise TelemetryError("telemetry spool must be a regular file")
            if os.name == "posix" and info.st_mode & 0o077:
                raise TelemetryError("telemetry spool must have mode 0600 or stricter")
            if info.st_nlink != 1:
                raise TelemetryError("telemetry spool must not have multiple hard links")
            if info.st_size > MAX_SPOOL_BYTES:
                raise TelemetryError("telemetry spool exceeds 4 MiB; rotate it and re-emit active task snapshots")
        finally:
            os.close(fd)

    @staticmethod
    def _safe_summary(summary: str | None) -> str | None:
        if summary is None:
            return None
        if not isinstance(summary, str):
            raise ValueError("summary must be a string")
        if any(ord(char) < 32 or ord(char) == 127 for char in summary):
            raise ValueError("summary must not contain control characters")
        if len(summary.encode("utf-8")) > MAX_SUMMARY_BYTES:
            raise ValueError(f"summary must be at most {MAX_SUMMARY_BYTES} UTF-8 bytes")
        return summary

    def _append(self, event: dict[str, Any]) -> None:
        line = (json.dumps(event, ensure_ascii=False, separators=(",", ":")) + "\n").encode("utf-8")
        if len(line) > MAX_EVENT_LINE_BYTES:
            raise TelemetryError("encoded event exceeds the 16 KiB line limit")
        self._ensure_spool()
        try:
            existing = self.spool_path.lstat()
        except FileNotFoundError as exc:
            raise TelemetryError("telemetry spool disappeared before append") from exc
        if stat.S_ISLNK(existing.st_mode):
            raise TelemetryError("telemetry spool must not be a symlink")
        flags = os.O_WRONLY | os.O_APPEND | getattr(os, "O_NOFOLLOW", 0)
        try:
            fd = os.open(self.spool_path, flags)
        except OSError as exc:
            raise TelemetryError(f"cannot open telemetry spool for append: {exc}") from exc
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode):
                raise TelemetryError("telemetry spool must be a regular file")
            if os.name == "posix" and info.st_mode & 0o077:
                raise TelemetryError("telemetry spool permissions changed; expected 0600 or stricter")
            if os.name == "posix":
                import fcntl
                fcntl.flock(fd, fcntl.LOCK_EX)
            try:
                if os.fstat(fd).st_size + len(line) > MAX_SPOOL_BYTES:
                    raise TelemetryError("telemetry spool would exceed 4 MiB; rotate it and re-emit active task snapshots")
                if os.write(fd, line) != len(line):
                    raise TelemetryError("short write while appending telemetry event")
            finally:
                if os.name == "posix":
                    fcntl.flock(fd, fcntl.LOCK_UN)
        except OSError as exc:
            raise TelemetryError(f"failed to append telemetry event: {exc}") from exc
        finally:
            os.close(fd)

    def _emit(self, kind: str, state: str | None, *, reason: str | None = None, summary: str | None = None) -> None:
        summary = self._safe_summary(summary)
        with self._lock:
            if kind == "agent.started":
                if self._state is not None:
                    raise TelemetryError("agent.started may be emitted only once per task object")
            elif self._state is None:
                raise TelemetryError("task must begin with agent.started")
            elif self._state in _TERMINAL:
                raise TelemetryError("terminal task cannot be changed; use a new task_id for retries")

            if kind in {"agent.waiting", "agent.blocked"}:
                if reason not in _ALLOWED_REASONS:
                    raise ValueError("waiting/blocked reason is not an allowed reason code")
            elif reason is not None:
                raise ValueError("reason is valid only for waiting or blocked events")

            if kind == "agent.heartbeat":
                if self._state not in _ACTIVE:
                    raise TelemetryError("heartbeat requires an active task")
            else:
                allowed = {
                    "agent.started": "starting",
                    "agent.progress": "working",
                    "agent.resumed": "working",
                    "agent.waiting": "waiting",
                    "agent.blocked": "blocked",
                    "agent.completed": "completed",
                    "agent.failed": "failed",
                    "agent.cancelled": "cancelled",
                }
                if allowed.get(kind) != state:
                    raise ValueError(f"invalid state for {kind}")
                if kind != "agent.started" and self._state not in _ACTIVE:
                    raise TelemetryError("invalid lifecycle transition")

            sequence = self._sequence + 1
            event: dict[str, Any] = {
                "schema_version": SCHEMA_VERSION,
                "event_id": uuid.uuid4().hex,
                "sequence": sequence,
                "event_type": kind,
                "occurred_at": datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z"),
                "agent_id": self.agent_id,
                "task_id": self.task_id,
                "repository": self.repository,
                "source": self.source,
                "heartbeat_interval_seconds": self.heartbeat_interval_seconds,
            }
            optional = {
                "branch": self.branch,
                "commit_sha": self.commit_sha,
                "pull_request": self.pull_request,
                "workflow_run_id": self.workflow_run_id,
                "state": state,
                "reason": reason,
                "summary": summary,
            }
            event.update({key: value for key, value in optional.items() if value is not None})
            if kind == "agent.heartbeat":
                event.pop("state", None)
            self._append(event)
            self._sequence = sequence
            if state is not None:
                self._state = state

    def start(self, summary: str | None = None) -> None:
        self._emit("agent.started", "starting", summary=summary)

    def progress(self, summary: str | None = None) -> None:
        self._emit("agent.progress", "working", summary=summary)

    def waiting(self, reason: str, summary: str | None = None) -> None:
        self._emit("agent.waiting", "waiting", reason=reason, summary=summary)

    def resumed(self, summary: str | None = None) -> None:
        self._emit("agent.resumed", "working", summary=summary)

    def blocked(self, reason: str, summary: str | None = None) -> None:
        self._emit("agent.blocked", "blocked", reason=reason, summary=summary)

    def heartbeat(self) -> None:
        self._emit("agent.heartbeat", None)

    def completed(self, summary: str | None = None) -> None:
        self._emit("agent.completed", "completed", summary=summary)

    def failed(self, summary: str | None = None) -> None:
        self._emit("agent.failed", "failed", summary=summary)

    def cancelled(self, summary: str | None = None) -> None:
        self._emit("agent.cancelled", "cancelled", summary=summary)

    @contextmanager
    def task(self, *, heartbeat: bool = True) -> Iterator[AgentTelemetry]:
        """Emit start/heartbeats and a terminal event around a Python task."""
        self.start()
        stop = threading.Event()
        heartbeat_errors: list[BaseException] = []

        def send_heartbeats() -> None:
            while not stop.wait(self.heartbeat_interval_seconds):
                try:
                    self.heartbeat()
                except BaseException as exc:
                    heartbeat_errors.append(exc)
                    stop.set()
                    return

        worker = None
        if heartbeat:
            worker = threading.Thread(target=send_heartbeats, name="gh-fleet-heartbeat", daemon=True)
            worker.start()
        try:
            yield self
        except BaseException as exc:
            stop.set()
            if worker is not None:
                worker.join(timeout=self.heartbeat_interval_seconds + 1)
            terminal = self.cancelled if isinstance(exc, (KeyboardInterrupt, SystemExit)) else self.failed
            try:
                terminal(f"Raised {type(exc).__name__}")
            except BaseException as telemetry_exc:
                if hasattr(exc, "add_note"):
                    exc.add_note(f"gh-fleet telemetry terminal event failed: {telemetry_exc}")
            raise
        else:
            stop.set()
            if worker is not None:
                worker.join(timeout=self.heartbeat_interval_seconds + 1)
            if heartbeat_errors:
                raise TelemetryError(f"heartbeat emitter stopped: {heartbeat_errors[0]}") from heartbeat_errors[0]
            self.completed()

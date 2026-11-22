# gh-fleet Python telemetry SDK

A dependency-free producer for the local JSONL contract consumed by gh-fleet. It works with any Python agent runner and does not depend on LangChain, LangGraph, an LLM provider, or an orchestration framework.

## Install from a checkout

~~~bash
python -m pip install ./integrations/python
~~~

## Minimal integration

~~~python
from gh_fleet_telemetry import AgentTelemetry

telemetry = AgentTelemetry(
    agent_id="worker-01",
    task_id="issue-42-attempt-1",  # use a new task ID for each retry
    repository="owner/service",
    branch="agent/fix-timeout",
    source="my-python-runner/1.0",
)

with telemetry.task():  # emits started, periodic heartbeats, then completed/failed
    telemetry.progress("Reading task metadata")
    result = run_agent_work()
    telemetry.progress("Validated the proposed change")
~~~

The SDK uses GH_FLEET_AGENT_EVENTS when set, otherwise ~/.cache/gh-fleet/agent-events.jsonl. Pass spool_path=... to override it. It creates the private directory/file with POSIX modes 0700/0600, rejects symlinks and unsafe permissions, appends one bounded JSON line per event, and fails closed when the 4 MiB spool limit would be exceeded. Windows ACLs remain the operator's responsibility.

## Explicit lifecycle callbacks

For runtimes that expose callbacks rather than a single call boundary:

~~~python
telemetry.start("Task accepted")
telemetry.progress("Retrieving approved context")
telemetry.waiting("approval", "Awaiting operator approval")
# callback when the runtime is active again:
telemetry.resumed("Approval received")
telemetry.progress("Applying the approved change")
telemetry.completed("Work finished")
~~~

Use blocked(reason, summary) for a blocked task. Supported reasons are ci, review, approval, user, dependency, rate_limit, and external_tool. Call heartbeat() from an explicit runtime callback when not using task().

task() emits a heartbeat every configured interval by default. It emits failed when the body raises, recording only the exception class (never the exception message); KeyboardInterrupt/SystemExit emit cancelled. Set heartbeat=False to disable the background heartbeat thread and send heartbeats from your own scheduler instead.

Do not pass prompts, model outputs, chain-of-thought, raw tool arguments/results, shell commands, file contents, environment variables, credentials, or exception messages as summaries. Summaries must be short, single-line operational descriptions (at most 240 UTF-8 bytes).

## Runner integration notes

Create one AgentTelemetry object per (agent_id, task_id). Keep the same object for the full attempt so sequence numbers remain monotonic. Start each retry with a new task_id and a new object. Multiple processes may append to the same local spool; each event is appended as one write, with POSIX advisory locking around the spool size check. Keep the spool local to the machine running gh-fleet.

For LangGraph or another callback-driven runtime, call progress() from meaningful node/step callbacks, waiting() when execution yields to an external dependency, and resumed() when it resumes. Avoid emitting per-token or per-tool-call events.

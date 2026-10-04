#!/usr/bin/env python3
# Modified for k9+; see NOTICE.
"""Behavioral journeys in the actual TUI against a disposable local API.

Run with the Go-built binary and Python packages Pillow/pyte. Screens contain
only emitted terminal cells. This is fixture coverage, never a live-cluster claim.
"""

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import statistics
import tempfile
import threading
import time
import select
import fcntl
import struct
import termios
from copy import deepcopy
from datetime import datetime, timezone
from http.server import ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

spec = importlib.util.spec_from_file_location("capture_demo", Path(__file__).with_name("capture-demo.py"))
demo = importlib.util.module_from_spec(spec)
spec.loader.exec_module(demo)


class JourneyHandler(demo.APIHandler):
    def do_PATCH(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
        self.server.patch_started.set()
        self.server.patch_started_at = time.monotonic()
        self.server.patch_release.wait(5)
        obj = next((o for o in self.server.objects if o["kind"] == "Deployment"), None)
        if obj is None:
            self.send_error(404)
            return
        expected = body.get("metadata", {}).get("uid")
        if expected != obj["metadata"]["uid"]:
            self.send_error(409, "UID precondition missing or changed")
            return
        try:
            self.reply(obj)
        except (BrokenPipeError, ConnectionResetError):
            # The slow-request journey intentionally quits before PATCH returns.
            pass

    def do_GET(self):
        if self.path.startswith("/apis/metrics.k8s.io/v1beta1/"):
            body = json.dumps({"kind": "Status", "apiVersion": "v1", "status": "Failure",
                               "message": "Fixture metrics unavailable", "reason": "ServiceUnavailable", "code": 503}).encode()
            self.send_response(503)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        super().do_GET()


class JourneyAPI(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self):
        super().__init__(("127.0.0.1", 0), JourneyHandler)
        self.objects = demo.fixtures()
        self.stopped = threading.Event()
        self.patch_started = threading.Event()
        self.patch_release = threading.Event()
        self.patch_started_at = None
        self.objects.append({"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {
            "name": "payments-api", "namespace": "apps", "uid": "fixture-deployment", "resourceVersion": "100"},
            "spec": {"replicas": 1, "selector": {"matchLabels": {"app": "payments"}},
                     "template": {"metadata": {"labels": {"app": "payments"}},
                                  "spec": {"containers": [{"name": "api", "image": "example.test/api:v1"}]}}},
            "status": {"replicas": 1, "readyReplicas": 1, "availableReplicas": 1}})
        self.objects.append({"apiVersion": "v1", "kind": "Node", "metadata": {
            "name": "fixture-node", "uid": "fixture-node", "resourceVersion": "100"},
            "status": {"capacity": {"cpu": "4", "memory": "8Gi", "pods": "110"},
                       "allocatable": {"cpu": "4", "memory": "8Gi", "pods": "110"},
                       "conditions": [{"type": "Ready", "status": "True"}]}})
        for i, state in enumerate(["CrashLoopBackOff", "ContainerCreating"]):
            name = "payments-api-with-a-deliberately-long-workload-name-" + str(i)
            self.objects.append({"apiVersion": "v1", "kind": "Pod", "metadata": {
                "name": name, "namespace": "apps", "uid": "fixture-pod-" + str(i),
                "resourceVersion": "100", "creationTimestamp": "2026-10-03T00:00:00Z"},
                "spec": {"nodeName": "fixture-node", "containers": [{"name": "api", "image": "example.test/api:v1"}]},
                "status": {"phase": "Pending" if i else "Running", "containerStatuses": [{
                    "name": "api", "ready": False, "restartCount": 7,
                    "state": {"waiting": {"reason": state, "message": "Fixture fault evidence"}},
                    "lastState": {"terminated": {"reason": "OOMKilled", "exitCode": 137}}}]}})


class InvestigationHandler(JourneyHandler):
    """Journal real requests and serve only UID-matching retained events."""

    def record(self, method):
        parsed = urlparse(self.path)
        with self.server.request_lock:
            self.server.requests.append({"method": method, "path": parsed.path,
                                         "query": parse_qs(parsed.query)})
        return parsed

    def do_GET(self):
        parsed = self.record("GET")
        if parsed.path == self.server.selected_path and self.server.deny_selected:
            body = json.dumps({"kind": "Status", "apiVersion": "v1", "status": "Failure",
                               "message": "Fixture selected resource unavailable after export",
                               "reason": "ServiceUnavailable", "code": 503}).encode()
            self.send_response(503)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if parsed.path == "/api/v1/namespaces/apps/events":
            selector = parse_qs(parsed.query).get("fieldSelector", [""])[0]
            uid = selector.removeprefix("involvedObject.uid=")
            items = [deepcopy(obj) for obj in self.server.objects if obj["kind"] == "Event"
                     and obj["involvedObject"]["uid"] == uid]
            return self.reply({"apiVersion": "v1", "kind": "EventList",
                               "metadata": {"resourceVersion": "100"}, "items": items})
        super().do_GET()

    def do_POST(self):
        self.record("POST")
        super().do_POST()

    def do_PATCH(self):
        self.record("PATCH")
        self.send_error(405, "Investigation fixtures forbid resource mutations")

    def do_PUT(self):
        self.record("PUT")
        self.send_error(405, "Investigation fixtures forbid resource mutations")

    def do_DELETE(self):
        self.record("DELETE")
        self.send_error(405, "Investigation fixtures forbid resource mutations")


class InvestigationAPI(JourneyAPI):
    def __init__(self):
        super().__init__()
        self.RequestHandlerClass = InvestigationHandler
        self.request_lock = threading.Lock()
        self.requests = []
        self.deny_selected = False
        self.selected_path = "/api/v1/namespaces/apps/pods/investigation-api"
        now = datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
        self.objects.append({"apiVersion": "v1", "kind": "Pod", "metadata": {
            "name": "investigation-api", "namespace": "apps", "uid": "fixture-investigation-pod",
            "resourceVersion": "100", "creationTimestamp": "2026-10-03T00:00:00Z",
            "labels": {"fixture-before": "present"},
            "annotations": {"fixture-token": "fixture-credential-must-not-export"}},
            "spec": {"nodeName": "fixture-node", "containers": [
                {"name": "api", "image": "example.test/api:v1", "resources": {
                    "requests": {"cpu": "250m", "memory": "128Mi"},
                    "limits": {"cpu": "1", "memory": "256Mi"}}},
                {"name": "sidecar", "image": "example.test/sidecar:v1"}]},
            "status": {"phase": "Running", "containerStatuses": [
                {"name": "api", "ready": False, "restartCount": 7,
                 "state": {"waiting": {"reason": "CrashLoopBackOff", "message": "Fixture fault evidence"}},
                 "lastState": {"terminated": {"reason": "OOMKilled", "exitCode": 137, "finishedAt": now}}},
                {"name": "sidecar", "ready": True, "restartCount": 0, "state": {"running": {"startedAt": now}}}]}})
        self.objects.append({"apiVersion": "v1", "kind": "Event", "metadata": {
            "name": "fixture-scheduling", "namespace": "apps", "uid": "fixture-event",
            "resourceVersion": "100", "creationTimestamp": now},
            "involvedObject": {"apiVersion": "v1", "kind": "Pod", "name": "investigation-api",
                               "namespace": "apps", "uid": "fixture-investigation-pod"},
            "reason": "FailedScheduling", "type": "Warning", "count": 2,
            "firstTimestamp": now, "lastTimestamp": now,
            "source": {"component": "default-scheduler"},
            "message": "Fixture scheduler observed insufficient memory; retained evidence, not a causal diagnosis."})

    def selected_reads(self):
        with self.request_lock:
            return sum(r["method"] == "GET" and r["path"] == self.selected_path for r in self.requests)

    def change_selected(self):
        for i, obj in enumerate(self.objects):
            if obj["kind"] == "Pod" and obj["metadata"]["name"] == "investigation-api":
                changed = deepcopy(obj)
                changed["metadata"]["resourceVersion"] = "101"
                del changed["metadata"]["labels"]["fixture-before"]
                changed["metadata"]["labels"]["fixture-after"] = "present"
                changed["spec"]["containers"][0]["resources"]["requests"]["memory"] = "192Mi"
                self.objects[i] = changed
                return
        raise AssertionError("Selected fixture is missing")


def text(terminal):
    return "\n".join(terminal.screen.display)


def assert_screen(terminal, required=(), excluded=()):
    screen = text(terminal)
    for phrase in required:
        if phrase not in screen:
            raise AssertionError(f"Missing {phrase!r}:\n{screen}")
    for phrase in excluded:
        if phrase in screen:
            raise AssertionError(f"Unexpected {phrase!r}:\n{screen}")
    if terminal.process.poll() is not None:
        raise AssertionError("Application exited during journey")


def capture(terminal, output, name):
    (output / (name + ".txt")).write_text(text(terminal) + "\n")
    demo.rasterize(terminal.screen, output / (name + ".png"))


def inspect_pages(terminal, output, name, required=(), excluded=(), max_pages=12):
    """Assert content by scrolling emitted cells, never reading app internals."""
    terminal.keys("\x1b[H", .15)
    pages = []
    previous = None
    for page in range(max_pages):
        current = text(terminal)
        if current == previous:
            break
        capture(terminal, output, f"{name}-{page + 1}")
        pages.append(current)
        previous = current
        terminal.keys("\x1b[6~", .15)
    else:
        raise AssertionError(f"{name}: view exceeded the fixture's {max_pages}-page bound")
    seen = "\n".join(pages)
    # tview wraps a word at the viewport edge. Assertions may cross that wrap,
    # while captures remain the unmodified emitted cells (including borders).
    compact = "".join(c for c in seen if not c.isspace() and c != "│")
    def contains(phrase):
        return phrase in seen or "".join(c for c in phrase if not c.isspace()) in compact

    for phrase in required:
        if not contains(phrase):
            raise AssertionError(f"{name}: missing {phrase!r} in emitted pages:\n{seen}")
    for phrase in excluded:
        if contains(phrase):
            raise AssertionError(f"{name}: unexpected {phrase!r} in emitted pages:\n{seen}")
    terminal.keys("\x1b[H", .1)
    return seen


def save_preview(terminal, path):
    terminal.keys("s", .2)
    assert_screen(terminal, ["Export evidence", "New absolute .json/.md path"])
    terminal.keys(str(path) + "\t\t\r", .4)
    deadline = time.monotonic() + 3
    while not path.exists() and time.monotonic() < deadline:
        terminal.drain(.1)
    if not path.exists():
        raise AssertionError("Explicit export did not create the selected path:\n" + text(terminal))
    if path.stat().st_mode & 0o777 != 0o600:
        raise AssertionError("Evidence export did not use mode 0600")


def investigation_journey(binary, output):
    """Selected-object evidence, with no live Kubernetes or desired-state source."""
    demo.COLS, demo.ROWS = 120, 34
    api = InvestigationAPI()
    threading.Thread(target=api.serve_forever, daemon=True).start()
    started = time.monotonic()
    try:
        with tempfile.TemporaryDirectory(prefix="k9plus-investigation-") as directory:
            terminal = demo.Terminal(binary, directory, api.server_port, command="pods apps", flags=["--readonly"])
            try:
                terminal.drain(4)
                terminal.keys("/investigation-api\r", .5)
                assert_screen(terminal, ["investigation-api", "1/3", "[RO]", "demo-dev"])
                capture(terminal, output, "investigation-selected")

                terminal.command("diagnostics resource")
                inspect_pages(terminal, output, "diagnostics-selected", [
                    "Selected API object: available", "pods apps/investigation-api",
                    "Selected object is readable; identity checked", "Context: demo-dev"])
                terminal.keys("\x1b", .2)
                terminal.command("diagnostics metrics")
                inspect_pages(terminal, output, "diagnostics-metrics-unavailable", [
                    "Node metrics: unavailable", "Fixture metrics unavailable", "APIService/controller availability",
                    "limit=1", "eight-second deadline"], ["Node metrics: available"])
                terminal.keys("\x1b", .2)

                terminal.command("compare")
                baseline = inspect_pages(terminal, output, "comparison-baseline", [
                    "RESOURCE COMPARISON · CHANGES FIRST", "A · chosen baseline", "B · comparison observation", "UID fixture-investigation-pod",
                    "Source: Kubernetes API observation", "Observed:", "Press r to capture B",
                    "State: complete", "State: unknown"])
                reads_at_a = api.selected_reads()
                api.change_selected()
                terminal.drain(2.2)
                assert_screen(terminal, ["A · chosen baseline", "State: unknown"])
                if api.selected_reads() != reads_at_a:
                    raise AssertionError("Comparison fetched B without an explicit capture gesture")
                terminal.keys("r", .5)
                normalized = inspect_pages(terminal, output, "comparison-differences", [
                    "ADDED /metadata/labels/fixture-after", "REMOVED /metadata/labels/fixture-before",
                    "CHANGED /spec/containers", "128Mi", "192Mi", "API bookkeeping hidden",
                    "A stays fixed", "no desired configuration source selected."],
                    ["CHANGED /metadata/resourceVersion", "State: unknown"])
                if api.selected_reads() != reads_at_a + 1:
                    raise AssertionError("Explicit r must capture exactly one B resource observation")
                terminal.keys("n", .2)
                unnormalized = inspect_pages(terminal, output, "comparison-api-noise", [
                    "CHANGED /metadata/resourceVersion", '"100" → "101"', "API bookkeeping included"], ["API bookkeeping hidden"])
                terminal.keys("n", .2)
                restored = inspect_pages(terminal, output, "comparison-normalized-again", [
                    "API bookkeeping hidden"], ["CHANGED /metadata/resourceVersion"])
                if api.selected_reads() != reads_at_a + 1:
                    raise AssertionError("Noise toggles must reuse A/B, without fetching or replacing either observation")
                # Source/time labels are part of the retained observations.
                def observed_line(value, label):
                    return next(line.replace("│", "").strip() for line in value.split(label, 1)[1].splitlines() if "Observed:" in line)

                for label in ["A · chosen baseline", "B · comparison observation"]:
                    if observed_line(normalized, label) != observed_line(unnormalized, label) or observed_line(normalized, label) != observed_line(restored, label):
                        raise AssertionError("Noise toggles changed a retained observation timestamp")
                if observed_line(baseline, "A · chosen baseline") != observed_line(normalized, "A · chosen baseline"):
                    raise AssertionError("Explicit B capture replaced chosen baseline A")
                terminal.keys("o", .2)
                inspect_pages(terminal, output, "comparison-all-evidence", [
                    "RESOURCE OBSERVATION COMPARISON", "Source: Kubernetes API observation",
                    "128Mi", "192Mi", "UID: fixture-investigation-pod",
                    "API noise omitted (n reveals it)"], ["CHANGED /metadata/resourceVersion"])
                terminal.keys("o", .2)
                inspect_pages(terminal, output, "comparison-overview-again", [
                    "RESOURCE COMPARISON · CHANGES FIRST", "128Mi", "192Mi", "API bookkeeping hidden"])
                if api.selected_reads() != reads_at_a + 1:
                    raise AssertionError("Overview/evidence toggles captured new observations")
                comparison_path = Path(directory) / "comparison-evidence.json"
                terminal.command("evidence")
                inspect_pages(terminal, output, "evidence-retained-comparison", [
                    "EVIDENCE PREVIEW", "chosen comparison baseline A", "retained comparison B",
                    "128Mi", "192Mi", "UID: fixture-investigation-pod"])
                save_preview(terminal, comparison_path)
                comparison_bundle = json.loads(comparison_path.read_text())
                retained = comparison_bundle["observations"]
                if len(retained) != 2 or [o["object"]["spec"]["containers"][0]["resources"]["requests"]["memory"] for o in retained] != ["128Mi", "192Mi"]:
                    raise AssertionError("Comparison evidence did not retain the chosen A/B resource snapshots")
                for index, label in enumerate(["A · chosen baseline", "B · comparison observation"]):
                    if retained[index]["observedAt"] != observed_line(normalized, label).removeprefix("Observed: "):
                        raise AssertionError("Comparison export replaced a retained observation time")
                if api.selected_reads() != reads_at_a + 1:
                    raise AssertionError("Comparison evidence silently fetched a newer selected resource")
                terminal.keys("\x1b", .2)
                terminal.keys("\x1b", .2)

                terminal.command("pressure")
                inspect_pages(terminal, output, "pressure-budget-overview", [
                    "RESOURCE BUDGETS", "REQUEST", "LIMIT", "USAGE", "USE / LIMIT", "250m",
                    "192.00MiB", "256.00MiB", "sidecar", "Metrics: unavailable", "N/A",
                    "Fixture metrics unavailable", "CPU throttling: unknown"], ["usage=0m", "usage=0Mi"])
                reads_at_pressure = api.selected_reads()
                terminal.keys("2", .2)
                inspect_pages(terminal, output, "pressure-container-history", [
                    "CURRENT CONTAINERS", "CrashLoopBackOff", "Previous termination: OOMKilled",
                    "Previous termination does not establish the current cause."])
                terminal.keys("3", .2)
                inspect_pages(terminal, output, "pressure-retained-events", [
                    "RETAINED EVENTS", "FailedScheduling", "core/v1 events", "Fixture scheduler observed insufficient memory"])
                terminal.keys("5", .2)
                inspect_pages(terminal, output, "pressure-metrics-unavailable", [
                    "RESOURCE PRESSURE", "UID: fixture-investigation-pod", "Metrics: unavailable",
                    "Fixture metrics unavailable", "cpu request=250m", "memory request=192.00MiB (192Mi)",
                    "limit=256.00MiB (256Mi)", "usage=N/A", "CONTAINER sidecar", "request=N/A", "limit=N/A",
                    "last termination state: reason=OOMKilled", "FailedScheduling", "default-scheduler",
                    "not a causal diagnosis", "CPU throttling: unknown", "source: metrics.k8s.io"],
                    ["usage=0m", "usage=0Mi", "usage/request=0.0%"])
                if api.selected_reads() != reads_at_pressure:
                    raise AssertionError("Pressure tabs fetched a new resource observation")
                pressure_path = Path(directory) / "pressure-evidence.json"
                terminal.command("evidence")
                inspect_pages(terminal, output, "evidence-retained-pressure", [
                    "EVIDENCE PREVIEW", "Retained pressure inspection snapshot", "State: incomplete",
                    "raw resource object was not retained", "OOMKilled", "FailedScheduling"])
                save_preview(terminal, pressure_path)
                pressure_bundle = json.loads(pressure_path.read_text())
                if pressure_bundle["observations"][0]["state"] != "incomplete" or pressure_bundle["observations"][0].get("object"):
                    raise AssertionError("Inspection-text evidence invented a complete raw resource object")
                if not pressure_bundle["snippets"] or api.selected_reads() != reads_at_pressure:
                    raise AssertionError("Retained inspection export lost its named snippet or refreshed the resource")
                terminal.keys("\x1b", .2)
                terminal.keys("\x1b", .2)

                json_path = Path(directory) / "selected-evidence.json"
                markdown_path = Path(directory) / "roundtrip-evidence.md"
                terminal.command("evidence")
                inspect_pages(terminal, output, "evidence-preview", [
                    "EVIDENCE PREVIEW", "RESOURCE v1/pods apps/investigation-api", "UID: fixture-investigation-pod",
                    "Kubernetes API GET (explicit evidence capture)", "State: complete", "FailedScheduling",
                    "not a complete history", "INCLUDED RESOURCE SNAPSHOT", "[REDACTED]"],
                    ["fixture-credential-must-not-export"])
                if json_path.exists():
                    raise AssertionError("Evidence preview saved a file before an explicit export gesture")
                terminal.keys("n", .2)
                assert_screen(terminal, ["Investigation note"])
                note = "Fixture note: inspect allocation evidence before changing memory."
                terminal.keys(note + "\t\t\r", .3)
                inspect_pages(terminal, output, "evidence-with-note", [note])
                save_preview(terminal, json_path)
                bundle = json.loads(json_path.read_text())
                observation = bundle["observations"][0]
                if bundle["version"] != 1 or bundle["notes"] != [note] or observation["state"] != "complete":
                    raise AssertionError("Export lost the version, explicit note or observation state")
                expected_identity = {"context": "demo-dev", "gvr": "v1/pods", "namespace": "apps",
                                     "name": "investigation-api", "uid": "fixture-investigation-pod"}
                if observation["identity"] != expected_identity or not observation.get("observedAt") or not bundle["limits"]:
                    raise AssertionError("Export lost selected identity, observation time or completeness limits")
                if "fixture-credential-must-not-export" in json_path.read_text():
                    raise AssertionError("Credential-shaped fixture field escaped export redaction")
                capture(terminal, output, "evidence-explicit-export")

                # Keep chrome polling possible, but make the saved resource itself
                # unavailable. Import must use the file, never refetch its identity.
                reads_at_export = api.selected_reads()
                api.deny_selected = True
                terminal.command("evidence-open " + str(json_path))
                inspect_pages(terminal, output, "evidence-offline-import", [
                    "Offline evidence", "UID: fixture-investigation-pod", note,
                    "State: complete", "FailedScheduling", "INCLUDED RESOURCE SNAPSHOT"],
                    ["Offline evidence unavailable", "Fixture selected resource unavailable after export"])
                save_preview(terminal, markdown_path)
                markdown_json = markdown_path.read_text().split("```json k9plus-evidence-v1\n", 1)[1].split("\n```", 1)[0]
                if json.loads(markdown_json) != bundle:
                    raise AssertionError("JSON import / Markdown re-export changed identity, sources, times or retained evidence")
                if api.selected_reads() != reads_at_export:
                    raise AssertionError("Offline import/refile contacted the selected recorded resource")
                terminal.command("evidence-open " + str(markdown_path))
                inspect_pages(terminal, output, "evidence-markdown-import", [
                    "Offline evidence", "UID: fixture-investigation-pod", note, "State: complete"])
                if api.selected_reads() != reads_at_export:
                    raise AssertionError("Markdown import contacted the selected recorded resource")

                with api.request_lock:
                    requests = deepcopy(api.requests)
                event_reads = [r for r in requests if r["method"] == "GET" and r["path"] == "/api/v1/namespaces/apps/events"]
                if len(event_reads) != 2 or any(r["query"].get("fieldSelector") != ["involvedObject.uid=fixture-investigation-pod"] for r in event_reads):
                    raise AssertionError("Pressure/evidence event reads were not scoped to the selected Pod UID")
                if [r["query"].get("limit") for r in event_reads] != [["20"], ["100"]]:
                    raise AssertionError("Pressure/evidence event reads lost their respective 20/100-item bounds")
                mutations = [r for r in requests if r["method"] != "GET" and not (
                    r["method"] == "POST" and r["path"].startswith("/apis/authorization.k8s.io/"))]
                if mutations:
                    raise AssertionError(f"Read-only investigation attempted mutations: {mutations}")
                (output / "investigation-api-requests.json").write_text(json.dumps(requests, indent=2) + "\n")
                for source, name in [(json_path, "investigation-evidence.json"), (markdown_path, "investigation-evidence.md"),
                                     (comparison_path, "comparison-retained-evidence.json"), (pressure_path, "pressure-retained-evidence.json")]:
                    destination = output / name
                    destination.write_bytes(source.read_bytes())
                    destination.chmod(0o600)
                os.write(terminal.master, b":quit\r")
                terminal.process.wait(timeout=5)
                if terminal.process.returncode != 0:
                    raise AssertionError(f"Normal investigation quit returned {terminal.process.returncode}")
                return {"fixture": "selected Pod, metrics 503, OOM last state and UID-scoped FailedScheduling event",
                        "columns": 120, "rows": 34, "elapsed_seconds": round(time.monotonic() - started, 3),
                        "assertions": ["on-demand selected identity and unavailable-metrics diagnostics",
                                       "explicit A/B capture keeps A, adds/removes/changes, reversible API-noise toggle",
                                       "pressure retains configuration/OOM/events and N/A metrics for both containers",
                                       "comparison A/B and incomplete inspection text export their retained source/time without refreshing",
                                       "explicit JSON export -> offline import -> identical Markdown export/import; mode 0600",
                                       "UID-scoped bounded events; no resource mutation or offline resource refetch"],
                        "result": "passed"}
            finally:
                terminal.close()
    finally:
        api.stopped.set()
        api.shutdown()
        api.server_close()


def key_to_paint(terminal, keys, predicate, timeout=3):
    """Measure emitted terminal output satisfying a visible-state assertion."""
    started = time.monotonic()
    os.write(terminal.master, keys.encode())
    while time.monotonic() - started < timeout:
        if select.select([terminal.master], [], [], .01)[0]:
            data = os.read(terminal.master, 65536)
            terminal.stream.feed(terminal.decoder.decode(data))
            if predicate(text(terminal)):
                return (time.monotonic() - started) * 1000
        if terminal.process.poll() is not None:
            raise AssertionError("Application exited while waiting for terminal paint")
    raise AssertionError("Input failed to produce expected terminal paint:\n" + text(terminal))


def slow_api_journey(binary, output):
    demo.COLS, demo.ROWS = 100, 30
    api = JourneyAPI()
    threading.Thread(target=api.serve_forever, daemon=True).start()
    try:
        with tempfile.TemporaryDirectory(prefix="k9plus-slow-api-") as directory:
            terminal = demo.Terminal(binary, directory, api.server_port, command="deployments apps")
            try:
                terminal.drain(4)
                terminal.keys("r", .3)
                assert_screen(terminal, ["Confirm Restart", "demo-dev", "fixture-deployment"])
                terminal.keys("\t\r", .3)
                if not api.patch_started.wait(2):
                    raise AssertionError("Restart did not reach the disposable delayed PATCH endpoint:\n" + text(terminal))
                latency = key_to_paint(terminal, "/no-such-workload\r", lambda s: "no-such-workload" in s and "0/1" in s)
                fcntl.ioctl(terminal.master, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
                terminal.screen.resize(24, 80)
                demo.COLS, demo.ROWS = 80, 24
                terminal.drain(.15)
                assert_screen(terminal, ["[RW]", "demo-dev"])
                capture(terminal, output, "slow-restart-filter-resize")
                started = time.monotonic()
                os.write(terminal.master, b":quit\r")
                terminal.process.wait(timeout=1)
                elapsed = time.monotonic() - api.patch_started_at
                if elapsed >= 5:
                    raise AssertionError("Input/quit only completed after delayed API returned")
                return {"fixture": "5-second restart PATCH", "filter_paint_ms": round(latency, 3),
                        "quit_ms": round((time.monotonic() - started) * 1000, 3),
                        "quit_while_api_pending_seconds": round(elapsed, 3), "result": "passed"}
            finally:
                terminal.close()
    finally:
        api.patch_release.set()
        api.stopped.set()
        api.shutdown()
        api.server_close()


def performance_journey(binary, output):
    demo.COLS, demo.ROWS = 120, 34
    api = JourneyAPI()
    exemplar = next(o for o in api.objects if o["kind"] == "Pod")
    api.objects = [o for o in api.objects if o["kind"] != "Pod"]
    for i in range(10000):
        obj = json.loads(json.dumps(exemplar))
        obj["metadata"].update(name=f"target-{i:05d}", uid=f"pod-{i}")
        api.objects.append(obj)
    threading.Thread(target=api.serve_forever, daemon=True).start()
    try:
        with tempfile.TemporaryDirectory(prefix="k9plus-paint-") as directory:
            terminal = demo.Terminal(binary, directory, api.server_port, command="pods apps", flags=["--readonly"])
            try:
                terminal.drain(5)
                samples = []
                for i in range(1, 21):
                    name = f"target-{i:05d}"
                    # The draft prompt can contain the new name while the old
                    # result still says 1/10000. Require its committed title and
                    # an emitted resource row before recording the sample.
                    def painted(screen, name=name):
                        return "</" + name + ">" in screen and "1/10000" in screen and any(
                            "│" in line and name in line and "</" not in line for line in screen.splitlines())
                    samples.append(key_to_paint(terminal, "/" + name + "\r", painted, timeout=15))
                capture(terminal, output, "pods-10000-filtered")
                p95 = sorted(samples)[18]
                return {"fixture": "10000 retained Pods, 20 committed regex filters", "samples_ms": [round(n, 3) for n in samples],
                        "median_ms": round(statistics.median(samples), 3), "p95_ms": round(p95, 3),
                        "target_ms": 100, "p95_meets_target": p95 <= 100,
                        "method": "Input write to emitted visible result/count parsed from real PTY; includes Python parser overhead, no drain wait.",
                        "result": "passed"}
            finally:
                terminal.close()
    finally:
        api.stopped.set()
        api.shutdown()
        api.server_close()


def run(binary, output, columns, rows, no_icons=False):
    demo.COLS, demo.ROWS = columns, rows
    api = JourneyAPI()
    threading.Thread(target=api.serve_forever, daemon=True).start()
    started = time.monotonic()
    try:
        with tempfile.TemporaryDirectory(prefix="k9plus-journey-") as directory:
            terminal = demo.Terminal(binary, directory, api.server_port, flags=["--readonly"],
                                     ui_config="    noIcons: true\n" if no_icons else "")
            try:
                terminal.drain(5)
                terminal.keys("/payments\r", 1)
                assert_screen(terminal, ["payments"], ["grafana"])
                capture(terminal, output, f"filter-valid-{columns}")
                terminal.keys("/[\r", 1)
                assert_screen(terminal, ["payments"], ["grafana"])
                capture(terminal, output, f"filter-invalid-{columns}")
                # Invalid input remains in the editor for correction; Ctrl-U
                # deliberately clears that draft before entering a new query.
                terminal.keys("\x15never matches\r", 1)
                assert_screen(terminal, excluded=["grafana", "infrastructure"])
                capture(terminal, output, f"filter-empty-{columns}")
                terminal.keys("/\r", 1)
                terminal.keys("/payments\r", 1)
                terminal.command("troubleshoot")
                assert_screen(terminal, ["Kustomization", "flux-system/payments"], ["kustomize.toolkit.fluxcd.io/v1/kustomizations|flux-system"])
                capture(terminal, output, f"flux-inspection-{columns}")
                terminal.keys("\x1b")
                terminal.command("kustomizations flux-system")
                terminal.keys("/payments\r", 1)
                terminal.command("troubleshoot")
                assert_screen(terminal, ["Kustomization", "flux-system/payments"])
                capture(terminal, output, f"native-flux-inspection-{columns}")
                terminal.keys("\x1b")
                terminal.command("pulse")
                for command in ["troubleshoot", "tls", "actions"]:
                    terminal.command(command)
                    assert_screen(terminal, excluded=["Boom!!", "nil pointer"])
                    capture(terminal, output, f"pulse-{command}-{columns}")
                    if command == "actions":
                        terminal.keys("\x1b")
                terminal.command("pods apps")
                assert_screen(terminal, ["CrashLoopBackOff", "ContainerCreating", "[RO]"])
                capture(terminal, output, f"pods-{columns}{'-no-icons' if no_icons else ''}")
                terminal.keys("\x05")
                assert_screen(terminal, ["[RO]", "demo-dev"])
                terminal.command("not-a-command")
                assert_screen(terminal, ["[RO]", "demo-dev"])
                capture(terminal, output, f"identity-during-error-{columns}")
                terminal.keys("\x1b")
                os.write(terminal.master, b":quit\r")
                terminal.process.wait(timeout=5)
                if terminal.process.returncode != 0:
                    raise AssertionError(f"Normal quit returned {terminal.process.returncode}")
            finally:
                terminal.close()
    finally:
        api.stopped.set()
        api.shutdown()
        api.server_close()
    return {"columns": columns, "rows": rows, "no_icons": no_icons,
            "elapsed_seconds": round(time.monotonic() - started, 3), "result": "passed"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", type=Path, default=Path("assets/daily-app"))
    parser.add_argument("--size", action="append", help="WIDTHxHEIGHT; default 80x24, 100x30, 120x34")
    parser.add_argument("--slow-api", action="store_true", help="Verify filtering, resize and quit during a five-second restart request")
    parser.add_argument("--performance", action="store_true", help="Measure actual PTY output for 20 filters over 10000 Pod fixtures")
    parser.add_argument("--investigations", action="store_true", help="Exercise selected diagnostics, A/B comparison, pressure and offline evidence roundtrip")
    parser.add_argument("--only-investigations", action="store_true", help="Run only the selected-investigation journey")
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    binary = args.binary.resolve()
    journeys = []
    for value in [] if args.only_investigations else args.size or ["80x24", "100x30", "120x34"]:
        columns, rows = map(int, value.split("x"))
        journeys.append(run(binary, args.output, columns, rows, no_icons=columns == 80))
        print(json.dumps(journeys[-1]), flush=True)
    if args.slow_api:
        journeys.append(slow_api_journey(binary, args.output))
        print(json.dumps(journeys[-1]), flush=True)
    if args.performance:
        journeys.append(performance_journey(binary, args.output))
        print(json.dumps(journeys[-1]), flush=True)
    if args.investigations or args.only_investigations:
        journeys.append(investigation_journey(binary, args.output))
        print(json.dumps(journeys[-1]), flush=True)
    (args.output / "journeys.json").write_text(json.dumps({
        "captured_at": datetime.now(timezone.utc).isoformat(), "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "platform": platform.platform(), "cpu_count": os.cpu_count(), "journeys": journeys,
        "coverage": "Real PTY and disposable loopback Kubernetes API fixtures; no live Kubernetes or Relay integration.",
        "timing": "Journey durations include deliberate drain waits; they are not keystroke-to-paint latency measurements."
    }, indent=2) + "\n")


if __name__ == "__main__":
    main()

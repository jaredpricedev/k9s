#!/usr/bin/env python3
# Modified for k9+; see NOTICE.
"""Actual daily-workspace PTY journeys against a disposable localhost API.

Requires the same Pillow/pyte packages as capture-demo.py. No live Kubernetes
configuration is inherited. Captures contain only emitted terminal cells.
"""

import argparse
from copy import deepcopy
from datetime import datetime, timedelta, timezone
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import struct
import tempfile
import termios
import threading
import time
from urllib.parse import parse_qs, urlparse

spec = importlib.util.spec_from_file_location("regression_journeys", Path(__file__).with_name("regression-journeys.py"))
journeys = importlib.util.module_from_spec(spec)
spec.loader.exec_module(journeys)
demo = journeys.demo


class WorkspaceHandler(journeys.InvestigationHandler):
    def record(self, method):
        parsed = urlparse(self.path)
        with self.server.request_lock:
            self.server.requests.append({"method": method, "path": parsed.path,
                                         "query": parse_qs(parsed.query)})
        return parsed

    def failure(self, code, reason, message):
        body = json.dumps({"kind": "Status", "apiVersion": "v1", "status": "Failure",
                           "message": message, "reason": reason, "code": code}).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        parsed = urlparse(self.path)
        query = parse_qs(parsed.query)
        if parsed.path == self.server.selected_path + "/log":
            self.record("GET")
            if not self.server.logs_active or query.get("container", [""])[0] not in ("api", "sidecar"):
                return self.failure(400, "BadRequest", "Logs require explicit selected Pod and container")
            stamp = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
            body = (stamp + " daily workspace fixture safe log line\n").encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if parsed.path == "/version" and self.server.connection_fail:
            self.record("GET")
            return self.failure(503, "ServiceUnavailable", "Fixture connection probe unavailable")
        parts = parsed.path.strip("/").split("/")
        gv, rest = ("v1", parts[2:]) if parts[:2] == ["api", "v1"] else ("/".join(parts[1:3]), parts[3:])
        namespaced = len(rest) == 3 and rest[0] == "namespaces"
        resource = rest[-1] if rest else ""
        selected = resource in self.server.workspace_resources
        probe = namespaced and resource == "pods" and query.get("limit") == ["1"]
        native_log_selection = namespaced and resource == "pods" and rest[1] == "apps" and self.server.logs_active and query.get("fieldSelector") == ["metadata.name=investigation-api"]
        if native_log_selection:
            self.record("GET")
            if query.get("watch") == ["true"]:
                # The regular fixture supports named Pod watches and holds the
                # HTTP stream until this disposable server is stopped.
                demo.APIHandler.do_GET(self)
                return
            if query.get("limit") != ["128"]:
                return self.failure(400, "BadRequest", "Native log discovery requires its 128-Pod bound")
            return self.reply({"apiVersion": "v1", "kind": "PodList", "metadata": {"resourceVersion": "100"},
                               "items": [deepcopy(self.server.selected())]})
        workspace_list = namespaced and selected and query.get("labelSelector") == ["app=daily"]
        if self.server.enforce_workspace and selected and query.get("watch") != ["true"] and (
                len(rest) == 1 or namespaced) and not workspace_list and not (probe and self.server.connection_active):
            self.record("GET")
            return self.failure(400, "BadRequest", "Workspace LIST requires explicit namespace and the exact app=daily selector")
        if probe or workspace_list:
            self.record("GET")
            if probe:
                if self.server.connection_fail:
                    return self.failure(503, "ServiceUnavailable", "Fixture namespace probe unavailable")
            else:
                if rest[1] not in ("apps", "ops") or query.get("limit") != ["200"]:
                    return self.failure(400, "BadRequest", "Workspace request must use explicit scope and bounded list")
                if self.server.all_unavailable:
                    return self.failure(503, "ServiceUnavailable", "Fixture refresh unavailable")
                if resource == "ingresses":
                    return self.failure(403, "Forbidden", "Fixture ingress LIST denied")
            kind = next((kind for name, kind, _ in demo.GROUPS.get(gv, []) if name == resource), "List")
            items = [deepcopy(o) for o in self.server.objects if o["apiVersion"] == gv and o["kind"] == kind
                     and o["metadata"].get("namespace") == rest[1]
                     and (probe or o["metadata"].get("labels", {}).get("app") == "daily")]
            if probe:
                items = items[:1]
            return self.reply({"apiVersion": gv, "kind": kind + "List", "metadata": {"resourceVersion": "100"}, "items": items})
        # Discovery, the initial browser/watch, selected GET and UID-scoped
        # events use the inherited retained-evidence fixture implementation.
        super().do_GET()


class WorkspaceAPI(journeys.InvestigationAPI):
    workspace_resources = {"pods", "deployments", "jobs", "cronjobs", "resourcequotas", "certificates", "ingresses"}

    def __init__(self):
        demo.GROUPS["v1"].append(("resourcequotas", "ResourceQuota", True))
        demo.GROUPS["batch/v1"] = [("jobs", "Job", True), ("cronjobs", "CronJob", True)]
        demo.GROUPS["networking.k8s.io/v1"] = [("ingresses", "Ingress", True)]
        super().__init__()
        self.RequestHandlerClass = WorkspaceHandler
        self.all_unavailable = False
        self.connection_fail = False
        self.connection_active = False
        self.logs_active = False
        self.enforce_workspace = False
        self.selected()["metadata"]["labels"]["app"] = "daily"
        now = datetime.now(timezone.utc).replace(microsecond=0)

        def resource(api_version, kind, name, namespace, spec=None, status=None):
            return {"apiVersion": api_version, "kind": kind, "metadata": {
                "name": name, "namespace": namespace, "uid": "fixture-" + name,
                "resourceVersion": "100", "generation": 1,
                "creationTimestamp": now.isoformat().replace("+00:00", "Z"), "labels": {"app": "daily"}},
                "spec": spec or {}, "status": status or {}}

        self.objects.extend([
            {"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "ops", "uid": "fixture-namespace-ops", "resourceVersion": "100"}},
            resource("v1", "Pod", "daily-worker", "ops", {"containers": [{"name": "worker", "image": "example.test/worker:v1"}]}, {
                "phase": "Running", "conditions": [{"type": "Ready", "status": "True"}], "containerStatuses": [{
                    "name": "worker", "ready": True, "restartCount": 0, "state": {"running": {}}}]}),
            resource("apps/v1", "Deployment", "daily-release", "apps", {"replicas": 2}, {
                "observedGeneration": 1, "replicas": 2, "availableReplicas": 0, "readyReplicas": 0,
                "conditions": [{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded",
                                "message": "Fixture rollout deadline exceeded"}]}),
            resource("batch/v1", "Job", "daily-migration", "apps", {}, {"failed": 3, "conditions": [{
                "type": "Failed", "status": "True", "reason": "BackoffLimitExceeded", "message": "Fixture terminal job failure"}]}),
            resource("batch/v1", "CronJob", "daily-backup", "ops", {"schedule": "0 * * * *", "suspend": True}),
            resource("v1", "ResourceQuota", "daily-compute", "ops", {}, {
                "hard": {"requests.cpu": "1"}, "used": {"requests.cpu": "950m"}}),
            resource("cert-manager.io/v1", "Certificate", "daily-tls", "ops", {}, {
                "notAfter": (now - timedelta(days=1)).isoformat().replace("+00:00", "Z"),
                "conditions": [{"type": "Ready", "status": "True", "observedGeneration": 1}]}),
        ])
        self.original_selected = deepcopy(self.selected())

    def selected(self):
        return next(o for o in self.objects if o["kind"] == "Pod" and o["metadata"]["name"] == "investigation-api")

    def set_selected_healthy(self):
        selected = self.selected()
        selected["metadata"]["resourceVersion"] = "102"
        for status in selected["status"]["containerStatuses"]:
            status["ready"] = True
            status["state"] = {"running": {}}

    def restore_selected(self):
        for i, obj in enumerate(self.objects):
            if obj["kind"] == "Pod" and obj["metadata"]["name"] == "investigation-api":
                self.objects[i] = deepcopy(self.original_selected)
                return


def observed(terminal):
    terminal.keys("v", .2)
    match = re.search(r"Captured: ([^\s│]+)", journeys.text(terminal))
    terminal.keys("\x1b", .2)
    if not match:
        raise AssertionError("Workspace observation timestamp missing:\n" + journeys.text(terminal))
    return match.group(1)


def wait_screen(terminal, required, timeout=8):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if all(value in journeys.text(terminal) for value in required):
            journeys.assert_screen(terminal, required)
            return
        terminal.drain(.1)
    journeys.assert_screen(terminal, required)


def store_metadata(root):
    store = root / "config" / "k9plus" / "workspaces.yaml"
    if store.stat().st_mode & 0o777 != 0o600:
        raise AssertionError("Workspace metadata permissions are not 0600")
    value = store.read_text()
    for expected in ("active: daily", "context: demo-dev", "labelSelector: app=daily", "fixture-investigation-pod",
                     "name: faults", "query: kind:pod status:crashloopbackoff", "layout: inventory"):
        if expected not in value:
            raise AssertionError(f"Persisted workspace metadata missing {expected!r}:\n{value}")
    return store, value


def check_requests(api, start, evidence_start, connection_start):
    with api.request_lock:
        journal = deepcopy(api.requests)
    requests = journal[start:]
    scoped = []
    for request in requests:
        path, query = request["path"], request["query"]
        if request["method"] in ("PATCH", "PUT", "DELETE") or request["method"] == "POST" and "selfsubjectaccessreviews" not in path:
            raise AssertionError("Journey mutated a Kubernetes resource: " + repr(request))
        if "/secrets" in path:
            raise AssertionError("Journey fetched Secrets: " + repr(request))
        if request["method"] != "GET" or query.get("watch") == ["true"]:
            continue
        parts = path.strip("/").split("/")
        rest = parts[2:] if parts[:2] == ["api", "v1"] else parts[3:]
        if not rest or rest[-1] not in api.workspace_resources:
            continue
        if len(rest) != 3 or rest[0] != "namespaces" or rest[1] not in ("apps", "ops"):
            raise AssertionError("Workspace issued a broad or unexpected namespace LIST: " + repr(request))
        if rest[-1] == "pods" and query.get("limit") == ["1"]:
            continue
        if rest[-1] == "pods" and rest[1] == "apps" and query.get("limit") == ["128"] and query.get("fieldSelector") == ["metadata.name=investigation-api"]:
            continue
        if query.get("labelSelector") != ["app=daily"] or query.get("limit") != ["200"]:
            raise AssertionError("Workspace changed its selector or request bound: " + repr(request))
        scoped.append(request)
    if not scoped or {r["path"].split("/namespaces/")[1].split("/")[0] for r in scoped} != {"apps", "ops"}:
        raise AssertionError("Both saved namespaces were not explicitly queried")
    evidence_reads = [r for r in journal[evidence_start:connection_start] if r["path"] == api.selected_path and r["method"] == "GET"]
    if not evidence_reads:
        raise AssertionError("Evidence did not read the selected pinned resource")
    connection = journal[connection_start:]
    if not any(r["path"] == "/version" for r in connection) or not any(
            r["path"] == "/api/v1/namespaces/apps/pods" and r["query"].get("limit") == ["1"] for r in connection):
        raise AssertionError("Connection checks did not use /version and an explicit namespace pod LIST limit=1")
    return journal, scoped


def run(binary, output):
    demo.COLS, demo.ROWS = 120, 34
    api = WorkspaceAPI()
    threading.Thread(target=api.serve_forever, daemon=True).start()
    terminal = None
    started = time.monotonic()
    binary_sha256 = hashlib.sha256(binary.read_bytes()).hexdigest()
    checks = []
    capture_names = []

    def capture(name):
        journeys.capture(terminal, output, name)
        capture_names.append(name)

    def inspect(name, required):
        seen = journeys.inspect_pages(terminal, output, name, required)
        pages = len(seen.splitlines()) // terminal.screen.lines
        capture_names.extend(f"{name}-{page}" for page in range(1, pages + 1))
        return seen

    try:
        with tempfile.TemporaryDirectory(prefix="k9plus-daily-workspace-") as directory:
            root = Path(directory)
            terminal = demo.Terminal(binary, directory, api.server_port, command="pods apps", flags=["--readonly"])
            terminal.drain(4)
            terminal.command("workspace")
            journeys.assert_screen(terminal, ["Workspace", "No saved scopes", "No observation", "demo-dev"])
            capture("workspace-empty")
            request_start = len(api.requests)
            api.enforce_workspace = True
            terminal.keys("n", .3)
            journeys.assert_screen(terminal, ["Create workspace", "Name", "Namespaces", "Save workspace"])
            capture("workspace-create-form")
            terminal.keys("\x1b", .3)
            journeys.assert_screen(terminal, ["No saved scopes"], ["Create workspace"])
            checks.append("empty workspace and keyboard form cancellation")

            terminal.command("workspace save daily apps,ops --selector=app=daily --kinds=pods,deployments,jobs,cronjobs,resourcequotas,certificates,ingresses")
            wait_screen(terminal, ["CrashLoopBackOff", "ProgressDeadlineExceeded", "BackoffLimitExceeded", "CertificateExpired", "QuotaNearLimit", "Suspended", "coverage gaps"])
            capture("daily-queue")
            reads_before_idle = sum(request["query"].get("labelSelector") == ["app=daily"] for request in api.requests)
            terminal.drain(2.3)
            if sum(request["query"].get("labelSelector") == ["app=daily"] for request in api.requests) != reads_before_idle:
                raise AssertionError("Workspace started background inventory scans without a refresh gesture")
            checks.append("retained workspace does not poll its configured inventory in the background")
            terminal.keys("3", .3)
            journeys.assert_screen(terminal, ["denied", "ingresses", "apps", "ops"])
            capture("daily-coverage-denied")
            terminal.keys("2", .3)
            journeys.assert_screen(terminal, ["investigation-api", "daily-worker", "daily-compute", "AGE"])
            capture("daily-inventory")
            checks.append("kind-aware daily queue and explicit ingress RBAC coverage")

            terminal.command("inventory name:investigation-api")
            wait_screen(terminal, ["Search: name:investigation-api", "CrashLoopBackOff"])
            terminal.keys("p", .3)
            terminal.keys("4", .3)
            journeys.assert_screen(terminal, ["investigation-api", "Pinned identity"])
            metadata = (root / "config" / "k9plus" / "workspaces.yaml").read_text()
            if "uid: fixture-investigation-pod" not in metadata:
                raise AssertionError("Pin did not persist the selected UID")
            capture("daily-pinned-identity")
            terminal.keys("2", .3)
            journeys.assert_screen(terminal, ["Search: name:investigation-api", "investigation-api"])
            terminal.keys("\r", .5)
            wait_screen(terminal, ["CURRENT FINDINGS", "CrashLoopBackOff", "READY", "Previous termination: OOMKilled"])
            capture("daily-compact-investigation")
            terminal.keys("\x1b", .3)
            journeys.assert_screen(terminal, ["Workspace", "Search: name:investigation-api"])

            api.logs_active = True
            prior_uid = api.selected()["metadata"]["uid"]
            api.selected()["metadata"]["uid"] = "fixture-replacement-pod"
            rejected_at = len(api.requests)
            terminal.keys("l", .5)
            wait_screen(terminal, ["Logs were not opened", "identity changed"])
            if any(request["path"] == api.selected_path + "/log" for request in api.requests[rejected_at:]):
                raise AssertionError("Replacement Pod was allowed to stream logs for the retained identity")
            capture("daily-logs-replacement-rejected")
            terminal.keys("\x1b", .3)
            api.selected()["metadata"]["uid"] = prior_uid
            terminal.keys("l", .5)
            wait_screen(terminal, ["daily workspace fixture safe log line", "investigation-api"])
            capture("daily-native-pod-logs")
            terminal.keys("\x1b", .3)
            journeys.assert_screen(terminal, ["Workspace", "Search: name:investigation-api"])
            api.logs_active = False
            checks.append("native Pod logs preserve workspace query and refuse replaced UID before any log request")

            evidence_start = len(api.requests)
            terminal.command("evidence")
            inspect("daily-evidence-preview", ["EVIDENCE PREVIEW", "fixture-investigation-pod", "investigation-api"])
            terminal.keys("\x1b", .3)
            checks.append("UID-bearing pins, native investigation, retained query and evidence target")

            terminal.command("workspace search save faults kind:pod status:crashloopbackoff")
            terminal.command("inventory name:investigation-api")
            before = observed(terminal)
            terminal.drain(1.1)
            api.set_selected_healthy()
            terminal.keys("r", .6)
            wait_screen(terminal, ["Running 2/2 ready", "Successful reads updated"])
            if observed(terminal) == before:
                raise AssertionError("Successful partial refresh did not update observation time")
            capture("daily-partial-refresh")
            retained_time = observed(terminal)
            api.all_unavailable = True
            terminal.drain(1.1)
            terminal.keys("r", .6)
            wait_screen(terminal, ["Refresh unavailable", "retained observation", "Running 2/2 ready"])
            if observed(terminal) != retained_time:
                raise AssertionError("All-unavailable refresh changed retained observation time")
            capture("daily-unavailable-retained")
            checks.append("persistent RBAC gaps allow good reads to refresh; total failure retains rows and original time")

            api.all_unavailable = False
            connection_start = len(api.requests)
            api.connection_active = True
            terminal.command("connection")
            inspect("daily-connection-readable", ["CONNECTION AND SESSION", "READABLE", "GET /version", "LIST pods limit=1"])
            api.connection_fail = True
            terminal.keys("r", .5)
            inspect("daily-connection-failed-retry", ["PREVIOUS READABLE OBSERVATION", "API UNAVAILABLE", "retained, not current"])
            terminal.keys("\x1b", .3)
            journeys.assert_screen(terminal, ["Workspace", "name:investigation-api"])
            api.connection_fail = False
            api.connection_active = False
            terminal.close()
            terminal = None

            api.restore_selected()
            if hashlib.sha256(binary.read_bytes()).hexdigest() != binary_sha256:
                raise AssertionError("Binary changed during the journey; rerun with one stable build")
            terminal = demo.Terminal(binary, directory, api.server_port, command="workspace", flags=["--readonly"])
            terminal.drain(4)
            journeys.assert_screen(terminal, ["daily", "demo-dev", "[2 Inventory]", "investigation-api"])
            store, metadata = store_metadata(root)
            terminal.keys("S", .3)
            journeys.assert_screen(terminal, ["Saved searches", "faults", "Open inventory"])
            capture("daily-saved-search-picker")
            terminal.keys("\t\t\r", .5)
            wait_screen(terminal, ["Search: kind:pod status:crashloopbackoff", "investigation-api", "CrashLoopBackOff"])
            capture("daily-saved-search-restored")
            checks.append("0600 local scope, UID pin, saved query and inventory layout survive app restart")
            checks.append("connection retry uses bounded explicit reads and preserves prior success as historical")

            # A real SIGWINCH reflows the actual app; this is not a composed mock.
            terminal.screen.resize(24, 80)
            fcntl.ioctl(terminal.master, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
            os.killpg(terminal.process.pid, signal.SIGWINCH)
            terminal.drain(.5)
            journeys.assert_screen(terminal, ["Workspace", "daily", "demo-dev", "[2 Inventory]", "investigation-api"])
            terminal.keys("v", .3)
            journeys.assert_screen(terminal, ["Context: demo-dev", "Namespaces: apps, ops", "Selector: app=daily", "investigation-api"])
            capture("daily-exact-scope-80x24")
            terminal.keys("\x1b", .3)
            capture("daily-scope-header-80x24")
            checks.append("real 80x24 terminal resize preserves scope identity, selector and selected resource")

            journal, scoped = check_requests(api, request_start, evidence_start, connection_start)
            (output / "request-journal.json").write_text(json.dumps(journal, indent=2) + "\n")
            (output / "persisted-workspaces.yaml").write_text(metadata)
            os.chmod(output / "persisted-workspaces.yaml", 0o600)
            checks.append("journal confirms namespace-specific exact-selector bounded LISTs, no Secrets or resource mutations")
            manifest = {"result": "passed", "source": "actual PTY terminal cells; disposable local API fixture",
                        "binary": str(binary), "columns": demo.COLS, "rows": demo.ROWS,
                        "binary_sha256": binary_sha256,
                        "captured_at_utc": datetime.now(timezone.utc).isoformat(),
                        "elapsed_seconds": round(time.monotonic() - started, 3), "checks": checks,
                        "workspace_list_count": len(scoped), "captures": [name + ".png" for name in capture_names],
                        "responsive_capture": {"file": "daily-scope-header-80x24.png", "columns": 80, "rows": 24},
                        "request_journal": "request-journal.json", "store_mode": "0600"}
            (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
            return manifest
    finally:
        with api.request_lock:
            (output / "request-journal.json").write_text(json.dumps(api.requests, indent=2) + "\n")
        if terminal is not None:
            terminal.close()
        api.stopped.set()
        api.shutdown()
        api.server_close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    manifest = run(args.binary.resolve(), args.output.resolve())
    print(json.dumps(manifest, indent=2), flush=True)


if __name__ == "__main__":
    main()

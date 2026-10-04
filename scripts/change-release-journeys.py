#!/usr/bin/env python3
# Modified for k9+; see NOTICE.
"""Actual desired-state and rollout PTY journeys against a disposable local API.

Uses the existing pyte/Pillow capture harness. No live Kubernetes settings are
inherited. PNGs contain only terminal cells emitted by the Go-built application.
The request journal audits scope, UID ownership, retained previews and writes.
"""

import argparse
from copy import deepcopy
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import tempfile
import threading
import time
from urllib.parse import parse_qs, urlparse

spec = importlib.util.spec_from_file_location(
    "regression_journeys", Path(__file__).with_name("regression-journeys.py"))
journeys = importlib.util.module_from_spec(spec)
spec.loader.exec_module(journeys)
demo = journeys.demo

DEPLOYMENT_PATH = "/apis/apps/v1/namespaces/apps/deployments/checkout"
RS_PATH = "/apis/apps/v1/namespaces/apps/replicasets"
PODS_PATH = "/api/v1/namespaces/apps/pods"
DEPLOYMENT_UID = "fixture-checkout-deployment"
OLD_RS_UID = "fixture-checkout-rs-old"
CURRENT_RS_UID = "fixture-checkout-rs-current"
SECRET_MARKER = "fixture-secret-must-not-render"
GUIDE_IMAGES = {"desired-resource-table", "desired-authored-detail",
               "rollout-progress-overview", "rollout-historical-preview"}


def stamp():
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def template(image, memory="128Mi", defaulted=False):
    spec = {"containers": [{"name": "api", "image": image,
                            "env": [{"name": "ACCESS_TOKEN", "value": SECRET_MARKER}],
                            "resources": {"requests": {"cpu": "250m", "memory": memory},
                                          "limits": {"cpu": "1", "memory": "256Mi"}}}]}
    if defaulted:
        spec.update(dnsPolicy="ClusterFirst", restartPolicy="Always", terminationGracePeriodSeconds=30)
        spec["containers"][0]["imagePullPolicy"] = "IfNotPresent"
    return {"metadata": {"labels": {"app": "checkout"}}, "spec": spec}


def object_fixture(kind, name, uid, spec, status=None, owner=None):
    api_version = "v1" if kind == "Pod" else "apps/v1"
    metadata = {"name": name, "namespace": "apps", "uid": uid, "resourceVersion": "100",
                "generation": 3, "creationTimestamp": stamp(), "labels": {"app": "checkout"}}
    if owner:
        metadata["ownerReferences"] = [{"apiVersion": "apps/v1", "kind": owner[0],
                                        "name": owner[1], "uid": owner[2], "controller": True}]
    return {"apiVersion": api_version, "kind": kind, "metadata": metadata,
            "spec": spec, "status": status or {}}


class ChangeReleaseHandler(journeys.JourneyHandler):
    def record(self, method):
        parsed = urlparse(self.path)
        with self.server.request_lock:
            self.server.requests.append({"method": method, "path": parsed.path,
                                         "query": parse_qs(parsed.query), "stage": self.server.stage})
        return parsed

    def failure(self, code, reason, message):
        body = json.dumps({"kind": "Status", "apiVersion": "v1", "status": "Failure",
                           "reason": reason, "message": message, "code": code}).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        parsed = self.record("GET")
        query = parse_qs(parsed.query)
        if "/secrets" in parsed.path:
            return self.failure(403, "Forbidden", "Fixture forbids all Secret reads")
        if "/namespaces/outside/" in parsed.path:
            return self.failure(403, "Forbidden", "Outside namespace must be excluded without an API request")
        if parsed.path == DEPLOYMENT_PATH:
            if self.server.deployment_denied:
                return self.failure(403, "Forbidden", "Fixture selected Deployment GET denied")
            return self.reply(deepcopy(self.server.deployment()))

        # List fixtures include same-label foreign-controller descendants so the
        # application, rather than the server, must filter actual owning UIDs.
        parts = parsed.path.strip("/").split("/")
        gv, rest = ("v1", parts[2:]) if parts[:2] == ["api", "v1"] else ("/".join(parts[1:3]), parts[3:])
        if len(rest) == 3 and rest[0] == "namespaces" and query.get("watch") != ["true"]:
            resource, namespace = rest[2], rest[1]
            kind = next((kind for name, kind, _ in demo.GROUPS.get(gv, []) if name == resource), None)
            if kind:
                items = [deepcopy(item) for item in self.server.objects
                         if item["apiVersion"] == gv and item["kind"] == kind
                         and item["metadata"].get("namespace") == namespace]
                selector = query.get("labelSelector", [""])[0]
                for term in selector.split(",") if selector else ():
                    if "=" not in term:
                        return self.failure(400, "BadRequest", "Fixture accepts explicit equality selectors only")
                    key, value = term.split("=", 1)
                    items = [item for item in items if item["metadata"].get("labels", {}).get(key) == value]
                field_selector = query.get("fieldSelector", [""])[0]
                if field_selector.startswith("metadata.name="):
                    items = [item for item in items if item["metadata"]["name"] == field_selector.split("=", 1)[1]]
                return self.reply({"apiVersion": gv, "kind": kind + "List",
                                   "metadata": {"resourceVersion": "100"}, "items": items})
        super().do_GET()

    def do_POST(self):
        self.record("POST")
        super().do_POST()

    def do_PATCH(self):
        self.record("PATCH")
        self.failure(405, "MethodNotAllowed", "Change/release fixture forbids mutations")

    def do_PUT(self):
        self.record("PUT")
        self.failure(405, "MethodNotAllowed", "Change/release fixture forbids mutations")

    def do_DELETE(self):
        self.record("DELETE")
        self.failure(405, "MethodNotAllowed", "Change/release fixture forbids mutations")


class ChangeReleaseAPI(journeys.JourneyAPI):
    def __init__(self):
        if not any(resource == "replicasets" for resource, _, _ in demo.GROUPS["apps/v1"]):
            demo.GROUPS["apps/v1"].append(("replicasets", "ReplicaSet", True))
        super().__init__()
        self.RequestHandlerClass = ChangeReleaseHandler
        self.request_lock = threading.Lock()
        self.requests = []
        self.stage = "startup"
        self.deployment_denied = False
        # One Deployment makes the selected browser identity deterministic.
        self.objects = [obj for obj in self.objects if obj["kind"] != "Deployment"]
        deployment = object_fixture("Deployment", "checkout", DEPLOYMENT_UID, {
            "replicas": 2, "selector": {"matchLabels": {"app": "checkout"}},
            "template": template("example.test/api:v1", defaulted=True)}, {
                "observedGeneration": 3, "replicas": 2, "updatedReplicas": 2,
                "readyReplicas": 2, "availableReplicas": 2,
                "conditions": [{"type": "Progressing", "status": "True", "reason": "NewReplicaSetAvailable"}]})
        deployment["metadata"]["labels"].update({"kustomize.toolkit.fluxcd.io/name": "checkout",
                                                  "kustomize.toolkit.fluxcd.io/namespace": "flux-system"})
        self.objects.append(deployment)
        self.set_rollout("complete", image="example.test/api:v1")

    def deployment(self):
        return next(obj for obj in self.objects if obj["kind"] == "Deployment")

    def set_rollout(self, state, image="example.test/api:v2"):
        deployment = self.deployment()
        deployment["metadata"]["uid"] = DEPLOYMENT_UID
        deployment["spec"]["template"] = template(image, defaulted=True)
        deployment["metadata"]["resourceVersion"] = "101"
        deployment["metadata"]["generation"] = 4 if state == "unobserved" else 3
        deployment["status"] = {
            "observedGeneration": 3, "replicas": 3, "updatedReplicas": 1,
            "readyReplicas": 1, "availableReplicas": 1,
            "conditions": [{"type": "Progressing", "status": "True", "reason": "ReplicaSetUpdated"}]}
        if state == "complete":
            deployment["status"].update(replicas=2, updatedReplicas=2, readyReplicas=2, availableReplicas=2)
        if state == "unobserved":
            deployment["status"]["conditions"] = [{"type": "Progressing", "status": "False",
                                                    "reason": "ProgressDeadlineExceeded",
                                                    "message": "Older generation condition, not a current verdict"}]
        self.objects = [obj for obj in self.objects if obj["kind"] not in ("ReplicaSet", "Pod")]
        for name, uid, revision, rs_image, counts in (
                ("checkout-10-old", OLD_RS_UID, "8", "example.test/api:v1", (1, 1)),
                ("checkout-20-new", CURRENT_RS_UID, "9", image, (1, 0))):
            rs = object_fixture("ReplicaSet", name, uid,
                                {"replicas": counts[0], "template": template(rs_image, defaulted=True)},
                                {"replicas": counts[0], "readyReplicas": counts[1], "availableReplicas": counts[1]},
                                ("Deployment", "checkout", DEPLOYMENT_UID))
            rs["metadata"]["annotations"] = {"deployment.kubernetes.io/revision": revision}
            rs["spec"]["template"]["metadata"]["labels"]["pod-template-hash"] = uid
            self.objects.append(rs)
            pod = object_fixture("Pod", name + "-pod", "fixture-pod-" + revision,
                                 deepcopy(rs["spec"]["template"]["spec"]), {
                                     "phase": "Running", "containerStatuses": [{"name": "api", "ready": bool(counts[1]),
                                         "restartCount": 0, "image": rs_image,
                                         "imageID": "example.test/api@sha256:" + revision * 64,
                                         "state": {"running": {"startedAt": stamp()}}}]},
                                 ("ReplicaSet", name, uid))
            self.objects.append(pod)
        self.objects.extend([
            object_fixture("ReplicaSet", "foreign-rs", "fixture-foreign-rs",
                           {"replicas": 9, "template": template("example.test/foreign:v99")},
                           {"replicas": 9, "readyReplicas": 9, "availableReplicas": 9},
                           ("Deployment", "checkout", "different-deployment-uid")),
            object_fixture("Pod", "foreign-pod", "fixture-foreign-pod", template("example.test/foreign:v99")["spec"],
                           {"phase": "Running"}, ("ReplicaSet", "foreign-rs", "fixture-foreign-rs")),
        ])
        non_controller_rs = object_fixture("ReplicaSet", "non-controller-rs", "fixture-non-controller-rs",
            {"replicas": 9, "template": template("example.test/unowned:v99")},
            {"replicas": 9}, ("Deployment", "checkout", DEPLOYMENT_UID))
        non_controller_pod = object_fixture("Pod", "non-controller-pod", "fixture-non-controller-pod",
            template("example.test/unowned:v99")["spec"], {"phase": "Running"},
            ("ReplicaSet", "checkout-20-new", CURRENT_RS_UID))
        for child in (non_controller_rs, non_controller_pod):
            child["metadata"]["ownerReferences"][0]["controller"] = False
            self.objects.append(child)

    def journal(self):
        with self.request_lock:
            return deepcopy(self.requests)

    def review_reads(self):
        return [r for r in self.journal() if r["method"] == "GET" and r["path"] in (DEPLOYMENT_PATH, RS_PATH, PODS_PATH)
                and r["query"].get("watch") != ["true"]]


def desired_documents():
    return [
        {"apiVersion": "apps/v1", "kind": "Deployment",
         "metadata": {"name": "checkout", "namespace": "apps", "labels": {"app": "checkout"}},
         "spec": {"replicas": 2, "template": template("example.test/api:v2", memory="192Mi")}},
        {"apiVersion": "apps/v1", "kind": "Deployment",
         "metadata": {"name": "outside-release", "namespace": "outside"},
         "spec": {"replicas": 1}},
        {"apiVersion": "v1", "kind": "Secret",
         "metadata": {"name": "excluded-auth", "namespace": "apps"},
         "stringData": {"token": SECRET_MARKER}},
    ]


def source_text(documents):
    return "\n---\n".join(json.dumps(doc, indent=2) for doc in documents) + "\n"


def wait_screen(terminal, required, timeout=8, excluded=()):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        current = journeys.text(terminal)
        if all(value in current for value in required) and not any(value in current for value in excluded):
            journeys.assert_screen(terminal, required, excluded)
            return current
        terminal.drain(.1)
    journeys.assert_screen(terminal, required, excluded)


def read_pages(terminal, required=(), excluded=(), max_pages=12):
    excluded = tuple(excluded) + (SECRET_MARKER,)
    terminal.keys("\x1b[H", .15)
    pages, previous = [], None
    for _ in range(max_pages):
        current = journeys.text(terminal)
        if current == previous:
            break
        pages.append(current)
        previous = current
        terminal.keys("\x1b[6~", .15)
    else:
        raise AssertionError("Retained detail exceeded the fixture's page bound")
    seen = "\n".join(pages)
    compact = "".join(c for c in seen if not c.isspace() and c != "│")
    for value in required:
        if value not in seen and "".join(c for c in value if not c.isspace()) not in compact:
            raise AssertionError(f"Retained detail missing {value!r}:\n{seen}")
    for value in excluded:
        if value in seen or "".join(c for c in value if not c.isspace()) in compact:
            raise AssertionError(f"Retained detail disclosed {value!r}:\n{seen}")
    terminal.keys("\x1b[H", .1)
    return seen


def captured_time(screen):
    match = re.search(r"captured (\d{2}:\d{2}:\d{2}Z)", screen)
    if not match:
        raise AssertionError("Rollout capture timestamp missing:\n" + screen)
    return match.group(1)


def audit_requests(api):
    journal = api.journal()
    for request in journal:
        method, path = request["method"], request["path"]
        if method != "GET" and not (method == "POST" and "selfsubjectaccessreviews" in path):
            raise AssertionError("Read-only journey mutated or submitted a resource: " + repr(request))
        if "/secrets" in path or "/namespaces/outside/" in path:
            raise AssertionError("Excluded/out-of-scope manifest caused a read: " + repr(request))
        if request["stage"].startswith("rollout-") and path in (RS_PATH, PODS_PATH) and request["query"].get("watch") != ["true"]:
            if request["query"].get("labelSelector") != ["app=checkout"] or request["query"].get("limit") != ["200"]:
                raise AssertionError("Rollout list broadened or lost its pagination limit: " + repr(request))
            if request["stage"] in ("rollout-denied-refresh", "rollout-stale-refresh"):
                raise AssertionError("Failed Deployment continuity caused descendant reads: " + repr(request))
    if not any(request["path"] == DEPLOYMENT_PATH for request in journal):
        raise AssertionError("Review never read the intended named Deployment")
    return journal


def run_width(binary, output, columns, rows):
    demo.COLS, demo.ROWS = columns, rows
    api = ChangeReleaseAPI()
    threading.Thread(target=api.serve_forever, daemon=True).start()
    terminal = None
    captures, checks = [], []
    prefix = f"{columns}x{rows}"

    def capture(name):
        filename = f"{prefix}-{name}"
        journeys.assert_screen(terminal, (), (SECRET_MARKER,))
        journeys.capture(terminal, output, filename)
        record = {"text": filename + ".txt", "columns": columns, "rows": rows,
                  "source": "actual PTY terminal cells; disposable localhost fixture"}
        pages_path = output / (filename + ".pages.txt")
        if pages_path.exists():
            record["pages"] = pages_path.name
        if name in GUIDE_IMAGES or name == "failure-screen":
            record["file"] = filename + ".png"
        else:
            (output / (filename + ".png")).unlink()
        captures.append(record)

    def detail(name, required=(), excluded=(), capture_required=(), max_pages=12):
        seen = read_pages(terminal, required, excluded, max_pages)
        (output / (f"{prefix}-{name}.pages.txt")).write_text(seen + "\n")
        if capture_required:
            for _ in range(64):
                if all(value in journeys.text(terminal) for value in capture_required):
                    break
                terminal.keys("\x1b[B", .03)
            else:
                raise AssertionError("Unable to position the emitted detail at its declared changes")
        capture(name)
        return seen

    try:
        with tempfile.TemporaryDirectory(prefix="kr-") as directory:
            root = Path(directory)
            manifest_path = root / "release review.yaml"
            original_source = source_text(desired_documents())
            original_hash = hashlib.sha256(original_source.encode()).hexdigest()
            manifest_path.write_text(original_source)
            terminal = demo.Terminal(binary, directory, api.server_port, command="deployments apps", flags=["--readonly"],
                                     ui_config="    noIcons: true\n")
            terminal.drain(4)
            wait_screen(terminal, ["checkout", "demo-dev", "ns:apps"])
            api.stage = "desired-initial"
            terminal.command("review " + str(manifest_path))
            wait_screen(terminal, ["Desired-state review", "checkout", "out of scope", "excluded", original_hash[:12]])
            capture("desired-resource-table")
            terminal.keys("\r", .4)
            detail("desired-authored-detail", ["RETAINED SOURCE", original_hash, "DECLARED INTENT", "example.test/api:v1", "example.test/api:v2",
                                                "128Mi", "192Mi", "Live-only fields and omitted resources",
                                                "Flux tracking marker"],
                   [SECRET_MARKER, "REMOVE /spec", "dnsPolicy", "terminationGracePeriodSeconds"],
                   ["DECLARED INTENT", "example.test/api:v2", "192Mi"])
            terminal.keys("\x1b", .2)
            checks.append("authored image/resource changes are visible; omitted defaults are not deletion proposals")

            # Rewriting the file is intentionally insufficient: r keeps the
            # selected source observation and hash rather than silently reloading.
            changed_documents = desired_documents()
            changed_documents[0]["spec"]["template"]["spec"]["containers"][0]["image"] = "example.test/api:v999"
            manifest_path.write_text(source_text(changed_documents))
            api.stage = "desired-fixed-source-refresh"
            terminal.keys("r", .5)
            wait_screen(terminal, [original_hash[:12], "checkout"])
            terminal.keys("\r", .3)
            detail("desired-fixed-source", ["example.test/api:v2"], ["example.test/api:v999", SECRET_MARKER])
            terminal.keys("\x1b", .2)
            checks.append("explicit live refresh retains the original authored source and SHA256 after file changes")

            api.stage = "desired-denied-refresh"
            api.deployment_denied = True
            terminal.keys("r", .5)
            wait_screen(terminal, ["retained", original_hash[:12]])
            capture("desired-denied-retained")
            terminal.keys("\r", .3)
            detail("desired-denied-detail", ["RETAINED EVIDENCE", "denied", "example.test/api:v2", DEPLOYMENT_UID], [SECRET_MARKER])
            terminal.keys("\x1b", .2)
            api.deployment_denied = False
            api.stage = "desired-stale-refresh"
            api.deployment()["metadata"]["uid"] = "fixture-recreated-deployment"
            terminal.keys("r", .5)
            wait_screen(terminal, ["retained", original_hash[:12]])
            capture("desired-stale-retained")
            terminal.keys("\r", .3)
            detail("desired-stale-detail", ["RETAINED EVIDENCE", DEPLOYMENT_UID, "example.test/api:v2"],
                   ["fixture-recreated-deployment", SECRET_MARKER])
            terminal.keys("\x1b", .2)
            checks.append("denied and replacement-UID refreshes retain prior evidence without claiming a current match")
            terminal.keys("\x1b", .3)

            api.set_rollout("progressing")
            api.stage = "rollout-initial"
            terminal.command("rollout")
            wait_screen(terminal, ["Rollout review", "PROGRESSING", "checkout", "Generation"])
            before_fail_time = captured_time(journeys.text(terminal))
            capture("rollout-progress-overview")
            terminal.keys("2", .3)
            detail("rollout-owned-revisions", ["REPLICA SET REVISIONS", "checkout-10-old", "checkout-20-new"],
                   ["foreign-rs", "fixture-foreign-rs", "non-controller-rs"])
            terminal.keys("3", .3)
            detail("rollout-owned-pods", ["POD IMAGE EVIDENCE", "checkout-10-old-pod", "checkout-20-new-pod"],
                   ["foreign-pod", "foreign:v99", "non-controller-pod", "unowned:v99"])
            checks.append("same-label foreign and non-controlling ReplicaSets and Pods are excluded by actual controlling-owner UIDs")
            before_evidence = len(api.journal())
            terminal.keys("5", .2)
            detail("rollout-source-evidence", ["ROLLOUT SOURCE EVIDENCE", DEPLOYMENT_UID, "ACCESS_TOKEN", "[REDACTED]"],
                   ["foreign-rs", "foreign-pod", "non-controller-rs", "non-controller-pod"], max_pages=64)
            if len(api.journal()) != before_evidence:
                raise AssertionError("Opening retained source evidence fetched fresh review resources")
            checks.append("synthetic workload credential values are redacted in retained source evidence and all captured text")

            api.stage = "rollout-retained-preview"
            before_preview = len(api.journal())
            terminal.keys("2", .2)
            terminal.keys("j", .2)
            if not re.search(r">\s+9\s+checkout-20-new", journeys.text(terminal)):
                raise AssertionError("Revision navigation did not select the exact second retained ReplicaSet")
            terminal.keys("k", .2)
            if not re.search(r">\s+8\s+checkout-10-old", journeys.text(terminal)):
                raise AssertionError("Revision navigation did not return to the exact historical ReplicaSet")
            # Change the server after collection. Historical template review
            # must remain an explicit retained selection and make no new read.
            old = next(obj for obj in api.objects if obj["kind"] == "ReplicaSet" and obj["metadata"]["uid"] == OLD_RS_UID)
            old["spec"]["template"]["spec"]["containers"][0]["image"] = "example.test/api:v777"
            terminal.keys("\r", .3)
            detail("rollout-historical-preview", ["RECOVERY CANDIDATE", "NOT EXECUTED", "checkout-10-old", OLD_RS_UID,
                                                   "example.test/api:v1", "example.test/api:v2"],
                   ["example.test/api:v777", "foreign-rs"])
            if len(api.journal()) != before_preview:
                raise AssertionError("Historical template selection fetched fresh review resources")
            checks.append("historical RS chosen by retained UID; preview keeps original template and issues no additional resource reads")

            api.stage = "rollout-denied-refresh"
            api.deployment_denied = True
            terminal.keys("r", .5)
            wait_screen(terminal, ["Refresh failed", "source/time retained"])
            if captured_time(journeys.text(terminal)) != before_fail_time:
                raise AssertionError("Failed rollout refresh replaced retained capture time")
            capture("rollout-denied-retained")
            api.deployment_denied = False
            api.stage = "rollout-stale-refresh"
            api.deployment()["metadata"]["uid"] = "fixture-recreated-deployment"
            terminal.keys("r", .5)
            wait_screen(terminal, ["Refresh failed", "source/time retained"])
            if captured_time(journeys.text(terminal)) != before_fail_time:
                raise AssertionError("Recreated Deployment replaced the pinned rollout observation")
            capture("rollout-stale-retained")
            checks.append("denied/recreated Deployment refreshes keep captured UID, time and selected retained recovery evidence")

            api.stage = "rollout-unobserved-generation"
            api.set_rollout("unobserved")
            terminal.keys("1", .2)
            terminal.keys("r", .5)
            detail("rollout-awaiting-generation", ["PROGRESSING", "Generation 4", "controller observed 3"], ["[!] BLOCKED"])
            checks.append("an older-generation Progressing=False condition does not become a current blocked verdict")
            api.stage = "rollout-complete"
            api.set_rollout("complete")
            terminal.keys("r", .5)
            wait_screen(terminal, ["COMPLETE", "checkout"])
            capture("rollout-complete-overview")
            checks.append("explicit refresh reports converged Deployment counts separately from child evidence coverage")
            journal = audit_requests(api)
            for text_file in output.glob(prefix + "-*.txt"):
                if SECRET_MARKER in text_file.read_text():
                    raise AssertionError("Persisted terminal text disclosed the synthetic credential marker: " + text_file.name)
            return {"terminal": {"columns": columns, "rows": rows}, "checks": checks,
                    "captures": captures, "request_journal": f"{prefix}-request-journal.json",
                    "original_source_sha256": original_hash,
                    "resource_read_count": len(api.review_reads()), "request_count": len(journal)}
    except Exception as error:
        if terminal is not None and terminal.process.poll() is None:
            capture("failure-screen")
        (output / (f"{prefix}-failure.json")).write_text(json.dumps({"error": str(error), "stage": api.stage}, indent=2) + "\n")
        raise
    finally:
        (output / (f"{prefix}-request-journal.json")).write_text(json.dumps(api.journal(), indent=2) + "\n")
        if terminal is not None:
            terminal.close()
        api.stopped.set()
        api.shutdown()
        api.server_close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--width", choices=(80, 120), type=int, help="Run one width while debugging; default verifies both")
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    binary_hash = hashlib.sha256(args.binary.read_bytes()).hexdigest()
    script_hash = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
    started = time.monotonic()
    widths = (args.width,) if args.width else (80, 120)
    results = [run_width(args.binary.resolve(), args.output.resolve(), columns, 24 if columns == 80 else 34) for columns in widths]
    if hashlib.sha256(args.binary.read_bytes()).hexdigest() != binary_hash:
        raise AssertionError("The capture binary changed during the journey; rerun against one fixed build")
    if hashlib.sha256(Path(__file__).read_bytes()).hexdigest() != script_hash:
        raise AssertionError("The capture script changed during the journey; rerun against one fixed script")
    manifest = {"result": "passed", "source": "actual PTY terminal cells; disposable localhost fixtures",
                "binary": str(args.binary.resolve()), "binary_sha256": binary_hash,
                "script": "scripts/change-release-journeys.py", "script_sha256": script_hash,
                "captured_at": stamp(), "elapsed_seconds": round(time.monotonic() - started, 3),
                "limitations": "Synthetic read-only evidence; no live-cluster or operator-study validation; no workload writes submitted",
                "runs": results}
    (args.output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(json.dumps({"result": "passed", "terminals": [run["terminal"] for run in results],
                      "workflow_checks": sum(len(run["checks"]) for run in results),
                      "request_counts": [run["request_count"] for run in results],
                      "binary_sha256": binary_hash, "manifest": str(args.output.resolve() / "manifest.json")},
                     indent=2), flush=True)


if __name__ == "__main__":
    main()

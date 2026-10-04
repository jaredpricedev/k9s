#!/usr/bin/env python3
"""Run a manual read-only usability session against synthetic loopback data.

Requires Python 3. This starts the actual local API handler, adds named study
objects, and launches the application in an isolated temporary runtime. No
cluster or cloud credentials are used. See docs/operator-task-study-2026-10-04.md.
"""

import argparse
from copy import deepcopy
from datetime import datetime, timezone
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import types
from urllib.error import HTTPError
from urllib.request import Request, urlopen


HERE = Path(__file__).resolve().parent
# Importing the journey module also imports its PTY rendering libraries, but
# this helper reuses only its API handlers and never invokes its renderer.
for dependency in ("pyte", "PIL", "PIL.Image", "PIL.ImageDraw", "PIL.ImageFont"):
    try:
        __import__(dependency)
    except ModuleNotFoundError:
        if dependency == "PIL":
            package = types.ModuleType("PIL")
            package.__path__ = []
            sys.modules["PIL"] = package
        elif dependency.startswith("PIL."):
            module = types.ModuleType(dependency)
            sys.modules[dependency] = module
            setattr(sys.modules["PIL"], dependency.rsplit(".", 1)[1], module)
        else:
            sys.modules[dependency] = types.ModuleType(dependency)
spec = importlib.util.spec_from_file_location("daily_workspace_journeys", HERE / "daily-workspace-journeys.py")
daily = importlib.util.module_from_spec(spec)
spec.loader.exec_module(daily)
demo = daily.journeys.demo


class StudyHandler(daily.WorkspaceHandler):
    """Add explicit missing-coverage states while retaining existing guards."""

    def do_GET(self):
        if self.path.startswith("/apis/metrics.k8s.io/"):
            self.record("GET")
            return self.failure(403, "Forbidden", "Synthetic fixture denies metrics API reads")
        if self.path.split("?", 1)[0] == "/api/v1/namespaces/ops/events":
            self.record("GET")
            return self.failure(403, "Forbidden", "Synthetic fixture denies ops event reads")
        return super().do_GET()


def study_objects(api):
    now = datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")

    def obj(api_version, kind, name, namespace, labels, spec=None, status=None):
        return {"apiVersion": api_version, "kind": kind, "metadata": {
            "name": name, "namespace": namespace, "uid": "study-" + kind.lower() + "-" + name,
            "resourceVersion": "100", "generation": 2, "creationTimestamp": now, "labels": labels},
            "spec": spec or {}, "status": status or {}}

    # The base fixture already contains investigation-api with current
    # CrashLoopBackOff plus a previous OOMKilled termination and retained event.
    # Add stable names for the remaining participant tasks.
    api.objects.extend([
        obj("v1", "Pod", "worker", "ops", {"app": "daily", "role": "worker"},
            {"containers": [{"name": "worker", "image": "example.test/worker:v1"}]},
            {"phase": "Pending", "conditions": [
                {"type": "PodScheduled", "status": "False", "reason": "Unschedulable",
                 "message": "0/1 nodes available: insufficient memory"},
                {"type": "Ready", "status": "False", "reason": "PodScheduled"}]}),
        obj("apps/v1", "Deployment", "checkout-api", "apps", {"app": "daily", "component": "checkout"},
            {"replicas": 2, "selector": {"matchLabels": {"app": "checkout"}},
             "template": {"metadata": {"labels": {"app": "checkout"}},
                          "spec": {"containers": [{"name": "api", "image": "example.test/checkout:v2"}]}}},
            {"observedGeneration": 1, "replicas": 2, "updatedReplicas": 1,
             "readyReplicas": 1, "availableReplicas": 1, "unavailableReplicas": 1,
             "conditions": [{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded",
                             "message": "Synthetic rollout has not completed"}]}),
        obj("v1", "Service", "checkout-api", "apps", {"app": "daily", "component": "checkout"},
            {"type": "ClusterIP", "clusterIP": "10.0.0.20", "selector": {"app": "checkout"},
             "ports": [{"name": "http", "port": 8080, "targetPort": 8080}]}),
        obj("v1", "Pod", "checkout-api-0", "apps", {"app": "checkout", "component": "checkout"},
            {"containers": [{"name": "api", "image": "example.test/checkout:v2"}]},
            {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}],
             "containerStatuses": [{"name": "api", "ready": True, "restartCount": 0,
                                   "state": {"running": {"startedAt": now}}}]}),
    ])
    # Services are part of the existing core-v1 discovery in the test helpers.
    if not any(name == "services" for name, _, _ in demo.GROUPS["v1"]):
        demo.GROUPS["v1"].append(("services", "Service", True))
    if "services" not in api.workspace_resources:
        api.workspace_resources.add("services")
    api.RequestHandlerClass = StudyHandler


def create_kubeconfig(path, port):
    path.write_text(f"""apiVersion: v1
kind: Config
clusters:
- name: study-loopback
  cluster:
    server: http://127.0.0.1:{port}
contexts:
- name: study-dev
  context:
    cluster: study-loopback
    user: study-user
    namespace: apps
current-context: study-dev
users:
- name: study-user
  user: {{}}
""")
    path.chmod(0o600)


def isolated_environment(root, kubeconfig):
    env = {k: v for k, v in os.environ.items() if not k.startswith((
        "K9PLUS_", "K9S_", "KUBECONFIG", "KUBECACHEDIR", "XDG_", "AWS_", "AZURE_", "GOOGLE_"))
           and "TOKEN" not in k.upper() and "PASSWORD" not in k.upper() and "CREDENTIAL" not in k.upper()}
    for name in ("config", "data", "state", "cache", "logs"):
        (root / name / "k9plus").mkdir(parents=True, exist_ok=True)
    env.update({"KUBECONFIG": str(kubeconfig), "KUBECACHEDIR": str(root / "kube-cache"),
                "K9PLUS_CONFIG_DIR": str(root / "config" / "k9plus"),
                "K9PLUS_LOGS_DIR": str(root / "logs" / "k9plus"),
                "XDG_CONFIG_HOME": str(root / "config"), "XDG_DATA_HOME": str(root / "data"),
                "XDG_STATE_HOME": str(root / "state"), "XDG_CACHE_HOME": str(root / "cache"),
                "K9PLUS_NO_UPDATE_CHECK": "1"})
    return env


def named_get(base, path):
    with urlopen(base + path, timeout=2) as response:
        return json.loads(response.read())


def smoke():
    api = daily.WorkspaceAPI()
    study_objects(api)
    thread = threading.Thread(target=api.serve_forever, daemon=True)
    thread.start()
    base = f"http://127.0.0.1:{api.server_port}"
    checks = []
    try:
        for path, expected_kind in (
            ("/api/v1/namespaces/apps/pods/investigation-api", "Pod"),
            ("/api/v1/namespaces/ops/pods/worker", "Pod"),
            ("/apis/apps/v1/namespaces/apps/deployments/checkout-api", "Deployment"),
            ("/api/v1/namespaces/apps/services/checkout-api", "Service"),
        ):
            value = named_get(base, path)
            assert value["kind"] == expected_kind, (path, value.get("kind"))
            checks.append("GET " + path)
        for path in ("/apis/metrics.k8s.io/v1beta1/namespaces/apps/pods",
                     "/api/v1/namespaces/ops/events"):
            try:
                named_get(base, path)
                raise AssertionError("fixture did not preserve the configured evidence gap: " + path)
            except HTTPError as error:
                assert error.code == 403, (path, error.code)
                checks.append("GET " + path + " returned synthetic HTTP 403")
        worker = named_get(base, "/api/v1/namespaces/ops/pods/worker")
        condition = next(c for c in worker["status"]["conditions"] if c["type"] == "PodScheduled")
        assert condition["status"] == "False" and condition["reason"] == "Unschedulable"
        investigation = named_get(base, "/api/v1/namespaces/apps/pods/investigation-api")
        api_status = next(s for s in investigation["status"]["containerStatuses"] if s["name"] == "api")
        assert api_status["state"]["waiting"]["reason"] == "CrashLoopBackOff"
        assert api_status["lastState"]["terminated"]["reason"] == "OOMKilled"
        deployment = named_get(base, "/apis/apps/v1/namespaces/apps/deployments/checkout-api")
        assert deployment["status"]["observedGeneration"] < deployment["metadata"]["generation"]
        assert deployment["status"]["readyReplicas"] < deployment["spec"]["replicas"]
        service = named_get(base, "/api/v1/namespaces/apps/services/checkout-api")
        pod = named_get(base, "/api/v1/namespaces/apps/pods/checkout-api-0")
        assert all(pod["metadata"]["labels"].get(key) == value
                   for key, value in service["spec"]["selector"].items())
        assert service["spec"]["type"] == "ClusterIP" and service["spec"]["clusterIP"] == "10.0.0.20"
        auth = Request(base + "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews",
                       data=b"{}", method="POST", headers={"Content-Type": "application/json"})
        with urlopen(auth, timeout=2) as response:
            review = json.loads(response.read())
            assert response.status == 200 and review["status"]["allowed"] is True
            checks.append("synthetic SelfSubjectAccessReview POST allowed")
        for method in ("POST", "PATCH", "PUT", "DELETE"):
            path = ("/apis/apps/v1/namespaces/apps/deployments" if method == "POST" else
                    "/apis/apps/v1/namespaces/apps/deployments/checkout-api")
            request = Request(base + path,
                              data=b"{}", method=method, headers={"Content-Type": "application/json"})
            try:
                urlopen(request, timeout=2)
                raise AssertionError(f"fixture accepted {method}")
            except HTTPError as error:
                assert error.code == 405, (method, error.code)
                checks.append(method + " rejected with HTTP 405")
        return checks
    finally:
        api.shutdown()
        api.server_close()
        thread.join(timeout=2)
        assert not thread.is_alive(), "fixture server did not stop"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, help="application binary for the interactive session")
    parser.add_argument("--journal", type=Path, help="new private JSON request journal path (never overwritten)")
    parser.add_argument("--smoke", action="store_true", help="check named objects, mutation rejection and server cleanup")
    parser.add_argument("app_args", nargs=argparse.REMAINDER, help="optional application arguments after --")
    args = parser.parse_args()
    if args.smoke:
        print(json.dumps({"checks": smoke(), "result": "passed", "scope": "synthetic loopback API only"}, indent=2))
        return 0
    if not args.binary or not args.journal:
        parser.error("interactive mode requires --binary and --journal; use --smoke for bounded fixture verification")
    binary = args.binary.resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        parser.error("--binary must name an executable file")
    journal = args.journal.expanduser().resolve()
    if journal.exists():
        parser.error("--journal already exists; choose a new path")

    api = daily.WorkspaceAPI()
    study_objects(api)
    thread = threading.Thread(target=api.serve_forever, daemon=True)
    thread.start()
    child = None
    try:
        with tempfile.TemporaryDirectory(prefix="k9plus-operator-study-") as directory:
            root = Path(directory)
            kubeconfig = root / "kubeconfig"
            create_kubeconfig(kubeconfig, api.server_port)
            env = isolated_environment(root, kubeconfig)
            (root / "config" / "k9plus" / "config.yaml").write_text(
                "k9s:\n  skipLatestRevCheck: true\n  refreshRate: 2\n  ui:\n    splashless: true\n")
            command = [str(binary), "--kubeconfig", str(kubeconfig), "--readonly"]
            if args.app_args:
                command.extend(args.app_args[1:] if args.app_args[0] == "--" else args.app_args)
            print(f"Fixture API: http://127.0.0.1:{api.server_port} (synthetic data only)", flush=True)
            print("Context: study-dev; default namespace: apps; app is in read-only mode.", flush=True)
            print("Temporary kubeconfig and app state are removed when the app exits.", flush=True)
            child = subprocess.Popen(command, env=env)
            child.wait()
        return child.returncode
    except KeyboardInterrupt:
        if child is not None and child.poll() is None:
            child.send_signal(signal.SIGINT)
            try:
                child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                child.terminate()
                child.wait(timeout=3)
        return 130
    finally:
        if child is not None and child.poll() is None:
            child.terminate()
            try:
                child.wait(timeout=3)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=3)
        api.shutdown()
        api.server_close()
        thread.join(timeout=2)
        entries = deepcopy(api.requests)
        if not journal.exists():
            journal.parent.mkdir(parents=True, exist_ok=True)
            with journal.open("x", encoding="utf-8") as stream:
                json.dump({"fixture": "operator-study-loopback", "context": "study-dev",
                           "captured_at": datetime.now(timezone.utc).isoformat(), "requests": entries},
                          stream, indent=2)
                stream.write("\n")
            journal.chmod(0o600)
            rejected = [r for r in entries if r["method"] in ("PATCH", "PUT", "DELETE") or
                        r["method"] == "POST" and "selfsubjectaccessreviews" not in r["path"]]
            print(f"Request journal written with mode 0600: {journal}; requests={len(entries)}; "
                  f"resource mutation attempts={len(rejected)}", flush=True)


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
# Modified for k9+; see NOTICE.
"""Exercise actual piped task output against an isolated disposable API."""
import argparse
from datetime import datetime, timezone
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time
from urllib.parse import parse_qs, urlparse


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    binary = args.binary.resolve()
    args.output.mkdir(parents=True, exist_ok=True)
    journal, checks = [], []
    now = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
    pod = {"apiVersion": "v1", "kind": "Pod", "metadata": {
        "name": "api", "namespace": "apps", "uid": "linear-pod-uid", "resourceVersion": "100"},
        "spec": {"containers": [{"name": "app", "env": [{"name": "ACCESS_TOKEN", "value": "fixture-secret-excluded"}]}]},
        "status": {"phase": "Running", "containerStatuses": [{"name": "app", "ready": False,
            "restartCount": 7, "state": {"waiting": {"reason": "CrashLoopBackOff"}},
            "lastState": {"terminated": {"reason": "OOMKilled", "exitCode": 137}}}]}}

    class Handler(BaseHTTPRequestHandler):
        mode = "complete"

        def log_message(self, *_args):
            pass

        def do_GET(self):
            parsed = urlparse(self.path)
            journal.append({"method": "GET", "path": parsed.path, "query": parse_qs(parsed.query), "mode": self.mode})
            if self.mode == "slow":
                time.sleep(2)
            code = 200
            if self.mode == "denied" or self.mode == "events-denied" and parsed.path.endswith("/events"):
                code, body = 403, {"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "Forbidden", "code": 403}
            elif parsed.path == "/api/v1/namespaces/apps/pods":
                body = {"apiVersion": "v1", "kind": "PodList", "metadata": {"resourceVersion": "100"}, "items": [pod]}
            elif parsed.path == "/api/v1/namespaces/apps/pods/api":
                body = pod
            elif parsed.path == "/api/v1/namespaces/apps/events":
                body = {"apiVersion": "v1", "kind": "EventList", "metadata": {"resourceVersion": "100"}, "items": []}
            else:
                code, body = 404, {"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "NotFound", "code": 404}
            encoded = json.dumps(body).encode()
            try:
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)
            except (BrokenPipeError, ConnectionResetError):
                pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="k9plus-linear-") as directory:
            root = Path(directory)
            config = root / "kubeconfig.json"
            config.write_text(json.dumps({"apiVersion": "v1", "kind": "Config", "current-context": "other",
                "clusters": [{"name": "fixture", "cluster": {"server": f"http://127.0.0.1:{server.server_port}"}}],
                "contexts": [{"name": "demo", "context": {"cluster": "fixture", "user": "fixture", "namespace": "apps"}}],
                "users": [{"name": "fixture", "user": {}}]}))
            bundle = root / "evidence.json"
            bundle.write_text(json.dumps({"version": 1, "created_at": now, "observations": [{
                "identity": {"context": "retained", "gvr": "v1/pods", "namespace": "apps", "name": "api", "uid": "retained-uid"},
                "source": "retained fixture API", "observedAt": now, "state": "denied", "reason": "Read denied"}],
                "notes": ["Bearer fixture-note-token\u001b[2J"], "limits": ["Offline selected evidence"]}))
            environment = dict(os.environ, TERM="dumb", NO_COLOR="1", KUBECONFIG=str(config), K9PLUS_CONFIG_DIR=str(root / "config"))

            def run(name, argv, expected, structured=True):
                started = time.monotonic()
                result = subprocess.run([str(binary), "task", *argv], input=b"", capture_output=True, env=environment, timeout=8)
                elapsed = time.monotonic() - started
                assert result.returncode == expected, (name, result.returncode, result.stderr.decode())
                assert b"\x1b" not in result.stdout + result.stderr, name
                assert b"fixture-secret-excluded" not in result.stdout, name
                assert b"fixture-note-token" not in result.stdout, name
                (args.output / f"{name}.stdout").write_bytes(result.stdout)
                (args.output / f"{name}.stderr").write_bytes(result.stderr)
                report = json.loads(result.stdout) if structured else None
                checks.append({"name": name, "exit": result.returncode, "elapsedSeconds": round(elapsed, 4), "stdoutBytes": len(result.stdout)})
                return report

            base = ["--context", "demo", "--namespace", "apps", "--output", "json"]
            complete = run("workspace-complete", ["workspace", "--kinds", "pods", *base], 0)
            assert complete["complete"] and complete["context"] == "demo"
            assert complete["facts"][0]["resource"]["uid"] == "linear-pod-uid"
            inv = run("investigation-current-prior", ["investigate", "pods", "api", *base], 0)
            assert [f["state"] for f in inv["facts"]][:2] == ["CURRENT CrashLoopBackOff", "PREVIOUS OOMKilled"]
            assert journal[-1]["query"]["fieldSelector"] == ["involvedObject.uid=linear-pod-uid"]
            Handler.mode = "events-denied"
            denied = run("investigation-denied-events", ["investigate", "pods", "api", *base], 2)
            assert not denied["complete"] and any(c["state"] == "denied" for c in denied["coverage"])
            Handler.mode = "denied"
            denied = run("workspace-denied", ["workspace", "--kinds", "pods", *base], 2)
            assert not denied["complete"]
            partial = run("workspace-allowed-partial", ["workspace", "--kinds", "pods", *base, "--allow-partial"], 0)
            assert not partial["complete"]
            Handler.mode = "slow"
            canceled = run("workspace-deadline", ["workspace", "--kinds", "pods", *base, "--deadline", "1s"], 2)
            assert not canceled["complete"] and checks[-1]["elapsedSeconds"] < 3
            before = len(journal)
            run("secret-kind-rejected", ["investigate", "secrets", "tls", *base], 1, structured=False)
            run("explicit-scope-required", ["workspace", "--kinds", "pods"], 1, structured=False)
            offline = run("offline-denied-evidence", ["evidence", str(bundle), "--output", "json"], 2)
            assert offline["context"] == "retained" and not offline["complete"]
            run("offline-text", ["evidence", str(bundle), "--allow-partial"], 0, structured=False)
            run("invalid-format", ["evidence", str(bundle), "--output", "ansi"], 1, structured=False)
            assert len(journal) == before, "offline/invalid/Secret tasks must not make API requests"
            assert all(row["method"] == "GET" and row["path"].startswith("/api/v1/namespaces/apps/") for row in journal)
            assert not any("secret" in row["path"] for row in journal)
    finally:
        server.shutdown()
        server.server_close()
    metadata = {"binary": str(binary), "sha256": hashlib.sha256(binary.read_bytes()).hexdigest(), "checks": checks,
        "requests": journal, "limits": ["Controlled local API and actual non-TTY subprocesses; no real-cluster or screen-reader operator study"]}
    (args.output / "manifest.json").write_text(json.dumps(metadata, indent=2) + "\n")
    print(json.dumps({"passed": len(checks), "output": str(args.output)}))


if __name__ == "__main__":
    main()

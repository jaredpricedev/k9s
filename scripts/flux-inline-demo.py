#!/usr/bin/env python3
# Modified for k9+; see NOTICE.
"""Exercise and film native Flux reconciliation in a real k9+ PTY.

  python scripts/flux-inline-demo.py --binary /tmp/k9plus-demo
Use --app k9s for a pre-rebrand fork. New captures default to assets/k9plus/flux-inline.

Requires Python 3, pyte, Pillow, and ffmpeg (unless --no-video). The disposable
localhost API contains 10,000 HelmReleases. Its PATCH response and fake controller
transitions are held until explicit test signals. This tests UI responsiveness,
native key precedence, and informer updates; it does not measure Flux throughput.
Only actual terminal cells are rasterized. Video titles describe the fixture;
the captured terminal is replayed continuously at its original pace.
"""

import argparse
import codecs
import copy
import fcntl
import gzip
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import pty
import queue
import re
import shutil
import struct
import subprocess
import tempfile
import termios
import threading
import time
from datetime import datetime, timezone
from http.server import ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

import pyte
from PIL import Image, ImageDraw, ImageFont

SPEC = importlib.util.spec_from_file_location("perf_demo", Path(__file__).with_name("perf-demo.py"))
PERF = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PERF)
DEMO = PERF.DEMO
API_VERSION = "helm.toolkit.fluxcd.io/v2"
RESOURCE = "helmreleases"
REQUESTED = "reconcile.fluxcd.io/requestedAt"
NAME = re.compile(r"release-\d{5}")
DEMO.GROUPS = {
    "v1": [("namespaces", "Namespace", False), ("nodes", "Node", False), ("pods", "Pod", True)],
    API_VERSION: [(RESOURCE, "HelmRelease", True)],
    "authorization.k8s.io/v1": [("selfsubjectaccessreviews", "SelfSubjectAccessReview", False)],
}


def condition(kind="Ready", status="True", reason="Succeeded", message="Synthetic Helm upgrade succeeded"):
    return {"type": kind, "status": status, "reason": reason, "message": message,
            "observedGeneration": 1, "lastTransitionTime": datetime.now(timezone.utc).isoformat()}


def fixtures(count):
    meta = {"resourceVersion": "100", "generation": 1, "creationTimestamp": "2026-09-01T00:00:00Z"}
    items = [{"apiVersion": "v1", "kind": "Namespace", "metadata": dict(meta, name="default", uid="demo-default"),
              "status": {"phase": "Active"}}]
    for index in range(count):
        name = f"release-{index:05d}"
        items.append({"apiVersion": API_VERSION, "kind": "HelmRelease",
                      "metadata": dict(meta, name=name, namespace="default", uid=name),
                      "spec": {"interval": "5m", "chartRef": {"kind": "OCIRepository", "name": "apps"}},
                      "status": {"conditions": [condition()], "observedGeneration": 1,
                                 "lastAppliedRevision": "1.0.0", "lastHandledReconcileAt": "previous-request"}})
    return items


class InlineAPI(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, count):
        super().__init__(("127.0.0.1", 0), InlineHandler)
        self.origin = time.monotonic()
        self.objects = fixtures(count)
        self.index = {o["metadata"]["name"]: o for o in self.objects if o["kind"] == "HelmRelease"}
        self.requests, self.actions, self.watchers, self.history = [], [], [], []
        self.lock = threading.RLock()
        self.stopped = threading.Event()
        self.rv = 100

    def record(self, method, path, **extra):
        with self.lock:
            self.requests.append(dict(time=time.monotonic() - self.origin, method=method, path=path, **extra))

    def modified(self, obj):
        # Caller holds lock; copies ensure a slow watch cannot see future states.
        self.rv += 1
        obj["metadata"]["resourceVersion"] = str(self.rv)
        event = {"type": "MODIFIED", "object": copy.deepcopy(obj)}
        self.history.append(event)
        for subscriber in self.watchers:
            subscriber.put(event)

    def progress(self, action):
        with self.lock:
            obj = self.index[action["name"]]
            obj["status"]["conditions"] = [condition("Reconciling", reason="Progressing",
                                                     message="Scripted controller is performing a Helm upgrade")]
            action["controller_started"] = time.monotonic() - self.origin
            self.modified(obj)

    def finish(self, action, success):
        with self.lock:
            obj = self.index[action["name"]]
            obj["status"]["lastHandledReconcileAt"] = action["token"]
            obj["status"]["conditions"] = [condition() if success else condition(
                status="False", reason="UpgradeFailed", message="Scripted controller: deployment did not become ready")]
            action["controller_finished"] = time.monotonic() - self.origin
            action["outcome"] = "Ready" if success else "Failed"
            self.modified(obj)


class InlineHandler(DEMO.APIHandler):
    def do_POST(self):
        self.server.record("POST", self.path)
        super().do_POST()

    def do_GET(self):
        parsed = urlparse(self.path)
        path, query = parsed.path.rstrip("/"), parse_qs(parsed.query)
        parts = path.split("/")
        if RESOURCE not in parts:
            self.server.record("GET", self.path, operation="discovery-or-other")
            return super().do_GET()
        resource_index = parts.index(RESOURCE)
        name = parts[resource_index + 1] if len(parts) > resource_index + 1 else None
        operation = "get" if name else "watch" if query.get("watch") == ["true"] else "list"
        self.server.record("GET", self.path, operation=operation, resource=RESOURCE, name=name)
        if name:
            with self.server.lock:
                obj = copy.deepcopy(self.server.index.get(name))
            return self.reply(obj) if obj else self.send_error(404)
        if operation == "list":
            with self.server.lock:
                result = {"apiVersion": API_VERSION, "kind": "HelmReleaseList",
                          "metadata": {"resourceVersion": str(self.server.rv)},
                          "items": copy.deepcopy(list(self.server.index.values()))}
            return self.reply(result)
        subscriber = queue.Queue()
        with self.server.lock:
            self.server.watchers.append(subscriber)
            if query.get("sendInitialEvents") == ["true"]:
                initial = [{"type": "ADDED", "object": copy.deepcopy(o)} for o in self.server.index.values()]
                initial.append({"type": "BOOKMARK", "object": {"apiVersion": API_VERSION, "kind": "HelmRelease",
                    "metadata": {"resourceVersion": str(self.server.rv),
                                 "annotations": {"k8s.io/initial-events-end": "true"}}}})
            else:
                since = int(query.get("resourceVersion", ["0"])[0] or "0")
                initial = [e for e in self.server.history if int(e["object"]["metadata"]["resourceVersion"]) > since]
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        try:
            for event in initial:
                self.wfile.write(json.dumps(event).encode() + b"\n")
            self.wfile.flush()
            while not self.server.stopped.is_set():
                try:
                    event = subscriber.get(timeout=.1)
                except queue.Empty:
                    continue
                self.wfile.write(json.dumps(event).encode() + b"\n")
                self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass
        finally:
            with self.server.lock:
                self.server.watchers.remove(subscriber)

    def do_PATCH(self):
        patch = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
        path = urlparse(self.path).path
        name = path.rsplit("/", 1)[-1]
        self.server.record("PATCH", self.path, operation="patch", resource=RESOURCE, name=name,
                           content_type=self.headers.get("Content-Type"), body=patch)
        with self.server.lock:
            obj = self.server.index.get(name)
            metadata = patch.get("metadata", {})
            if obj is None or metadata.get("uid") != obj["metadata"]["uid"] or metadata.get("resourceVersion") != obj["metadata"]["resourceVersion"]:
                return self.send_error(409, "UID/resourceVersion precondition mismatch")
            token = metadata.get("annotations", {}).get(REQUESTED)
            if not token or set(patch) != {"metadata"}:
                return self.send_error(400, "Expected only a native reconcile annotation patch")
            action = {"name": name, "token": token, "patch_received": time.monotonic() - self.server.origin,
                      "release_response": threading.Event(), "response_returned": threading.Event()}
            obj["metadata"].setdefault("annotations", {})[REQUESTED] = token
            self.server.actions.append(action)
            self.server.modified(obj)
            response = copy.deepcopy(obj)
        # Deliberately block HTTP completion, independently of the informer event.
        while not action["release_response"].wait(.1):
            if self.server.stopped.is_set():
                return
        action["patch_response_released"] = time.monotonic() - self.server.origin
        try:
            self.reply(response)
        except (BrokenPipeError, ConnectionResetError):
            pass
        finally:
            action["response_returned"].set()


class Capture(PERF.Capture):
    def __init__(self, binary, directory, port, cols, rows, app="k9plus"):
        root = Path(directory)
        kubeconfig = root / "kubeconfig"
        self.app = app
        self.environment = dict(PERF.ENVIRONMENT)
        env = DEMO.isolated_runtime(root, app, kubeconfig, self.environment)
        (root / "config" / app / "config.yaml").write_text(
            "k9s:\n  skipLatestRevCheck: true\n  refreshRate: 2\n  ui:\n    splashless: true\n")
        self.sentinel = root / "unexpected-cli-invocation.txt"
        (root / "bin").mkdir()
        executable = root / "bin/flux"
        executable.write_text('#!/bin/sh\nprintf "legacy plugin invoked\\n" >> "$FLUX_SENTINEL_FILE"\nexit 99\n')
        executable.chmod(0o755)
        (root / "config" / app / "plugins.yaml").write_text("""plugins:
  legacy-reconcile-hr:
    shortCut: Shift-R
    override: true
    confirm: false
    dangerous: true
    scopes: [helmreleases]
    description: Legacy reconcile override sentinel
    command: flux
    background: false
    args: [reconcile, helmrelease, $NAME]
  verify-loaded:
    shortCut: Shift-U
    scopes: [helmreleases]
    description: Sentinel probe
    command: flux
    background: false
""")
        kubeconfig.write_text(f"""apiVersion: v1
kind: Config
clusters:
- name: scripted-local
  cluster:
    server: http://127.0.0.1:{port}
contexts:
- name: scripted-local
  context:
    cluster: scripted-local
    user: scripted-local
    namespace: default
current-context: scripted-local
users:
- name: scripted-local
  user: {{}}
""")
        env.update(FLUX_SENTINEL_FILE=str(self.sentinel), PATH=str(root / "bin") + os.pathsep + env.get("PATH", ""))
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

        def control_terminal():
            os.setsid()
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)

        self.origin = time.monotonic()
        self.process = subprocess.Popen([str(binary), "--kubeconfig", str(kubeconfig), "--context", "scripted-local",
                                         "--command", "helmreleases default"],
                                        stdin=slave, stdout=slave, stderr=slave, env=env, preexec_fn=control_terminal)
        os.close(slave)
        self.screen = pyte.Screen(cols, rows)
        self.stream = pyte.Stream(self.screen)
        self.decoder = codecs.getincrementaldecoder("utf-8")("replace")
        self.pending, self.events = "", []
        self.received = queue.Queue()
        self.stopped = threading.Event()
        self.reader = threading.Thread(target=self.read_output, daemon=True)
        self.reader.start()


def display(screen):
    return "\n".join(screen.display)


def table_matches(screen, count, query=""):
    title = re.search(r"helmreleases\(default\)\[([\d,]+)\]([^\n]*)", display(screen))
    if not title or int(title[1].replace(",", "")) != count:
        return False
    if query and f"</{query}>" not in title[2] or not query and "</" in title[2]:
        return False
    return len(NAME.findall(display(screen))) >= min(8, count)


def row_status(screen, name, status):
    return any(re.search(rf"\b{re.escape(name)}\s+{status}\b", line) for line in screen.display)


def row_style(screen, name):
    for y, line in enumerate(screen.display):
        x = line.find(name)
        if x >= 0:
            return tuple((screen.buffer[y][x + n].fg, screen.buffer[y][x + n].bg,
                          screen.buffer[y][x + n].reverse) for n in range(len(name)))
    return None


def ensure(predicate, description):
    if not predicate:
        raise AssertionError(description)


def snapshot(capture, output, name, results):
    # Record timestamps only during interaction. Rasterize offline after capture.
    results["screenshots"][name] = capture.elapsed()
    (output / (name + ".txt")).write_text("\n".join(s.rstrip() for s in capture.screen.display).rstrip() + "\n")


def wait_api(capture, predicate, timeout=10):
    until = time.monotonic() + timeout
    while time.monotonic() < until:
        if predicate():
            return
        capture.settle(.02)
    raise AssertionError("Timed out waiting for scripted API state")


def apply_filter(capture, query, count):
    start = capture.key("/" + query + "\r")
    end = capture.wait(lambda s: table_matches(s, count, query), 5)
    # Applying a filter can preserve a previous scroll offset. Explicit Home
    # makes the next interaction deterministic without reaching into the app.
    capture.key("\x1b[H")
    first = query if count == 1 else "release-00000"
    capture.wait(lambda s: any(line.startswith("│ " + first + " ") for line in s.display), 5)
    return {"query": query, "rows": count, "start": start, "end": end, "pty_response_ms": (end - start) * 1000}


def move_down(capture, previous, following):
    before = row_style(capture.screen, previous)
    next_unselected = row_style(capture.screen, following)
    # The table highlights each selected row by swapping its own severity colors.
    next_selected = tuple((bg, fg, reverse) for fg, bg, reverse in next_unselected)
    start = capture.key("j")
    end = capture.wait(lambda s: row_style(s, previous) != before and row_style(s, following) == next_selected, 5)
    return {"input": "j", "previous": previous, "selected": following, "start": start, "end": end,
            "pty_response_ms": (end - start) * 1000}


def no_duplicate(capture, api, actions, expected="already being submitted"):
    capture.key("R")
    capture.wait(lambda s: expected in display(s), 5)
    ensure("Confirm Flux action" not in display(capture.screen), "Duplicate action opened another confirmation")
    ensure(len(api.actions) == actions, "Duplicate action submitted another PATCH")


def confirm(capture, api, count):
    capture.key("R")
    capture.wait(lambda s: "Confirm Flux action" in display(s), 5)
    ensure(not capture.sentinel.exists(), "Legacy CLI plugin ran instead of native reconciliation")
    capture.key("\t\r")
    wait_api(capture, lambda: len(api.actions) == count)
    return api.actions[-1]


def capture_run(args):
    api = InlineAPI(args.count)
    api_thread = threading.Thread(target=api.serve_forever, daemon=True)
    api_thread.start()
    results = {"schema_version": 1, "runtime": args.app, "captured_at": datetime.now(timezone.utc).isoformat(),
               "binary": {"path": str(args.binary), "sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest()},
               "fixture": {"resource": "HelmRelease", "count": args.count, "namespace": "default",
                           "transport": "disposable localhost HTTP", "controller": "scripted; no Flux controller or CLI is running",
                           "delays": "PATCH response and controller updates wait for explicit harness signals"},
               "terminal": {"columns": args.cols, "rows": args.rows}, "screenshots": {}, "checks": {}, "interactions": [],
               "method": {"capture": f"real {args.app} PTY; complete tcell frames; terminal output replayed at original pace",
                          "limit": "Synthetic responsiveness smoke test, not evidence of actual Flux reconciliation speed",
                          "isolation": f"Explicit loopback kubeconfig; isolated {args.app}, XDG config/data/state/cache and KUBECACHEDIR"}}
    capture = None
    error = None
    with tempfile.TemporaryDirectory(prefix=args.app + "-flux-inline-") as directory:
        try:
            capture = Capture(args.binary, directory, api.server_port, args.cols, args.rows, args.app)
            capture.wait(lambda s: table_matches(s, args.count), args.timeout)
            ensure("Sentinel probe" in display(capture.screen), "Sentinel plugin config did not load")
            results["checks"]["sentinel_plugin_config_loaded"] = True
            results["video_start"] = capture.elapsed()
            snapshot(capture, args.output, "01-ready-10000", results)
            capture.settle(.6)
            # Enter cancels by default; cancellation must not reach the API.
            capture.key("R")
            capture.wait(lambda s: "Confirm Flux action" in display(s), 5)
            snapshot(capture, args.output, "02-confirm-native", results)
            capture.key("\r")
            capture.wait(lambda s: "Confirm Flux action" not in display(s), 5)
            ensure(not any(r.get("operation") in ["get", "patch"] for r in api.requests), "Canceled action made a GET/PATCH")
            results["checks"]["cancel_has_no_resource_requests"] = True

            first = confirm(capture, api, 1)
            ensure(first["name"] == "release-00000", "Unexpected selected initial row")
            capture.wait(lambda s: row_status(s, first["name"], "Reconciling"), 5)
            ensure("controller_started" not in first, "Controller advanced before explicit signal")
            snapshot(capture, args.output, "03-accepted-patch-held", results)
            no_duplicate(capture, api, 1)
            results["interactions"].append(dict(apply_filter(capture, "release-0000", 10), phase="PATCH response held"))
            no_duplicate(capture, api, 1)
            results["interactions"].append(dict(move_down(capture, "release-00000", "release-00001"), phase="PATCH response held"))
            ensure(not first["response_returned"].is_set(), "PATCH completed before movement/filter verification")
            results["checks"]["navigation_and_filter_while_patch_held"] = True
            results["checks"]["duplicate_reconcile_while_patch_held_suppressed"] = True
            snapshot(capture, args.output, "04-filtered-moving-patch-held", results)
            capture.settle(.6)

            first["release_response"].set()
            capture.wait(lambda s: "watch STATUS for completion" in display(s), 5)
            results["interactions"].append(dict(apply_filter(capture, "release-000", 100), phase="controller not started"))
            no_duplicate(capture, api, 1, "reconciliation already queued")
            results["checks"]["duplicate_reconcile_after_patch_ack_suppressed"] = True
            results["interactions"].append(dict(move_down(capture, "release-00000", "release-00001"), phase="controller not started"))
            ensure("controller_started" not in first, "Controller advanced before navigation/filter verification")
            ensure(row_status(capture.screen, first["name"], "Reconciling"), "Pending annotation reverted to Ready")
            results["checks"]["navigation_and_filter_while_controller_waits"] = True
            snapshot(capture, args.output, "05-controller-waiting", results)
            api.progress(first)
            capture.settle(.8)
            api.finish(first, True)
            capture.wait(lambda s: row_status(s, first["name"], "Ready"), 5)
            snapshot(capture, args.output, "06-ready-acknowledged", results)
            capture.settle(.7)

            results["interactions"].append(dict(apply_filter(capture, "release-00001", 1), phase="select second release"))
            second = confirm(capture, api, 2)
            ensure(second["name"] == "release-00001", "Unexpected selected second row")
            capture.wait(lambda s: row_status(s, second["name"], "Reconciling"), 5)
            second["release_response"].set()
            capture.wait(lambda s: "watch STATUS for completion" in display(s), 5)
            api.progress(second)
            capture.settle(.8)
            api.finish(second, False)
            capture.wait(lambda s: row_status(s, second["name"], "Failed"), 5)
            snapshot(capture, args.output, "07-failed-acknowledged", results)
            results["interactions"].append(dict(apply_filter(capture, "", args.count), phase="restore full table"))
            capture.wait(lambda s: row_status(s, first["name"], "Ready") and row_status(s, second["name"], "Failed"), 5)
            snapshot(capture, args.output, "08-ready-and-failed-10000", results)
            capture.settle(1)
            results["video_end"] = capture.elapsed()
            ensure(not capture.sentinel.exists(), "Legacy CLI sentinel was invoked")
            results["checks"]["legacy_override_does_not_replace_native_key"] = True
            results["checks"]["cli_invocations"] = 0
            per_action = []
            for action in api.actions:
                counts = {op: sum(r.get("operation") == op and r.get("name") == action["name"] for r in api.requests)
                          for op in ["get", "patch"]}
                ensure(counts == {"get": 1, "patch": 1}, "Each action must use exactly one GET and one PATCH")
                per_action.append(dict(name=action["name"], **counts))
            counts = {op: sum(r.get("operation") == op and r.get("resource") == RESOURCE for r in api.requests)
                      for op in ["list", "watch", "get", "patch"]}
            ensure(counts["list"] <= 1 and counts["watch"] == 1, "Unexpected per-row lists/watches or informer restarts")
            ensure(counts["get"] == 2 and counts["patch"] == 2, "Unexpected per-row requests")
            results["checks"]["resource_request_counts"] = counts
            results["checks"]["per_action_requests"] = per_action
            results["checks"]["observed_outcomes"] = ["Ready", "Failed"]
            results["status"] = "passed"
        except BaseException as exc:
            error = exc
            results["status"] = "failed"
            results["error"] = str(exc)
            if capture is not None:
                snapshot(capture, args.output, "failure", results)
        finally:
            if capture is not None:
                capture.save(args.output / "session.cast.gz", args.cols, args.rows)
                capture.close()
            for log in Path(directory).rglob("*.log"):
                shutil.copyfile(log, args.output / log.name)
            api.stopped.set()
            api.shutdown()
            api.server_close()
            api_thread.join(timeout=1)
            results["actions"] = [{k: v for k, v in action.items() if not isinstance(v, threading.Event)} for action in api.actions]
            (args.output / "api-requests.json").write_text(json.dumps(api.requests, indent=2) + "\n")
            path = args.output / "results.json"
            path.write_text(json.dumps(results, indent=2) + "\n")
    if error:
        raise error
    return path, results


def render(path, results, fps, video):
    root = path.parent
    cols, rows = results["terminal"]["columns"], results["terminal"]["rows"]
    font_root = Path("/usr/share/fonts/truetype/dejavu")
    mono = ImageFont.truetype(str(font_root / "DejaVuSansMono.ttf"), 14)
    bold = ImageFont.truetype(str(font_root / "DejaVuSansMono-Bold.ttf"), 14)
    title_font = ImageFont.truetype(str(font_root / "DejaVuSans-Bold.ttf"), 22)
    label_font = ImageFont.truetype(str(font_root / "DejaVuSans.ttf"), 15)
    for name, stamp in results["screenshots"].items():
        replay = PERF.Replay(root / "session.cast.gz", cols, rows, stamp)
        PERF.rasterize(replay.screen, mono, bold, 9, 18).save(root / (name + ".png"))
    shutil.copyfile(root / "03-accepted-patch-held.png", root / "reconciling.png")
    if not video:
        return
    width, height = cols * 9 + 32, rows * 18 + 108
    width += width % 2
    height += height % 2
    target = root / "inline-reconcile.mp4"
    encoder = subprocess.Popen(["ffmpeg", "-y", "-loglevel", "error", "-f", "rawvideo", "-pix_fmt", "rgb24",
        "-s", f"{width}x{height}", "-r", str(fps), "-i", "-", "-an", "-c:v", "libx264", "-preset", "veryfast",
        "-crf", "24", "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-threads", "2", str(target)], stdin=subprocess.PIPE)
    start = results["video_start"]
    duration = results["video_end"] - start
    replay = PERF.Replay(root / "session.cast.gz", cols, rows, start)
    panel = PERF.rasterize(replay.screen, mono, bold, 9, 18)
    try:
        for frame in range(math.ceil(duration * fps)):
            elapsed = frame / fps
            if replay.advance(elapsed):
                panel = PERF.rasterize(replay.screen, mono, bold, 9, 18)
            image = Image.new("RGB", (width, height), "#10121a")
            draw = ImageDraw.Draw(image)
            label = "k9+ · " if results.get("runtime") == "k9plus" else ""
            draw.text((16, 12), f"{label}Native Flux reconcile · {results['fixture']['count']:,} HelmReleases", font=title_font, fill="#edf1f8")
            draw.text((16, 44), "REAL PACE 1× · Local fake API · Controller timing is scripted, not a Flux speed benchmark", font=label_font, fill="#d9c590")
            image.paste(panel, (16, 76))
            encoder.stdin.write(image.tobytes())
        encoder.stdin.close()
        ensure(encoder.wait() == 0, "ffmpeg encoding failed")
    except BaseException:
        encoder.kill()
        encoder.wait()
        raise
    results["video"] = {"file": target.name, "fps": fps, "duration_seconds": duration, "pace": 1,
                        "editing": "continuous original PTY timeline from loaded table to final outcomes; no cuts or speed changes"}
    path.write_text(json.dumps(results, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--binary", type=Path, default=Path("/tmp/k9plus-demo"))
    parser.add_argument("--app", choices=["k9plus", "k9s"], default="k9plus", help="runtime environment and storage namespace")
    parser.add_argument("--output", type=Path, default=Path("assets/k9plus/flux-inline"))
    parser.add_argument("--render-only", type=Path, metavar="RESULTS_JSON")
    parser.add_argument("--no-video", action="store_true")
    parser.add_argument("--count", type=int, default=10000)
    parser.add_argument("--cols", type=int, default=150)
    parser.add_argument("--rows", type=int, default=28)
    parser.add_argument("--fps", type=int, default=20)
    parser.add_argument("--timeout", type=float, default=60)
    args = parser.parse_args()
    if args.count < 100 or args.cols < 130 or args.rows < 20 or args.fps < 1:
        parser.error("count >= 100, cols >= 130, rows >= 20 and fps >= 1 required")
    if args.render_only:
        path = args.render_only.resolve()
        results = json.loads(path.read_text())
    else:
        args.binary = args.binary.resolve()
        args.output.mkdir(parents=True, exist_ok=True)
        path, results = capture_run(args)
    render(path, results, args.fps, not args.no_video)
    print(json.dumps({"status": results["status"], "checks": results["checks"], "video": results.get("video")}, indent=2))


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
# Modified for k9+; see NOTICE.
"""Check explicit session reconnect using real PTY cells and isolated API fixtures.

No live kubeconfig or configured cluster is inherited. Requires the same Python
packages as capture-demo.py. Credential renewal and TLS are covered separately
by session_refresh_test.go; these local HTTP fixtures verify UI and watch swap.
"""
import argparse
from copy import deepcopy
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import signal
import struct
import tempfile
import termios
import threading

SPEC = importlib.util.spec_from_file_location("workspace", Path(__file__).with_name("daily-workspace-journeys.py"))
W = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(W)
D, J = W.demo, W.journeys


def set_destination(path, old_port, fresh_port):
    text = path.read_text().replace(f"127.0.0.1:{old_port}", f"127.0.0.1:{fresh_port}")
    # The active named context remains demo-dev. A changed global current-context
    # must not redirect the refresh to this deliberately unusable destination.
    text = text.replace("current-context: demo-dev", "current-context: elsewhere")
    text = text.replace("contexts:\n", "contexts:\n- name: elsewhere\n  context:\n    cluster: elsewhere\n    user: demo-user\n")
    text = text.replace("clusters:\n", "clusters:\n- name: elsewhere\n  cluster:\n    server: http://127.0.0.1:1\n")
    path.write_text(text)
    return text


def inspect(terminal, output, name, required):
    # Poll emitted cells after each key: builds and race checks may be running
    # concurrently on the shared executor, so a single short drain is not proof
    # that a scrolling key has been handled.
    terminal.keys("\x1b[H", .4)
    pages = []
    for page in range(12):
        current = J.text(terminal)
        J.capture(terminal, output, f"{name}-{page + 1}")
        pages.append(current)
        terminal.keys("\x1b[6~", .4)
        for _ in range(5):
            if J.text(terminal) != current:
                break
            terminal.drain(.2)
        else:
            break
    else:
        raise AssertionError(f"{name}: view exceeded the fixture's 12-page bound")
    seen = "\n".join(pages)
    compact = "".join(c for c in seen if not c.isspace() and c != "│")
    for phrase in required:
        if phrase not in seen and "".join(c for c in phrase if not c.isspace()) not in compact:
            raise AssertionError(f"{name}: missing {phrase!r} in emitted pages:\n{seen}")
    terminal.keys("\x1b[H", .4)
    return seen


def run(binary, output):
    output.mkdir(parents=True, exist_ok=True)
    D.COLS, D.ROWS = 80, 24
    original, fresh = W.WorkspaceAPI(), W.WorkspaceAPI()
    fresh.objects = deepcopy(original.objects)
    for api in (original, fresh):
        api.connection_active = True
        threading.Thread(target=api.serve_forever, daemon=True).start()
    terminal = None
    checks = []
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    try:
        with tempfile.TemporaryDirectory(prefix="k9plus-session-refresh-") as directory:
            root = Path(directory)
            terminal = D.Terminal(binary, directory, original.server_port, command="pods apps", flags=["--readonly"])
            terminal.drain(4)
            terminal.command("workspace save daily apps --selector=app=daily --kinds=pods")
            W.wait_screen(terminal, ["Daily workspace", "daily", "investigation-api"])
            J.capture(terminal, output, "workspace-before-reconnect")
            store = root / "config" / "k9plus" / "workspaces.yaml"
            saved_workspace = store.read_bytes()
            terminal.command("connection")
            inspect(terminal, output, "connection-before", ["CONNECTION AND SESSION", "READABLE", "R reconnect session"])
            kubeconfig = root / "kubeconfig"
            edited = set_destination(kubeconfig, original.server_port, fresh.server_port)
            fresh.connection_fail = True
            terminal.keys("R", .5)
            inspect(terminal, output, "failed-refresh-retained", ["Reconnect failed", "Existing session and retained workspace kept", "API UNAVAILABLE"])
            terminal.keys("\x1b", .3)
            J.assert_screen(terminal, ["Daily workspace", "daily", "investigation-api"])
            if store.read_bytes() != saved_workspace:
                raise AssertionError("Failed reconnect changed saved workspace metadata")
            checks.append("failed setup retains the workspace and shows categorized API failure")

            terminal.command("connection")
            fresh.connection_fail = False
            start = len(fresh.requests)
            terminal.keys("R", .6)
            J.assert_screen(terminal, ["RECONNECTED", "R reconnect session"])
            inspect(terminal, output, "session-reconnected", ["Session reconnected for this same context", "Workspace and navigation retained", "GET /api and /apis"])
            if kubeconfig.read_text() != edited or store.read_bytes() != saved_workspace:
                raise AssertionError("Reconnect modified global kubeconfig or saved workspace metadata")
            setup = fresh.requests[start:]
            for path in ("/version", "/api", "/apis"):
                if not any(r["path"] == path for r in setup):
                    raise AssertionError("Fresh setup omitted " + path)
            if not any(r["path"] == "/api/v1/namespaces/apps/pods" and r["query"].get("limit") == ["1"] for r in setup):
                raise AssertionError("Fresh setup omitted the bounded captured-namespace permission check")
            checks.append("same named context reconnect ignores changed global current-context and verifies fresh API/discovery/namespace reads")
            terminal.keys("\x1b", .3)
            J.assert_screen(terminal, ["Daily workspace", "daily", "investigation-api"])
            J.capture(terminal, output, "workspace-after-reconnect")
            checks.append("Back restores retained workspace navigation and original rows")

            fresh.connection_active = False
            before_watch = len(fresh.requests)
            terminal.command("pods apps")
            W.wait_screen(terminal, ["pods(apps)", "investigation-api"])
            terminal.drain(.5)
            if not any(r["path"] == "/api/v1/namespaces/apps/pods" and r["query"].get("watch") == ["true"] for r in fresh.requests[before_watch:]):
                raise AssertionError("Resource browsing did not start a watch on the new session")
            J.capture(terminal, output, "fresh-session-pods")
            checks.append("resource browsing attaches list/watch to the fresh session")

            terminal.command("connection")
            terminal.screen.resize(18, 60)
            D.COLS, D.ROWS = 60, 18
            fcntl.ioctl(terminal.master, termios.TIOCSWINSZ, struct.pack("HHHH", 18, 60, 0, 0))
            os.killpg(terminal.process.pid, signal.SIGWINCH)
            terminal.drain(.5)
            J.assert_screen(terminal, ["demo-dev", "R reconnect session", "Observed"])
            inspect(terminal, output, "connection-narrow", ["CONNECTION AND SESSION", "demo-dev", "R reconnect session", "GET /version"])
            checks.append("80x24 and 60x18 terminal layouts retain destination, checks, reconnect gesture and Back")
            terminal.close()
            terminal = None
            if hashlib.sha256(binary.read_bytes()).hexdigest() != digest:
                raise AssertionError("Binary changed during the PTY journey")
            manifest = {"result": "passed", "source": "Actual isolated PTY; disposable localhost Kubernetes API fixtures",
                        "binary_sha256": digest, "checks": checks, "limitations": "HTTP fixture verifies session/UI/watch behavior; Go TLS fixtures verify actual credential renewal. No live cluster or credential provider.",
                        "fresh_requests": fresh.requests, "original_requests": original.requests}
            (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
            print(json.dumps({"result": "passed", "checks": checks}, indent=2))
    finally:
        if terminal:
            terminal.close()
        for api in (original, fresh):
            api.stopped.set()
            api.shutdown()
            api.server_close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    run(args.binary.resolve(), args.output.resolve())


if __name__ == "__main__":
    main()

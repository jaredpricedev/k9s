#!/usr/bin/env python3
# Modified for k9+; see NOTICE.
"""Verify owned local-session workflows using real PTY cells and local fixtures.

Disposable background Python plugins provide real children. A denied selected
Pod exercises forward setup failure. Actual TLS/websocket/SPDY forwarding and
socket cancellation are verified separately by the Go DAO fixtures.
"""

import argparse
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import struct
import sys
import tempfile
import termios
import threading
import time
from urllib.parse import urlparse

SPEC = importlib.util.spec_from_file_location("workspace", Path(__file__).with_name("daily-workspace-journeys.py"))
W = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(W)
D, J = W.demo, W.journeys


class LocalSessionHandler(W.WorkspaceHandler):
    def do_GET(self):
        if urlparse(self.path).path == self.server.selected_path and self.server.deny_selected:
            self.record("GET")
            return self.failure(403, "Forbidden", "Fixture selected endpoint access denied")
        super().do_GET()


def wait_file(terminal, path, timeout=8):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        terminal.drain(.1)
        if path.exists():
            return
    raise AssertionError(f"Owned fixture child did not start: {path.name}\n" + J.text(terminal))


def alive(pid):
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


def wait_child_exit(terminal, pid, timeout=8):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        terminal.drain(.1)
        if not alive(pid):
            return
    raise AssertionError("Owned fixture child remained alive after cancellation")


def inspect(terminal, output, name, required):
    terminal.keys("\x1b[H", .2)
    pages = []
    content = []
    for page in range(12):
        before = J.text(terminal)
        J.capture(terminal, output, f"{name}-{page + 1}")
        pages.append(before)
        # Page boundaries include app chrome. Rejoin only actual detail cells
        # so a wrapped fact split across two pages remains verifiable.
        content.extend(terminal.screen.display[5:-5])
        terminal.keys("\x1b[6~", .2)
        if J.text(terminal) == before:
            break
    else:
        raise AssertionError("Local lifecycle details exceeded fixture page bound")
    seen = "\n".join(pages)
    compact = "".join(c for c in "\n".join(content) if not c.isspace() and c != "│")
    for phrase in required:
        if phrase not in seen and "".join(c for c in phrase if not c.isspace()) not in compact:
            raise AssertionError(f"Missing lifecycle fact {phrase!r}:\n{seen}")
    for excluded in ("fixture-credential-must-not-export", "RAW-PLUGIN-SECRET"):
        if excluded in seen:
            raise AssertionError("Local lifecycle review exposed raw process output or Pod annotations")
    terminal.keys("\x1b[H", .2)


def write_plugins(root):
    plugins = {}
    for key, label, shortcut in (("alpha", "Owned alpha", "Shift-U"), ("beta", "Owned beta", "Shift-V"),
                                 ("early", "Early exit", "Shift-W")):
        marker = root / (key + ".pid")
        release = root / (key + ".release")
        code = "import os,pathlib,time; pathlib.Path(" + repr(str(marker)) + ").write_text(str(os.getpid())); print('RAW-PLUGIN-SECRET',flush=True); "
        if key == "early":
            code += "raise SystemExit(7)"
        else:
            code += "\nwhile not pathlib.Path(" + repr(str(release)) + ").exists(): time.sleep(.1)"
        plugins[key] = {"shortCut": shortcut, "description": label, "scopes": ["all"], "override": True,
                        "command": sys.executable, "background": True, "args": ["-c", code]}
    target = root / "config" / "k9plus" / "plugins.yaml"
    target.parent.mkdir(parents=True)
    target.write_text(json.dumps({"plugins": plugins}, indent=2))


def resize(terminal, columns, rows):
    terminal.screen.resize(rows, columns)
    D.COLS, D.ROWS = columns, rows
    fcntl.ioctl(terminal.master, termios.TIOCSWINSZ, struct.pack("HHHH", rows, columns, 0, 0))
    os.killpg(terminal.process.pid, signal.SIGWINCH)
    terminal.drain(.3)


def run_skin(binary, output, api, skin):
    D.COLS, D.ROWS = 80, 24
    checks = []
    terminal = None
    api.deny_selected = False
    with tempfile.TemporaryDirectory(prefix="k9plus-owned-session-") as directory:
        root = Path(directory)
        write_plugins(root)
        if skin != "stock":
            skins = root / "config" / "k9plus" / "skins"
            skins.mkdir()
            shutil.copyfile(Path(__file__).resolve().parent.parent / "skins" / (skin + ".yaml"), skins / (skin + ".yaml"))
        try:
            terminal = D.Terminal(binary, directory, api.server_port, command="pods apps", flags=["--readonly"],
                                  ui_config=f"    noIcons: true\n    skin: {skin}\n", color_mode="256")
            W.wait_screen(terminal, ["pods(apps)", "investigation-api"])
            terminal.keys("/investigation-api\r", .3)
            terminal.keys("\x0flocal sessions", .2)
            W.wait_screen(terminal, ["Local sessions", ":sessions"])
            terminal.keys("\r", .2)
            W.wait_screen(terminal, ["LOCAL SESSIONS", "0 active", "0 ended"])
            terminal.keys("\x1b", .2)
            W.wait_screen(terminal, ["pods(apps)", "investigation-api"])
            checks.append(f"{skin}: Actions search opens local sessions in read-only mode and Back retains the filtered Pod page")
            terminal.keys("U", .3)
            wait_file(terminal, root / "alpha.pid")
            alpha = int((root / "alpha.pid").read_text())
            terminal.keys("V", .3)
            wait_file(terminal, root / "beta.pid")
            beta = int((root / "beta.pid").read_text())
            terminal.command("sessions")
            W.wait_screen(terminal, ["LOCAL SESSIONS", "2 active", "RUNNING", "demo-dev", "Owned alpha", "Owned beta"])
            J.capture(terminal, output, skin + "-owned-overview-80x24")
            terminal.keys("\r", .3)
            inspect(terminal, output, skin + "-captured-beta", ["Owned beta", "demo-dev", "fixture-investigation-pod", "LIFECYCLE LOG", "Started:", "Operation receipt:"])
            terminal.keys("\x1b", .2)
            terminal.keys("c", .2)
            wait_child_exit(terminal, beta)
            if not alive(alpha):
                raise AssertionError("Stopping beta also stopped the unrelated alpha child")
            terminal.keys("r", .2)
            W.wait_screen(terminal, ["1 active", "1 ended", "UNKNOWN", "RUNNING"])
            J.capture(terminal, output, skin + "-exact-owned-cancellation")
            checks.append(f"{skin}: cleanup stops only selected real child; remote effects remain UNKNOWN")

            resize(terminal, 60, 18)
            J.assert_screen(terminal, ["LOCAL SESSIONS", "1 active", "demo-dev", "Enter details", "c stop owned", "Esc back"])
            J.capture(terminal, output, skin + "-owned-overview-60x18")
            terminal.keys("\r", .2)
            inspect(terminal, output, skin + "-captured-beta-narrow", ["Owned beta", "fixture-investigation-pod", "Operation receipt:", "remote command effects are unknown"])
            terminal.keys("\x1b", .2)
            terminal.keys("\x1b", .2)
            W.wait_screen(terminal, ["pods(apps)", "investigation-api"])
            checks.append(f"{skin}: no-icons 80x24/60x18 overview/details/Back retain captured identity and keyboard controls")

            terminal.keys("W", .2)
            wait_file(terminal, root / "early.pid")
            wait_child_exit(terminal, int((root / "early.pid").read_text()))
            terminal.command("sessions")
            W.wait_screen(terminal, ["1 active", "2 ended", "UNKNOWN"])
            terminal.keys("\r", .2)
            inspect(terminal, output, skin + "-early-exit", ["Early exit", "UNKNOWN", "remote command effects are unknown"])
            terminal.keys("\x1b", .2)
            terminal.keys("\x1b", .2)
            checks.append(f"{skin}: early child exit retains a bounded lifecycle receipt without raw plugin output")

            api.deny_selected = True
            terminal.keys("F", 1)
            terminal.command("sessions")
            W.wait_screen(terminal, ["1 active", "3 ended", "FAILED"])
            terminal.keys("\r", .2)
            inspect(terminal, output, skin + "-denied-forward", ["Pod port-forward", "fixture-investigation-pod", "denied", "Local binding:"])
            terminal.keys("\x1b", .2)
            before = api.selected_reads()
            terminal.keys("r", .2)
            if api.selected_reads() != before:
                raise AssertionError("Refreshing local session state reread the denied Kubernetes target")
            J.capture(terminal, output, skin + "-local-refresh-with-denied-api")
            checks.append(f"{skin}: failed forward distinguishes denied access; local refresh remains usable without an API read")

            # Newest two rows are failed/early; the older alpha is the fourth.
            terminal.keys("jjj", .2)
            terminal.keys("c", .2)
            wait_child_exit(terminal, alpha)
            terminal.keys("r", .2)
            W.wait_screen(terminal, ["0 active", "4 ended"])
            J.capture(terminal, output, skin + "-cleanup-confirmed")

            # A fresh isolated child must also stop on final app shutdown.
            terminal.keys("\x1b", .2)
            W.wait_screen(terminal, ["pods(apps)", "investigation-api"])
            (root / "alpha.pid").unlink()
            terminal.keys("U", .2)
            wait_file(terminal, root / "alpha.pid")
            shutdown_child = int((root / "alpha.pid").read_text())
            terminal.command("sessions")
            W.wait_screen(terminal, ["1 active", "RUNNING"])
            J.capture(terminal, output, skin + "-owned-before-shutdown")
            os.kill(terminal.process.pid, signal.SIGTERM)
            if terminal.process.wait(timeout=6) != 143:
                raise AssertionError("SIGTERM did not retain the conventional restored-terminal exit status")
            attributes = termios.tcgetattr(terminal.master)
            restored = termios.ECHO | termios.ICANON | termios.ISIG
            if attributes[3] & restored != restored:
                raise AssertionError("App shutdown left the controlling terminal in raw mode")
            if alive(shutdown_child):
                raise AssertionError("App shutdown left its isolated owned child alive")
            checks.append(f"{skin}: final SIGTERM cleans the isolated owned child and restores canonical terminal input")
            terminal.close()
            terminal = None
        finally:
            if terminal:
                terminal.close()
            for log in (root / "logs" / "k9plus").glob("*.log"):
                shutil.copyfile(log, output / (skin + "-" + log.name))
    return checks


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    binary, output = args.binary.resolve(), args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    api = W.WorkspaceAPI()
    api.RequestHandlerClass = LocalSessionHandler
    with socket.socket() as binding:
        binding.bind(("127.0.0.1", 0))
        port = binding.getsockname()[1]
    for obj in api.objects:
        if obj["kind"] == "Pod" and obj["metadata"]["name"] == "investigation-api":
            obj["spec"]["containers"][0]["ports"] = [{"name": "http", "containerPort": 8080, "protocol": "TCP"}]
            obj["metadata"]["annotations"]["k9scli.io/auto-port-forwards"] = f"api::{port}:8080"
    threading.Thread(target=api.serve_forever, daemon=True).start()
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    try:
        checks = []
        for skin in ("stock", "monochrome"):
            checks.extend(run_skin(binary, output, api, skin))
        if hashlib.sha256(binary.read_bytes()).hexdigest() != digest:
            raise AssertionError("Binary changed during the PTY journey")
        manifest = {"result": "passed", "source": "Actual controlling PTY; disposable localhost Kubernetes API; real owned Python children",
                    "binary_sha256": digest, "checks": checks,
                    "limitations": "No live cluster. Actual forwarding negotiation/TLS/auth/proxy cancellation is covered by Go DAO tests; these terminal journeys cover ownership, receipts and denied setup.",
                    "requests": api.requests}
        (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        print(json.dumps({"result": "passed", "checks": checks}, indent=2))
    finally:
        api.stopped.set()
        api.shutdown()
        api.server_close()


if __name__ == "__main__":
    main()

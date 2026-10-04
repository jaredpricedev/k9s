#!/usr/bin/env python3
# Modified for k9+; see NOTICE.
"""Assert actual terminal restoration for normal exit, signals and failures.

Uses isolated localhost fixtures, never a configured cluster. Requires the same
Python dependencies as capture-demo.py. Build the optional runtime fixture with:
  go test -c -o /tmp/k9plus-view.test ./internal/view
  python scripts/lifecycle-journeys.py --binary /tmp/k9plus \
    --test-binary /tmp/k9plus-view.test --output /tmp/lifecycle-results.json
"""
import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import pty
import select
import signal
import struct
import subprocess
import tempfile
import termios
import threading
import time

SPEC = importlib.util.spec_from_file_location("workspace", Path(__file__).with_name("daily-workspace-journeys.py"))
W = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(W)
D = W.demo


def drain(master, seconds=1):
    chunks = []
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if select.select([master], [], [], .05)[0]:
            try:
                data = os.read(master, 65536)
            except OSError:
                break
            if not data:
                break
            chunks.append(data)
    return b"".join(chunks)


def result(mode, process, master, baseline, during, output, code, entered=True):
    after = termios.tcgetattr(master)
    row = {
        "mode": mode, "exit_code": process.poll(),
        "raw_during": not bool(during[3] & termios.ICANON),
        "canonical_restored": bool(after[3] & termios.ICANON),
        "echo_restored": bool(after[3] & termios.ECHO),
        "terminal_flags_equal": baseline == after,
        "left_alternate_screen": b"\x1b[?1049l" in output or b"\x1b[?47l" in output,
        "cursor_shown": b"\x1b[?25h" in output,
    }
    row["passed"] = (row["exit_code"] == code and row["terminal_flags_equal"]
                     and (not entered or (row["raw_during"] and row["left_alternate_screen"] and row["cursor_shown"])))
    return row


def app_case(binary, root, mode, code):
    D.COLS, D.ROWS = 80, 24
    api = W.WorkspaceAPI()
    api.enforce_workspace = False
    threading.Thread(target=api.serve_forever, daemon=True).start()
    original = D.pty.openpty
    saved = {}
    terminal = None
    original_runtime = D.isolated_runtime

    def openpty():
        master, slave = original()
        saved[master] = termios.tcgetattr(slave)
        return master, slave

    def invalid_terminal(*args, **kwargs):
        env = original_runtime(*args, **kwargs)
        env["TERM"] = "k9plus-invalid-terminal"
        return env

    D.pty.openpty = openpty
    if mode == "startup-error":
        D.isolated_runtime = invalid_terminal
    try:
        terminal = D.Terminal(binary, str(root), api.server_port, command="pods apps", flags=["--readonly"], ui_config="    noIcons: true\n" + ("  noExitOnCtrlC: true\n" if mode == "SIGINT-no-keyboard-exit" else ""))
        if mode == "startup-error":
            output = drain(terminal.master, 2)
            terminal.process.wait(timeout=10)
            during = saved[terminal.master]
        else:
            terminal.drain(2)
            during = termios.tcgetattr(terminal.master)
            if mode == "ctrl-c":
                os.write(terminal.master, b"\x03")
            elif mode == "command-quit":
                os.write(terminal.master, b":q\r")
            else:
                os.kill(terminal.process.pid, getattr(signal, "SIGINT" if mode == "SIGINT-no-keyboard-exit" else mode))
            output = drain(terminal.master, 2)
            terminal.process.wait(timeout=10)
        return result(mode, terminal.process, terminal.master, saved[terminal.master], during, output, code, mode != "startup-error")
    finally:
        D.pty.openpty = original
        D.isolated_runtime = original_runtime
        if terminal:
            terminal.close()
        api.stopped.set()
        api.shutdown()
        api.server_close()


def fixture_case(binary, root, mode):
    master, slave = pty.openpty()
    baseline = termios.tcgetattr(slave)
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
    env = D.isolated_runtime(root, "k9plus", root / "kubeconfig", {"TERM": "xterm-256color", "K9PLUS_LIFECYCLE_TTY_FIXTURE": mode})

    def control_terminal():
        os.setsid()
        fcntl.ioctl(0, termios.TIOCSCTTY, 0)

    process = subprocess.Popen([str(binary), "-test.run=^TestApplicationLifecyclePTYFixture$", "-test.timeout=15s"], stdin=slave, stdout=slave, stderr=slave, env=env, preexec_fn=control_terminal)
    os.close(slave)
    try:
        drain(master, 1)
        during = termios.tcgetattr(master)
        os.write(master, b"x")
        output = drain(master, 2)
        process.wait(timeout=10)
        return result("runtime-" + mode, process, master, baseline, during, output, 0)
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
        os.close(master)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--test-binary", type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    results = []
    with tempfile.TemporaryDirectory(prefix="k9plus-lifecycle-") as runtime:
        root = Path(runtime)
        for mode, code in [("ctrl-c", 0), ("command-quit", 0), ("SIGHUP", 129), ("SIGTERM", 143), ("SIGINT", 130), ("SIGINT-no-keyboard-exit", 130), ("startup-error", 1)]:
            results.append(app_case(args.binary.resolve(), root / mode, mode, code))
        if args.test_binary:
            for mode in ["error", "panic"]:
                results.append(fixture_case(args.test_binary.resolve(), root / mode, mode))
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps({"method": "Actual isolated PTY; no configured cluster", "results": results}, indent=2) + "\n")
    print(json.dumps(results, indent=2))
    if any(not row["passed"] for row in results):
        raise SystemExit(1)


if __name__ == "__main__":
    main()

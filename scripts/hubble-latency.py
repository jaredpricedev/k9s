#!/usr/bin/env python3
# Modified for k9+; see NOTICE.
"""Measure native Hubble input-to-painted-PTY frames with synthetic 10k events.

Build the view test binary once, then run using Python with pyte and Pillow:
  go test -c ./internal/view -o /tmp/k9plus-view-tests
  python scripts/hubble-latency.py --binary /tmp/k9plus-view-tests --output /tmp/hubble-evidence
No Relay, Kubernetes API or configured context is contacted.
"""

import argparse
import codecs
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import pty
import select
import statistics
import struct
import subprocess
import termios
import time

import pyte


COLS, ROWS = 120, 40


class Terminal:
    def __init__(self, binary, gomaxprocs):
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))

        def controlling_terminal():
            os.setsid()
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)

        env = dict(os.environ, TERM="xterm-256color", COLORTERM="truecolor", K9PLUS_HUBBLE_TTY_FIXTURE="1",
                   GOMAXPROCS=str(gomaxprocs))
        self.process = subprocess.Popen([str(binary), "-test.run=^TestHubblePTYFixture$", "-test.timeout=2m"],
                                        stdin=slave, stdout=slave, stderr=slave, env=env, preexec_fn=controlling_terminal)
        os.close(slave)
        self.screen = pyte.Screen(COLS, ROWS)
        self.stream = pyte.Stream(self.screen)
        self.decoder = codecs.getincrementaldecoder("utf-8")("replace")
        self.raw = bytearray()

    def wait(self, predicate, timeout=15):
        until = time.monotonic() + timeout
        while time.monotonic() < until:
            if predicate(self.screen.display):
                return
            if select.select([self.master], [], [], min(.1, max(0, until - time.monotonic())))[0]:
                try:
                    data = os.read(self.master, 65536)
                except OSError:
                    break
                self.raw.extend(data)
                self.stream.feed(self.decoder.decode(data))
            if self.process.poll() is not None:
                break
        raise AssertionError("Expected frame not emitted:\n" + "\n".join(self.screen.display))

    def key_to_frame(self, key, expected):
        start = time.perf_counter_ns()
        os.write(self.master, key)
        self.wait(lambda lines: expected in lines[-1])
        return (time.perf_counter_ns() - start) / 1e6

    def close(self):
        if self.process.poll() is None:
            os.write(self.master, b"X")
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.terminate()
                self.process.wait(timeout=5)
        os.close(self.master)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--samples", type=int, default=20)
    parser.add_argument("--gomaxprocs", type=int, default=4)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    spec = importlib.util.spec_from_file_location("capture_demo", Path(__file__).with_name("capture-demo.py"))
    demo = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(demo)
    demo.COLS, demo.ROWS = COLS, ROWS
    terminal = Terminal(args.binary.resolve(), args.gomaxprocs)
    samples = {"navigation": [], "inspect": [], "back": []}

    def capture(name):
        (args.output / (name + ".txt")).write_text("\n".join(terminal.screen.display) + "\n")
        demo.rasterize(terminal.screen, args.output / (name + ".png"))

    def frame(mode, event, frozen=True, evicted=0):
        return f"FRAME mode={mode} event={event} frozen={str(frozen).lower()} evicted={evicted}"

    try:
        terminal.wait(lambda lines: frame("conversation", 1) in lines[-1])
        capture("frozen-conversation")
        for _ in range(args.samples):
            samples["navigation"].append(terminal.key_to_frame(b"j", frame("conversation", 2)))
            samples["inspect"].append(terminal.key_to_frame(b"\r", frame("detail", 2)))
            samples["back"].append(terminal.key_to_frame(b"\x1b", frame("conversation", 2)))
            terminal.key_to_frame(b"k", frame("conversation", 1))
        terminal.key_to_frame(b"\r", frame("detail", 1))
        capture("frozen-detail")
        terminal.key_to_frame(b"\x1b", frame("conversation", 1))
        body_before = "\n".join(terminal.screen.display[6:-2])
        burst_ms = terminal.key_to_frame(b"B", frame("conversation", 1, evicted=10000))
        body_after = "\n".join(terminal.screen.display[6:-2])
        if body_before != body_after:
            raise AssertionError("Frozen conversation body changed under eviction burst")
        capture("frozen-after-burst")
        resume_ms = terminal.key_to_frame(b"s", frame("conversation", 10001, frozen=False, evicted=10000))
        capture("live-resumed")
        report = {
            "fixture": "10,000 retained synthetic Hubble events; native tview/tcell in a 120x40 PTY; no Relay or API",
            "method": "Key write to completed visible frame/footer parsed from emitted PTY cells; includes Python/parser overhead and Escape decoding delay. Does not measure a graphical terminal compositor.",
            "platform": platform.platform(),
            "binary_sha256": hashlib.file_digest(args.binary.open("rb"), "sha256").hexdigest(),
            "gomaxprocs": args.gomaxprocs, "samples_per_action": args.samples, "columns": COLS, "rows": ROWS,
            "refresh": "Input-driven fixture frames; production collector status interval is 250 ms (4 Hz), collector not running here.",
            "samples_ms": {name: [round(n, 3) for n in values] for name, values in samples.items()},
            "median_ms": {name: round(statistics.median(values), 3) for name, values in samples.items()},
            "p95_ms": {name: round(sorted(values)[max(0, int(len(values) * .95) - 1)], 3) for name, values in samples.items()},
            "frozen_10000_event_burst_ms": round(burst_ms, 3), "explicit_resume_to_live_ms": round(resume_ms, 3),
            "frozen_body_sha256_before": hashlib.sha256(body_before.encode()).hexdigest(),
            "frozen_body_sha256_after": hashlib.sha256(body_after.encode()).hexdigest(), "result": "passed",
        }
        (args.output / "latency.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report, indent=2))
    finally:
        (args.output / "terminal.ansi").write_bytes(terminal.raw)
        terminal.close()


if __name__ == "__main__":
    main()

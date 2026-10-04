#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright Authors of K9s
"""Verify and capture real 80x24 TUI cells in true-color and 256-color modes.

Uses the disposable API fixture from regression-journeys.py. Requires pyte and
Pillow. Covers stock/high-contrast/monochrome with no icons and read-only mode.
This is repeatable fixture evidence, not a live-cluster or operator-study claim.
"""

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import tempfile
import threading
import time

spec = importlib.util.spec_from_file_location("regression_journeys", Path(__file__).with_name("regression-journeys.py"))
journeys = importlib.util.module_from_spec(spec)
spec.loader.exec_module(journeys)
demo = journeys.demo


def wait_for(terminal, required, timeout=8):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        terminal.drain(.1)
        text = "\n".join(terminal.screen.display)
        if all(phrase in text for phrase in required):
            return
    raise AssertionError(f"Timed out waiting for {required!r}:\n{text}")


def capture_skin(binary, output, api, skin, color_mode):
    with tempfile.TemporaryDirectory(prefix="k9plus-skin-") as directory:
        root = Path(directory)
        skin_directory = root / "config" / "k9plus" / "skins"
        skin_directory.mkdir(parents=True)
        if skin != "stock":
            shutil.copyfile(Path(__file__).resolve().parent.parent / "skins" / (skin + ".yaml"),
                            skin_directory / (skin + ".yaml"))
        terminal = demo.Terminal(binary, directory, api.server_port, command="pods apps", flags=["--readonly"],
                                 ui_config=f"    noIcons: true\n    skin: {skin}\n", color_mode=color_mode)
        try:
            expected = ["[RO]", "demo-dev", "ns:apps", "CrashLoopBackOff", "ContainerCreating", "0/1", "RESTARTS", "…", "> "]
            wait_for(terminal, expected)
            prefix = f"pods-80x24-{skin}-{color_mode}"
            terminal.capture(output, prefix, expected)
            terminal.keys("\x1bOQ", .1)  # xterm F2 reveals the full destination.
            destination = ["demo-dev", "demo-cluster", "demo-user"]
            wait_for(terminal, destination)
            terminal.capture(output, prefix + "-destination", destination)
            return terminal.captures
        finally:
            terminal.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", type=Path, default=Path("/tmp/k9plus-skin-journeys"))
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    demo.COLS, demo.ROWS = 80, 24
    api = journeys.JourneyAPI()
    threading.Thread(target=api.serve_forever, daemon=True).start()
    try:
        captures = []
        for skin in ("stock", "high-contrast", "monochrome"):
            for color_mode in ("true-color", "256"):
                captures.extend(capture_skin(args.binary.resolve(), args.output, api, skin, color_mode))
        manifest = {"source": "actual PTY terminal cells; disposable local API fixture", "result": "passed",
                    "binary_sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest(), "captures": captures}
        (args.output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    finally:
        api.stopped.set()
        api.shutdown()
        api.server_close()


if __name__ == "__main__":
    main()

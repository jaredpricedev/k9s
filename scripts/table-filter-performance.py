#!/usr/bin/env python3
# Modified for k9+; see NOTICE.
"""Measure only the retained 10,000-Pod filter journey in the real CLI.

Uses regression-journeys.py's disposable loopback API and 20 committed queries.
This measures input-write to visible result/count, including terminal parsing;
it is neither a human study nor a live-cluster or display-latency measurement.
Run builds and captures serially in a quiet window, with the same runtime limits.
"""

import argparse
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--go", required=True)
    parser.add_argument("--measurement-context", required=True, help="Quiet window and any known confounding processes")
    args = parser.parse_args()
    binary = args.binary.resolve()
    args.output.mkdir(parents=True, exist_ok=True)
    spec = importlib.util.spec_from_file_location("regression", Path(__file__).with_name("regression-journeys.py"))
    regression = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(regression)
    result = {
        "started_at_utc": datetime.now(timezone.utc).isoformat(),
        "source_commit": args.source_commit,
        "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "build_info": subprocess.check_output([args.go, "version", "-m", str(binary)], text=True),
        "platform": platform.platform(),
        "cpu_count": os.cpu_count(),
        "cgroup_cpu_max": Path("/sys/fs/cgroup/cpu.max").read_text().strip(),
        "measurement_context": args.measurement_context,
        "gomaxprocs": os.environ.get("GOMAXPROCS"),
        "gomemlimit": os.environ.get("GOMEMLIMIT"),
        "viewport": {"columns": 120, "rows": 34},
        "coverage": "Actual CLI, real PTY, disposable loopback API; no live cluster or human study.",
        "journey": regression.performance_journey(binary, args.output),
        "finished_at_utc": datetime.now(timezone.utc).isoformat(),
    }
    (args.output / "results.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result["journey"], indent=2))


if __name__ == "__main__":
    main()

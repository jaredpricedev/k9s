#!/usr/bin/env python3
# Modified for k9+; see NOTICE.
"""Run identical K9s microbenchmarks sequentially across three source trees.

The benchmark fixtures are copied from --fork into detached baseline worktrees;
only *_test.go files are copied. Production sources and dependencies are untouched.
Requires the same Go toolchain for every tree. No cluster is used.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import statistics
import subprocess
import tempfile
import time

FIXTURES = ["internal/ui/performance_test.go", "internal/model1/performance_test.go",
            "internal/model1/table_delete_bench_test.go", "internal/model1/row_clone_bench_test.go"]
PACKAGES = {"ui": "BenchmarkPerformance", "model1": "BenchmarkPerformance|BenchmarkTableDataDelete10K|BenchmarkRowEventsClone10K"}
PATTERN = re.compile(r"^(Benchmark\S+)-\d+\s+\d+\s+([\d.]+) ns/op\s+([\d.]+) B/op\s+([\d.]+) allocs/op$", re.M)


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ["upstream", "before", "fork"]:
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--go", default="go")
    parser.add_argument("--rounds", type=int, default=5)
    parser.add_argument("--benchtime", default="200ms")
    parser.add_argument("--output", type=Path, default=Path("assets/performance/benchmarks"))
    args = parser.parse_args()
    if args.rounds < 3:
        parser.error("use at least three rounds")
    roots = {name: getattr(args, name).resolve() for name in ["upstream", "before", "fork"]}
    if len(set(roots.values())) != 3:
        parser.error("provide three distinct worktrees")
    args.output.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GOTOOLCHAIN="local", GOMAXPROCS="4")
    env["PATH"] = str(Path(args.go).resolve().parent) + os.pathsep + env["PATH"]
    metadata = {"go": subprocess.check_output([args.go, "version"], text=True).strip(),
                "platform": platform.platform(), "gomaxprocs": 4,
                "rounds": args.rounds, "benchtime": args.benchtime,
                "captured_at_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                "cpu": next((line.split(":", 1)[1].strip() for line in Path("/proc/cpuinfo").read_text().splitlines()
                             if line.startswith("model name")), "unknown"),
                "trees": {}, "fixtures": {}, "samples": {}}
    for fixture in FIXTURES:
        source = (roots["fork"] / fixture).read_bytes()
        metadata["fixtures"][fixture] = hashlib.sha256(source).hexdigest()
        for name in ["upstream", "before"]:
            target = roots[name] / fixture
            # Never overwrite a tracked file from a baseline revision.
            tracked = subprocess.run(["git", "-C", str(roots[name]), "cat-file", "-e", "HEAD:" + fixture],
                                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0
            if tracked and target.read_bytes() != source:
                raise RuntimeError(f"Refusing to overwrite tracked fixture: {target}")
            target.write_bytes(source)
    for name, root in roots.items():
        metadata["trees"][name] = {"head": git(root, "rev-parse", "HEAD"),
            "tracked_diff_sha256": hashlib.sha256(git(root, "diff", "HEAD", "--", "*.go").encode()).hexdigest(),
            "go_mod_sha256": hashlib.sha256((root / "go.mod").read_bytes()).hexdigest(),
            "go_sum_sha256": hashlib.sha256((root / "go.sum").read_bytes()).hexdigest()}
    with tempfile.TemporaryDirectory(prefix="k9s-bench-") as temp:
        binaries = {}
        for name, root in roots.items():
            for package in PACKAGES:
                binary = Path(temp) / (name + "-" + package)
                subprocess.run([args.go, "test", "-buildvcs=false", "-p", "2", "-c", "-o", str(binary),
                                "./internal/" + package], cwd=root, env=env, check=True)
                binaries[name, package] = binary
        names = list(roots)
        for round_index in range(args.rounds):
            # Rotate order to reduce first/last-run bias. No benchmark runs overlap.
            order = names[round_index % 3:] + names[:round_index % 3]
            for name in order:
                for package, pattern in PACKAGES.items():
                    start = time.monotonic()
                    result = subprocess.run([str(binaries[name, package]), "-test.run=^$",
                        "-test.bench=" + pattern, "-test.benchmem", "-test.benchtime=" + args.benchtime,
                        "-test.count=1"], cwd=roots[name], env=env, text=True, stdout=subprocess.PIPE,
                        stderr=subprocess.STDOUT, check=True)
                    (args.output / f"round-{round_index + 1}-{name}-{package}.txt").write_text(result.stdout)
                    matches = PATTERN.findall(result.stdout)
                    if not matches:
                        raise RuntimeError("No benchmark samples: " + result.stdout)
                    for benchmark, ns, memory, allocs in matches:
                        key = benchmark.removeprefix("Benchmark")
                        metadata["samples"].setdefault(key, {}).setdefault(name, []).append(
                            {"ns_per_op": float(ns), "bytes_per_op": float(memory), "allocs_per_op": float(allocs)})
                    print(f"Round {round_index + 1}: {name}/{package} ({time.monotonic() - start:.1f}s)", flush=True)
                    (args.output / "results.json").write_text(json.dumps(metadata, indent=2) + "\n")
    metadata["summary"] = {}
    for benchmark, builds in metadata["samples"].items():
        metadata["summary"][benchmark] = {}
        for name, samples in builds.items():
            metadata["summary"][benchmark][name] = {metric: {"median": statistics.median(s[metric] for s in samples),
                "min": min(s[metric] for s in samples), "max": max(s[metric] for s in samples)}
                for metric in samples[0]}
    (args.output / "results.json").write_text(json.dumps(metadata, indent=2) + "\n")


if __name__ == "__main__":
    main()

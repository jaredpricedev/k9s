#!/usr/bin/env python3
# Modified for k9+; see NOTICE.
"""Measure and film real k9+/K9s terminal responses against a disposable local API.

Dependencies: Python 3, pyte, Pillow, ffmpeg, and Go (to verify build metadata).
Example (compile both revisions with the same Go version and build flags first):
  python scripts/perf-demo.py --upstream /tmp/k9s-upstream --fork /tmp/k9plus-demo
  python scripts/perf-demo.py --render-only assets/performance/results.json
Runtime defaults: upstream/before use k9s; fork uses k9plus. Use --fork-app k9s
to repeat a historical fork comparison. New runs default to assets/k9plus/performance.

Captures run sequentially; rotate build order on successive rounds. A sample
starts immediately before Enter applies a prepared filter and ends when a complete tcell frame
contains the exact expected count and visible rows. Output receipt is timestamped
before terminal emulation. These are PTY-observed responses, not display latency
or an isolated CPU benchmark. Gzip-compressed asciicast v2 events include input and output.
The video chooses the sample nearest each build's median independently, holds
both at their pre-input screen, then replays output on one shared elapsed clock.
Optional --slowdown applies identically to every panel and is visibly labeled.
An optional benchmark JSON adds a static summary card after the real UI replay;
its CPU/allocator measurements are labeled separately from terminal responses.
No render/encoding work runs during measurement. No application or API delays
are inserted. Identical short inter-sample settling periods are outside timing.
"""

import argparse
import codecs
import fcntl
import gzip
import hashlib
import importlib.util
import importlib.metadata
import json
import math
import os
from pathlib import Path
import platform
import pty
import queue
import re
import select
import shutil
import signal
import statistics
import struct
import subprocess
import tempfile
import termios
import threading
import time
from datetime import datetime, timezone

import pyte
from PIL import Image, ImageDraw, ImageFont

SPEC = importlib.util.spec_from_file_location("capture_demo", Path(__file__).with_name("capture-demo.py"))
DEMO = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DEMO)
CURSOR = re.compile(r"\x1b\[\?25[hl]")
NAME = re.compile(r"perf-config-\d{5,}")
ENVIRONMENT = {"TERM": "xterm-256color", "COLORTERM": "truecolor", "LANG": "C.UTF-8",
               "LC_ALL": "C.UTF-8", "TZ": "UTC", "GOMAXPROCS": "2"}


class PerfAPIHandler(DEMO.APIHandler):
    def do_GET(self):
        self.server.requests.append({"time": time.monotonic(), "method": "GET", "path": self.path})
        super().do_GET()

    def do_POST(self):
        self.server.requests.append({"time": time.monotonic(), "method": "POST", "path": self.path})
        super().do_POST()


def make_objects(count):
    metadata = {"resourceVersion": "100", "creationTimestamp": "2026-01-01T00:00:00Z"}
    items = [{"apiVersion": "v1", "kind": "Namespace", "metadata": dict(metadata, name="default", uid="perf-default"),
              "status": {"phase": "Active"}}]
    for i in range(count):
        name = f"perf-config-{i:05d}"
        items.append({"apiVersion": "v1", "kind": "ConfigMap",
                      "metadata": dict(metadata, name=name, namespace="default", uid=name),
                      "data": {"config.yaml": "enabled: true\n", "owner": "performance-demo"}})
    return items


def expected_names(count, query=""):
    return [f"perf-config-{i:05d}" for i in range(count) if query in f"perf-config-{i:05d}"]


def nearest_sample(samples):
    median = statistics.median(s["latency_ms"] for s in samples)
    return min(samples, key=lambda s: abs(s["latency_ms"] - median))


def table_matches(screen, expected, query):
    text = "\n".join(screen.display)
    title = re.search(r"configmaps\(default\)\[([\d,]+)\]([^\n]*)", text)
    if not title or int(title[1].replace(",", "")) != len(expected):
        return False
    if query and f"</{query}>" not in title[2]:
        return False
    if not query and "</" in title[2]:
        return False
    names = NAME.findall(text)
    return len(names) >= min(8, len(expected)) and names == expected[:len(names)]


class Capture:
    def __init__(self, binary, directory, port, cols, rows, environment, app="k9s"):
        root = Path(directory)
        kubeconfig = root / "kubeconfig"
        env = DEMO.isolated_runtime(root, app, kubeconfig, environment)
        (root / "config" / app / "config.yaml").write_text(
            "k9s:\n  skipLatestRevCheck: true\n  refreshRate: 2\n  liveViewAutoRefresh: false\n  ui:\n    splashless: true\n")
        kubeconfig.write_text(f"""apiVersion: v1
kind: Config
clusters:
- name: perf-local
  cluster:
    server: http://127.0.0.1:{port}
contexts:
- name: perf-local
  context:
    cluster: perf-local
    user: perf-local
    namespace: default
current-context: perf-local
users:
- name: perf-local
  user: {{}}
""")
        # Explicit local kubeconfig and one isolated app runtime select only the fixture.
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

        def control_terminal():
            os.setsid()
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)

        self.environment = environment
        self.app = app
        self.origin = time.monotonic()
        self.process = subprocess.Popen([str(binary), "--kubeconfig", str(kubeconfig), "--context", "perf-local",
                                         "--command", "configmaps default", "--readonly"],
                                        stdin=slave, stdout=slave, stderr=slave, env=env, preexec_fn=control_terminal)
        os.close(slave)
        self.screen = pyte.Screen(cols, rows)
        self.stream = pyte.Stream(self.screen)
        self.decoder = codecs.getincrementaldecoder("utf-8")("replace")
        self.pending = ""
        self.events = []
        self.received = queue.Queue()
        self.stopped = threading.Event()
        self.reader = threading.Thread(target=self.read_output, daemon=True)
        self.reader.start()

    def elapsed(self):
        return time.monotonic() - self.origin

    def read_output(self):
        # Drain the PTY independently of pyte, so rendering cannot backpressure it.
        while not self.stopped.is_set():
            if not select.select([self.master], [], [], .05)[0]:
                continue
            try:
                data = os.read(self.master, 65536)
            except OSError:
                break
            if not data:
                break
            self.received.put((self.elapsed(), data))
        self.received.put((self.elapsed(), None))

    def pump(self, timeout, predicate=None):
        try:
            stamp, data = self.received.get(timeout=max(.0001, timeout))
        except queue.Empty:
            return None
        if data is None:
            raise RuntimeError(f"{self.app} exited during capture:\n" + "\n".join(self.screen.display))
        text = self.decoder.decode(data)
        self.events.append([stamp, "o", text])
        self.pending += text
        # tcell starts each draw with HideCursor and ends with Hide/ShowCursor.
        # Only inspect completed segments; never accept partially written rows.
        consumed = 0
        match_time = None
        for marker in CURSOR.finditer(self.pending):
            self.stream.feed(self.pending[consumed:marker.end()])
            consumed = marker.end()
            if match_time is None and predicate is not None and predicate(self.screen):
                match_time = stamp
        self.pending = self.pending[consumed:]
        return match_time

    def wait(self, predicate, timeout=30):
        until = time.monotonic() + timeout
        while time.monotonic() < until:
            match = self.pump(min(.1, until - time.monotonic()), predicate)
            if match is not None:
                return match
        raise RuntimeError("Timed out waiting for a completed matching table:\n" + "\n".join(self.screen.display))

    def settle(self, seconds=.08):
        until = time.monotonic() + seconds
        while time.monotonic() < until:
            self.pump(min(.02, until - time.monotonic()))

    def key(self, value):
        stamp = self.elapsed()
        self.events.append([stamp, "i", value])
        os.write(self.master, value.encode())
        return stamp

    def sample(self, key, expected, query, operation, warmup, round_number, cast_name, timeout):
        start = self.key(key)
        end = self.wait(lambda s: table_matches(s, expected, query), timeout)
        return {"operation": operation, "input": key, "query": query, "expected_count": len(expected),
                "start": start, "end": end, "latency_ms": (end - start) * 1000,
                "warmup": warmup, "round": round_number, "cast": cast_name}

    def save(self, path, cols, rows):
        opener = gzip.open if path.suffix == ".gz" else open
        with opener(path, "wt", encoding="utf-8") as output:
            output.write(json.dumps({"version": 2, "width": cols, "height": rows,
                                     "title": f"{self.app} synthetic local API terminal capture", "env": self.environment}) + "\n")
            for event in sorted(self.events, key=lambda e: e[0]):
                output.write(json.dumps(event, ensure_ascii=False) + "\n")

    def close(self):
        if self.process.poll() is None:
            os.killpg(self.process.pid, signal.SIGTERM)
            try:
                self.process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                os.killpg(self.process.pid, signal.SIGKILL)
                self.process.wait()
        self.stopped.set()
        self.reader.join(timeout=1)
        os.close(self.master)


def binary_metadata(path, go):
    result = subprocess.run([go, "version", "-m", str(path)], text=True, capture_output=True, check=True)
    lines = result.stdout.splitlines()
    version = lines[0].rsplit(": ", 1)[-1]
    if not re.fullmatch(r"go\d+\.\d+(?:\.\d+)?(?:[a-z]+\d+)?", version):
        raise ValueError(f"Cannot determine Go version for {path}: {version}")
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return {"path": str(path), "sha256": digest.hexdigest(), "go_version": version, "go_build_info": lines[1:]}


def measure(args):
    binaries = {"upstream": args.upstream.resolve()}
    if args.before:
        binaries["before"] = args.before.resolve()
    binaries["fork"] = args.fork.resolve()
    metadata = {name: dict(binary_metadata(path, args.go_tool),
                           revision=getattr(args, name + "_revision"),
                           runtime=getattr(args, name + "_app")) for name, path in binaries.items()}
    for name, build in metadata.items():
        build["label"] = "K9+" if build["runtime"] == "k9plus" else {
            "upstream": "UPSTREAM", "before": "PREVIOUS FORK", "fork": "CUMULATIVE FORK"}[name]
    if len({value["go_version"] for value in metadata.values()}) != 1:
        raise ValueError("Compile every binary with the same Go version before comparing them")
    environment = dict(ENVIRONMENT, GOMAXPROCS=str(args.gomaxprocs))
    results = {"schema_version": 1, "captured_at": datetime.now(timezone.utc).isoformat(),
               "fixture": {"resource": "ConfigMap", "count": args.count, "namespace": "default", "query": "7",
                           "transport": "disposable localhost HTTP API", "artificial_api_delay_ms": 0},
               "terminal": {"columns": args.cols, "rows": args.rows}, "environment": environment,
               "host": {"platform": platform.platform(), "cpu_count": os.cpu_count()},
               "tools": {"python": platform.python_version(),
                         "pyte": importlib.metadata.version("pyte"),
                         "pillow": importlib.metadata.version("Pillow")},
               "method": {"rounds": args.rounds, "samples_per_round": args.samples, "warmups_per_round": args.warmups,
                          "settle_seconds": .08, "refresh_rate_seconds": 2, "order": "sequential; rotate build order on successive rounds",
                          "clock": "time.monotonic; PTY receipt before terminal emulation",
                          "completion": "tcell cursor boundary, exact table count, expected visible row prefix",
                          "metric": "Enter applies a prepared filter to completed matching table frame; startup recorded separately",
                          "limits": "Includes terminal output and scheduling. Not monitor paint latency or an isolated CPU benchmark."},
               "builds": {name: dict(meta, samples=[], startup=[]) for name, meta in metadata.items()}}
    DEMO.GROUPS = {"v1": [("namespaces", "Namespace", False), ("nodes", "Node", False),
                          ("pods", "Pod", True), ("configmaps", "ConfigMap", True)],
                   "authorization.k8s.io/v1": [("selfsubjectaccessreviews", "SelfSubjectAccessReview", False)]}
    DEMO.APIHandler = PerfAPIHandler
    api = DEMO.DemoAPI()
    api.requests = []
    api.objects = make_objects(args.count)
    api_thread = threading.Thread(target=api.serve_forever, daemon=True)
    api_thread.start()
    all_names, matching = expected_names(args.count), expected_names(args.count, "7")
    try:
        for round_number in range(args.rounds):
            names = list(binaries)
            offset = round_number % len(names)
            order = names[offset:] + names[:offset]
            for name in order:
                cast_name = f"{name}-{round_number + 1}.cast.gz"
                capture = None
                with tempfile.TemporaryDirectory(prefix=metadata[name]["runtime"] + "-perf-") as directory:
                    try:
                        capture = Capture(binaries[name], directory, api.server_port, args.cols, args.rows,
                                          environment, metadata[name]["runtime"])
                        ready = capture.wait(lambda s: table_matches(s, all_names, ""), args.timeout)
                        results["builds"][name]["startup"].append({"round": round_number, "latency_ms": ready * 1000, "cast": cast_name})
                        capture.settle()
                        for iteration in range(args.warmups + args.samples):
                            for preparation, before, expected, query, operation in [
                                    ("/7", all_names, matching, "7", "filter"),
                                    ("/", matching, all_names, "", "restore")]:
                                capture.key(preparation)
                                # Prompt/title changes alone do not apply the table filter.
                                # Verify the previous rows remain before timing Enter.
                                capture.wait(lambda s: table_matches(s, before, query), args.timeout)
                                capture.settle()
                                sample = capture.sample("\r", expected, query, operation, iteration < args.warmups,
                                                        round_number, cast_name, args.timeout)
                                results["builds"][name]["samples"].append(sample)
                                capture.settle()
                        (args.output / f"{name}-{round_number + 1}.screen.txt").write_text("\n".join(line.rstrip() for line in capture.screen.display).rstrip() + "\n")
                        print(f"Captured {name}, round {round_number + 1}", flush=True)
                    except Exception as error:
                        failure = args.output / f"{name}-{round_number + 1}-failure"
                        failure.mkdir(exist_ok=True)
                        for log in Path(directory).rglob("*.log"):
                            shutil.copyfile(log, failure / log.name)
                        (failure / "error.txt").write_text(str(error) + "\n")
                        (failure / "api-requests.json").write_text(json.dumps(api.requests, indent=2) + "\n")
                        raise
                    finally:
                        if capture is not None:
                            capture.save(args.output / cast_name, args.cols, args.rows)
                            capture.close()
    finally:
        api.stopped.set()
        api.shutdown()
        api.server_close()
        api_thread.join(timeout=1)
    (args.output / "api-requests.json").write_text(json.dumps(api.requests, indent=2) + "\n")
    for build in results["builds"].values():
        build["summary"] = {}
        for operation in ["filter", "restore"]:
            samples = [s for s in build["samples"] if s["operation"] == operation and not s["warmup"]]
            latencies = [s["latency_ms"] for s in samples]
            build["summary"][operation] = {"median_ms": statistics.median(latencies), "min_ms": min(latencies),
                                           "max_ms": max(latencies), "samples": len(samples),
                                           "video_sample": nearest_sample(samples)}
    path = args.output / "results.json"
    path.write_text(json.dumps(results, indent=2) + "\n")
    return path, results


def ansi_color(value, default):
    if value == "default":
        return default
    return DEMO.ANSI.get(value, "#" + value)


class Replay:
    def __init__(self, path, cols, rows, start):
        self.screen = pyte.Screen(cols, rows)
        self.stream = pyte.Stream(self.screen)
        opener = gzip.open if path.suffix == ".gz" else open
        with opener(path, "rt", encoding="utf-8") as source:
            self.events = [e for e in map(json.loads, source.read().splitlines()[1:]) if e[1] == "o"]
        self.index = 0
        self.start = start
        self.advance(0)

    def advance(self, elapsed):
        changed = False
        while self.index < len(self.events) and self.events[self.index][0] <= self.start + elapsed:
            self.stream.feed(self.events[self.index][2])
            self.index += 1
            changed = True
        return changed


def rasterize(screen, font, bold, cw=7, ch=14):
    image = Image.new("RGB", (screen.columns * cw, screen.lines * ch), "#10121a")
    draw = ImageDraw.Draw(image)
    for y in range(screen.lines):
        for x in range(screen.columns):
            cell = screen.buffer[y][x]
            fg, bg = ansi_color(cell.fg, "#e5e5e5"), ansi_color(cell.bg, "#10121a")
            if cell.reverse:
                fg, bg = bg, fg
            px, py = x * cw, y * ch
            if bg != "#10121a":
                draw.rectangle((px, py, px + cw - 1, py + ch - 1), fill=bg)
            if cell.data.strip():
                draw.text((px, py - 1), cell.data, font=bold if cell.bold else font, fill=fg)
    return image


def benchmark_card(path, builds, labels, width, height):
    raw = path.read_bytes()
    data = json.loads(raw)
    metrics = [
        ("Table rebuild + draw", "PerformanceTableRefresh/10000", "ns_per_op"),
        ("Remove 5,000 rows", "TableDataDelete10K/Half", "ns_per_op"),
        ("Customize columns", "PerformanceCustomize", "ns_per_op"),
        ("Snapshot bytes/op", "RowEventsClone10K", "bytes_per_op"),
    ]
    font_root = Path("/usr/share/fonts/truetype/dejavu")
    scale = min(1, width / 2172)
    title = ImageFont.truetype(str(font_root / "DejaVuSans-Bold.ttf"), round(42 * scale))
    text = ImageFont.truetype(str(font_root / "DejaVuSans.ttf"), round(27 * scale))
    header = ImageFont.truetype(str(font_root / "DejaVuSans-Bold.ttf"), round(25 * scale))
    number = ImageFont.truetype(str(font_root / "DejaVuSansMono.ttf"), round(33 * scale))
    small = ImageFont.truetype(str(font_root / "DejaVuSans.ttf"), round(22 * scale))
    image = Image.new("RGB", (width, height), "#10121a")
    draw = ImageDraw.Draw(image)
    left = round(width * .045)
    draw.text((left, 40), "Measured CPU work on 10,000 rows", font=title, fill="#f0f3f9")
    draw.text((left, 110), f"Separate CPU microbenchmarks · {data['rounds']}-round medians · shared host", font=text, fill="#b7c1d3")
    draw.text((left, 155), f"{data['go']} · GOMAXPROCS={data['gomaxprocs']} · lower is better", font=small, fill="#a3acbd")
    centers = {name: width * (.4 + .56 * (i + .5) / len(builds)) for i, name in enumerate(builds)}
    for name in builds:
        draw.text((centers[name], 225), labels[name], anchor="mm", font=header, fill="#e6eefc")
    values = {}
    for row, (label, key, metric) in enumerate(metrics):
        y = 292 + row * 68
        draw.line((left, y - 29, width - left, y - 29), fill="#313b4e", width=1)
        draw.text((left, y), label, anchor="lm", font=text, fill="#e6eefc")
        values[key] = {}
        for name in builds:
            samples = data["samples"][key][name]
            if len(samples) != data["rounds"]:
                raise ValueError(f"Expected {data['rounds']} benchmark rounds for {key}/{name}")
            median = statistics.median(sample[metric] for sample in samples)
            values[key][name] = {"metric": metric, "median": median}
            formatted = f"{median / 1e6:,.2f} ms" if metric == "ns_per_op" else f"{median:,.0f}"
            draw.text((centers[name], y), formatted, anchor="mm", font=number,
                      fill="#79d9ae" if name == "fork" else "#e6eefc")
    draw.text((left, height - 76), "Excludes API and terminal I/O; not overall app speed", font=text, fill="#e8d185")
    digest = hashlib.sha256(raw).hexdigest()
    draw.text((left, height - 34), f"Source: {path.parent.name}/{path.name} · SHA-256 {digest[:16]}", font=small, fill="#a3acbd")
    provenance = {"source": str(path), "sha256": digest, "duration_seconds": 4,
                  "rounds": data["rounds"], "gomaxprocs": data["gomaxprocs"],
                  "scope": "Separate CPU microbenchmarks; excludes API and terminal I/O; not overall app speed",
                  "values": values}
    return image, provenance


def render(path, results, slowdown, fps, benchmarks=None):
    output = path.parent
    labels = {"upstream": "UPSTREAM", "fork": "CUMULATIVE FORK", "before": "PREVIOUS FORK"}
    # Older result files keep their original labels when re-rendered.
    labels.update({name: build["label"] for name, build in results["builds"].items() if "label" in build})
    builds = list(results["builds"])
    cols, rows = results["terminal"]["columns"], results["terminal"]["rows"]
    panel_w, term_h, gap = cols * 7, rows * 14, 18
    width, height = len(builds) * (panel_w + gap) + gap, term_h + 248
    width += width % 2
    height += height % 2
    font_root = Path("/usr/share/fonts/truetype/dejavu")
    mono = ImageFont.truetype(str(font_root / "DejaVuSansMono.ttf"), 11)
    mono_bold = ImageFont.truetype(str(font_root / "DejaVuSansMono-Bold.ttf"), 11)
    title_font = ImageFont.truetype(str(font_root / "DejaVuSans-Bold.ttf"), 23)
    label_font = ImageFont.truetype(str(font_root / "DejaVuSans-Bold.ttf"), 16)
    small = ImageFont.truetype(str(font_root / "DejaVuSans.ttf"), 14)
    video_path = output / "comparison.mp4"
    command = ["ffmpeg", "-y", "-loglevel", "error", "-f", "rawvideo", "-pix_fmt", "rgb24",
               "-s", f"{width}x{height}", "-r", str(fps), "-i", "-", "-an", "-c:v", "libx264",
               "-preset", "veryfast", "-crf", "26", "-pix_fmt", "yuv420p", "-movflags", "+faststart",
               "-threads", "2", str(video_path)]
    encoder = subprocess.Popen(command, stdin=subprocess.PIPE)
    selected = {}
    card_provenance = None
    try:
        for operation in ["filter", "restore"]:
            samples = {name: results["builds"][name]["summary"][operation]["video_sample"] for name in builds}
            selected[operation] = samples
            replays = {name: Replay(output / sample["cast"], cols, rows, sample["start"]) for name, sample in samples.items()}
            panels = {name: rasterize(replay.screen, mono, mono_bold) for name, replay in replays.items()}
            maximum = max(s["latency_ms"] for s in samples.values()) / 1000
            duration = 1 + maximum * slowdown + 1.5
            for frame in range(math.ceil(duration * fps)):
                video_time = frame / fps
                elapsed = max(0, video_time - 1) / slowdown
                image = Image.new("RGB", (width, height), "#10121a")
                draw = ImageDraw.Draw(image)
                title = f"Filter {results['fixture']['count']:,} ConfigMaps" if operation == "filter" else f"Restore all {results['fixture']['count']:,} rows"
                draw.text((gap, 16), title, font=title_font, fill="#f0f3f9")
                speed = "REAL TIME · 1×" if slowdown == 1 else f"UNIFORM SLOW MOTION · {slowdown:g}× slower"
                draw.text((gap, 50), f"{speed}   |   Sequential captures · identical input and fixture", font=small, fill="#a3acbd")
                for i, name in enumerate(builds):
                    x = gap + i * (panel_w + gap)
                    sample = samples[name]
                    if replays[name].advance(min(elapsed, sample["latency_ms"] / 1000)):
                        panels[name] = rasterize(replays[name].screen, mono, mono_bold)
                    draw.text((x, 84), labels[name], font=label_font, fill="#e6eefc")
                    median = results["builds"][name]["summary"][operation]["median_ms"]
                    count = results["builds"][name]["summary"][operation]["samples"]
                    build = results["builds"][name]
                    revision = (build.get("revision") or "revision unspecified")
                    revision = re.sub(r"\b([a-f0-9]{12})[a-f0-9]{28}\b", r"\1", revision)
                    draw.text((x, 108), f"{revision[:42]} · binary {build['sha256'][:10]}", font=small, fill="#a3acbd")
                    draw.text((x, 132), f"Median {median:.1f} ms · {count} warm samples", font=small, fill="#a3acbd")
                    image.paste(panels[name], (x, 162))
                    done = elapsed >= sample["latency_ms"] / 1000
                    if video_time < 1:
                        status = 'Next input: Enter (apply /7)' if operation == "filter" else "Next input: Enter (clear filter)"
                    elif done:
                        status = f"Matching frame received · {sample['latency_ms']:.1f} ms"
                    else:
                        status = f"Input sent · waiting · {elapsed * 1000:.0f} ms"
                    draw.text((x, 173 + term_h), status, font=small, fill="#79d9ae" if done and video_time >= 1 else "#e8d185")
                draw.text((gap, 211 + term_h), "Real terminal output · median-nearest samples · timings measure PTY receipt, not monitor paint", font=small, fill="#a3acbd")
                if frame == math.ceil(duration * fps) - 1 and operation == "restore":
                    image.save(output / "comparison.png")
                encoder.stdin.write(image.tobytes())
        if benchmarks is not None:
            card, card_provenance = benchmark_card(benchmarks, builds, labels, width, height)
            card.save(output / "benchmark-summary.png")
            for _ in range(4 * fps):
                encoder.stdin.write(card.tobytes())
        encoder.stdin.close()
        if encoder.wait() != 0:
            raise RuntimeError("ffmpeg failed to encode the comparison")
    except BaseException:
        encoder.kill()
        encoder.wait()
        raise
    results["video"] = {"file": video_path.name, "fps": fps, "uniform_slowdown": slowdown,
                        "selection": "nearest sample to each operation/build median, independently",
                        "sample_alignment": "input timestamp is zero for all panels; 1-second pre-input hold; completed panels hold their final screen",
                        "samples": selected, "benchmark_card": card_provenance, "bytes": video_path.stat().st_size}
    path.write_text(json.dumps(results, indent=2) + "\n")
    print(f"Rendered {video_path} ({video_path.stat().st_size:,} bytes)", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--upstream", type=Path)
    parser.add_argument("--fork", type=Path)
    parser.add_argument("--before", type=Path, help="optional third binary; captured and rendered as previous fork")
    for name in ["upstream", "before", "fork"]:
        parser.add_argument("--" + name + "-revision", help="exact git revision, plus dirty-tree description if applicable")
        parser.add_argument("--" + name + "-app", choices=["k9plus", "k9s"],
                            default="k9plus" if name == "fork" else "k9s", help="runtime environment and storage namespace")
    parser.add_argument("--output", type=Path, default=Path("assets/k9plus/performance"))
    parser.add_argument("--render-only", type=Path, metavar="RESULTS_JSON")
    parser.add_argument("--no-video", action="store_true")
    parser.add_argument("--benchmarks", type=Path, help="offline summary-card source; defaults to sibling benchmarks/results.json if present")
    parser.add_argument("--count", type=int, default=10000)
    parser.add_argument("--samples", type=int, default=5)
    parser.add_argument("--warmups", type=int, default=2)
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--cols", type=int, default=100)
    parser.add_argument("--rows", type=int, default=28)
    parser.add_argument("--gomaxprocs", type=int, default=2)
    parser.add_argument("--timeout", type=float, default=60)
    parser.add_argument("--slowdown", type=float, default=1, help="uniform playback slowdown for ALL panels; visibly labeled")
    parser.add_argument("--fps", type=int, default=25)
    parser.add_argument("--go-tool", default=shutil.which("go"), help="Go binary used to inspect compiler/build metadata")
    args = parser.parse_args()
    if min(args.samples, args.rounds, args.gomaxprocs, args.fps) < 1 or args.warmups < 0 or args.count < 10:
        parser.error("samples, rounds, GOMAXPROCS and fps must be positive; count >= 10 and warmups >= 0")
    if args.slowdown < 1 or args.timeout <= 0 or args.cols < 80 or args.rows < 20:
        parser.error("slowdown >= 1, timeout > 0, columns >= 80 and rows >= 20 are required")
    if args.render_only:
        path = args.render_only.resolve()
        results = json.loads(path.read_text())
    else:
        if not args.upstream or not args.fork or not args.go_tool:
            parser.error("measurement requires --upstream, --fork and Go on PATH (or --go-tool)")
        args.output.mkdir(parents=True, exist_ok=True)
        path, results = measure(args)
    for name, build in results["builds"].items():
        print(name + ": " + ", ".join(f"{op} median {s['median_ms']:.2f} ms" for op, s in build["summary"].items()))
    if not args.no_video:
        benchmarks = args.benchmarks or path.parent / "benchmarks/results.json"
        render(path, results, args.slowdown, args.fps, benchmarks if benchmarks.exists() else None)


if __name__ == "__main__":
    main()

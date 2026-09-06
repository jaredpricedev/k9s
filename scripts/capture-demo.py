#!/usr/bin/env python3
"""Capture real K9s screens against a disposable localhost Kubernetes API.

Requires Python 3, Pillow and pyte; no cluster, cmctl, or TLS private keys.
  python scripts/capture-demo.py --binary /tmp/k9s-cert-demo
The fixtures use dates relative to capture time so health states remain useful.
Only terminal cells emitted by K9s are rasterized; no UI text is added.
"""

import argparse
import codecs
import fcntl
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
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

import pyte
from PIL import Image, ImageDraw, ImageFont

COLS, ROWS = 202, 32
GROUPS = {
    "v1": [("namespaces", "Namespace", False), ("nodes", "Node", False),
           ("pods", "Pod", True), ("secrets", "Secret", True), ("events", "Event", True)],
    "apps/v1": [("deployments", "Deployment", True)],
    "source.toolkit.fluxcd.io/v1": [("ocirepositories", "OCIRepository", True),
        ("gitrepositories", "GitRepository", True), ("helmrepositories", "HelmRepository", True),
        ("helmcharts", "HelmChart", True), ("buckets", "Bucket", True)],
    "kustomize.toolkit.fluxcd.io/v1": [("kustomizations", "Kustomization", True)],
    "helm.toolkit.fluxcd.io/v2": [("helmreleases", "HelmRelease", True)],
    "cert-manager.io/v1": [("certificates", "Certificate", True),
        ("certificaterequests", "CertificateRequest", True), ("issuers", "Issuer", True),
        ("clusterissuers", "ClusterIssuer", False)],
    "acme.cert-manager.io/v1": [("orders", "Order", True), ("challenges", "Challenge", True)],
    "authorization.k8s.io/v1": [("selfsubjectaccessreviews", "SelfSubjectAccessReview", False)],
    "metrics.k8s.io/v1beta1": [("pods", "PodMetrics", True), ("nodes", "NodeMetrics", False)],
}


def fixtures():
    now = datetime.now(timezone.utc).replace(microsecond=0)
    stamp = lambda days: (now + timedelta(days=days)).isoformat().replace("+00:00", "Z")
    objects = []

    def add(api, kind, name, namespace="", spec=None, status=None, owner=None):
        obj = {"apiVersion": api, "kind": kind, "metadata": {
            "name": name, "uid": f"demo-{kind.lower()}-{namespace}-{name}",
            "resourceVersion": "100", "generation": 1, "creationTimestamp": stamp(-12)}}
        if namespace:
            obj["metadata"]["namespace"] = namespace
        if spec is not None:
            obj["spec"] = spec
        if status is not None:
            obj["status"] = status
        if owner:
            obj["metadata"]["ownerReferences"] = [{"apiVersion": owner["apiVersion"],
                "kind": owner["kind"], "name": owner["metadata"]["name"],
                "uid": owner["metadata"]["uid"], "controller": True}]
        objects.append(obj)
        return obj

    def condition(kind="Ready", value="True", reason="Ready", message="Reconciliation succeeded"):
        return {"type": kind, "status": value, "reason": reason, "message": message,
                "observedGeneration": 1, "lastTransitionTime": stamp(-1)}

    for ns in ["default", "flux-system", "apps", "monitoring"]:
        add("v1", "Namespace", ns, status={"phase": "Active"})
    source_api = "source.toolkit.fluxcd.io/v1"
    for name, rev in [("apps", "c34d"), ("platform", "a12b")]:
        add(source_api, "OCIRepository", name, "flux-system",
            {"interval": "5m", "url": f"oci://registry.example.test/{name}"},
            {"conditions": [condition()], "artifact": {"revision": f"2.0.0@sha256:{rev}"}})
    add(source_api, "HelmRepository", "monitoring", "flux-system", {"url": "https://charts.example.test"},
        {"conditions": [condition()], "artifact": {"revision": "sha256:e56f"}})
    for name, state, source in [("apps", "Ready", "apps"), ("infrastructure", "Ready", "platform"),
                               ("monitoring", "Reconciling", "platform"), ("payments", "Failed", "apps"),
                               ("storage", "Suspended", "platform")]:
        conditions = [condition()]
        if state == "Failed":
            conditions = [condition(value="False", reason="HealthCheckFailed", message="Deployment payments/api is not ready: waiting for available replicas")]
        elif state == "Reconciling":
            conditions = [condition("Reconciling", reason="Progressing", message="Applying updated manifests")]
        spec = {"interval": "5m", "path": f"./clusters/demo/{name}",
                "sourceRef": {"kind": "OCIRepository", "name": source}, "suspend": state == "Suspended"}
        if name == "apps":
            spec["dependsOn"] = [{"name": "infrastructure"}]
        add("kustomize.toolkit.fluxcd.io/v1", "Kustomization", name, "flux-system", spec,
            {"observedGeneration": 1, "conditions": conditions, "lastAppliedRevision": "2.0.0@sha256:c34d"})
    add("helm.toolkit.fluxcd.io/v2", "HelmRelease", "grafana", "monitoring",
        {"interval": "5m", "chartRef": {"kind": "OCIRepository", "name": "apps", "namespace": "flux-system"}},
        {"conditions": [condition()], "lastAppliedRevision": "10.0.0"})
    issuer = {"name": "letsencrypt", "kind": "ClusterIssuer", "group": "cert-manager.io"}
    add("cert-manager.io/v1", "ClusterIssuer", "letsencrypt", spec={"acme": {
        "email": "demo@example.test", "server": "https://acme.example.test/directory",
        "privateKeySecretRef": {"name": "acme-account"}}},
        status={"conditions": [condition(message="ACME account registered")]})
    selected = None
    for name, ns, before, after, renewal in [
        ("api-tls", "apps", -30, 60, 30), ("legacy-tls", "apps", -91, -1, -31),
        ("edge-tls", "apps", -85, 5, -25), ("web-tls", "apps", -65, 25, -5),
        ("new-service-tls", "apps", None, None, None), ("grafana-tls", "monitoring", -20, 70, 40)]:
        status = {"conditions": [condition(message="Certificate is up to date and has not expired")]}
        if before is not None:
            status.update(notBefore=stamp(before), notAfter=stamp(after), renewalTime=stamp(renewal), revision=1)
        else:
            status["conditions"] = [condition("Issuing", reason="DoesNotExist",
                message="Issuing certificate as Secret does not exist; waiting for DNS-01 validation")]
        obj = add("cert-manager.io/v1", "Certificate", name, ns,
            {"secretName": name, "issuerRef": issuer, "dnsNames": [name.removesuffix("-tls") + ".example.test"]}, status)
        if name == "api-tls":
            selected = obj
    request = add("cert-manager.io/v1", "CertificateRequest", "api-tls-1", "apps",
        {"issuerRef": issuer}, {"conditions": [condition("Approved"), condition(message="Certificate issued successfully")]}, selected)
    order = add("acme.cert-manager.io/v1", "Order", "api-tls-1-3421", "apps",
        {"issuerRef": issuer, "dnsNames": ["api.example.test"]}, {"state": "valid"}, request)
    add("acme.cert-manager.io/v1", "Challenge", "api-tls-1-3421-8051", "apps",
        {"issuerRef": issuer, "dnsName": "api.example.test", "type": "DNS-01"},
        {"state": "valid", "presented": True, "processing": False}, order)
    return objects


class DemoAPI(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self):
        super().__init__(("127.0.0.1", 0), APIHandler)
        self.objects = fixtures()
        self.stopped = threading.Event()


class APIHandler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, obj):
        body = json.dumps(obj).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        # Client-go can send protobuf here; the demo only needs the access result.
        self.rfile.read(int(self.headers.get("Content-Length", 0)))
        if self.path.startswith("/apis/authorization.k8s.io/"):
            self.reply({"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview",
                        "status": {"allowed": True}})
        else:
            self.send_error(405, "Demo API does not mutate resources")

    def do_GET(self):
        parsed = urlparse(self.path)
        path, query = parsed.path.rstrip("/"), parse_qs(parsed.query)
        if path == "/version":
            return self.reply({"major": "1", "minor": "34", "gitVersion": "v1.34.0", "platform": "linux/amd64"})
        if path == "/api":
            return self.reply({"kind": "APIVersions", "apiVersion": "v1", "versions": ["v1"]})
        if path == "/apis":
            groups = []
            for gv in GROUPS:
                if gv == "v1":
                    continue
                group, version = gv.split("/")
                v = {"groupVersion": gv, "version": version}
                groups.append({"name": group, "versions": [v], "preferredVersion": v})
            return self.reply({"kind": "APIGroupList", "apiVersion": "v1", "groups": groups})
        parts = path.strip("/").split("/")
        gv, rest = ("v1", parts[2:]) if parts[:2] == ["api", "v1"] else ("/".join(parts[1:3]), parts[3:])
        resources = GROUPS.get(gv, [])
        if not rest:
            return self.reply({"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": gv,
                "resources": [{"name": n, "singularName": k.lower(), "kind": k, "namespaced": ns,
                               "verbs": ["get", "list", "watch", "patch"]} for n, k, ns in resources]})
        namespace = ""
        if rest[0] == "namespaces" and len(rest) >= 3:
            namespace, rest = rest[1], rest[2:]
        resource = rest[0]
        kind = next((k for n, k, _ in resources if n == resource), "List")
        items = [o for o in self.server.objects if o["apiVersion"] == gv and o["kind"] == kind
                 and (not namespace or o["metadata"].get("namespace") == namespace)]
        selector = query.get("fieldSelector", [""])[0]
        if selector.startswith("metadata.name="):
            items = [o for o in items if o["metadata"]["name"] == selector.split("=", 1)[1]]
        if len(rest) > 1:
            obj = next((o for o in items if o["metadata"]["name"] == rest[1]), None)
            if obj:
                return self.reply(obj)
            return self.send_error(404, "Fixture not found")
        if query.get("watch") == ["true"]:
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            try:
                if query.get("sendInitialEvents") == ["true"]:
                    for item in items:
                        self.wfile.write(json.dumps({"type": "ADDED", "object": item}).encode() + b"\n")
                    self.wfile.write(json.dumps({"type": "BOOKMARK", "object": {
                        "apiVersion": gv, "kind": kind, "metadata": {"resourceVersion": "100",
                        "annotations": {"k8s.io/initial-events-end": "true"}}}}).encode() + b"\n")
                    self.wfile.flush()
                self.server.stopped.wait(90)
            except (BrokenPipeError, ConnectionResetError):
                pass
            return
        self.reply({"apiVersion": gv, "kind": kind + "List", "metadata": {"resourceVersion": "100"}, "items": items})


ANSI = {"black": "#000000", "red": "#cd0000", "green": "#00cd00", "brown": "#cdcd00",
        "blue": "#0000ee", "magenta": "#cd00cd", "cyan": "#00cdcd", "white": "#e5e5e5",
        "brightblack": "#7f7f7f", "brightred": "#ff0000", "brightgreen": "#00ff00",
        "brightbrown": "#ffff00", "brightblue": "#5c5cff", "brightmagenta": "#ff00ff",
        "brightcyan": "#00ffff", "brightwhite": "#ffffff"}


def rasterize(screen, target):
    regular = ImageFont.truetype("/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf", 16)
    bold = ImageFont.truetype("/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf", 16)
    image = Image.new("RGB", (COLS * 10 + 36, ROWS * 21 + 36), "#101018")
    draw = ImageDraw.Draw(image)

    def color(value, default):
        return default if value == "default" else ANSI.get(value, "#" + value)

    for y in range(ROWS):
        for x in range(COLS):
            cell = screen.buffer[y][x]
            fg, bg = color(cell.fg, "#e5e5e5"), color(cell.bg, "#000000")
            if cell.reverse:
                fg, bg = bg, fg
            px, py = 18 + x * 10, 18 + y * 21
            draw.rectangle((px, py, px + 9, py + 20), fill=bg)
            draw.text((px, py - 1), cell.data, font=bold if cell.bold else regular, fill=fg)
            if cell.underscore:
                draw.line((px, py + 19, px + 9, py + 19), fill=fg)
    image.save(target)


class Terminal:
    def __init__(self, binary, directory, port):
        root = Path(directory)
        for name in ["config", "data", "state", "cache"]:
            (root / name / "k9s").mkdir(parents=True)
        (root / "config/k9s/config.yaml").write_text("k9s:\n  skipLatestRevCheck: true\n  refreshRate: 1\n  ui:\n    splashless: true\n")
        kubeconfig = root / "kubeconfig"
        kubeconfig.write_text(f"""apiVersion: v1
kind: Config
clusters:
- name: demo-cluster
  cluster:
    server: http://127.0.0.1:{port}
contexts:
- name: demo-dev
  context:
    cluster: demo-cluster
    user: demo-user
    namespace: default
current-context: demo-dev
users:
- name: demo-user
  user: {{}}
""")
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))
        env = dict(os.environ, TERM="xterm-256color", COLORTERM="truecolor", KUBECONFIG=str(kubeconfig))
        for name in ["config", "data", "state", "cache"]:
            env["XDG_" + name.upper() + "_HOME"] = str(root / name)

        def control_terminal():
            os.setsid()
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)

        self.process = subprocess.Popen([str(binary), "--kubeconfig", str(kubeconfig), "--command", "flux all"],
            stdin=slave, stdout=slave, stderr=slave, env=env, preexec_fn=control_terminal)
        os.close(slave)
        self.screen = pyte.Screen(COLS, ROWS)
        self.stream = pyte.Stream(self.screen)
        self.decoder = codecs.getincrementaldecoder("utf-8")("replace")

    def drain(self, seconds=1):
        until = time.monotonic() + seconds
        while time.monotonic() < until:
            if select.select([self.master], [], [], min(.1, max(0, until - time.monotonic())))[0]:
                try:
                    data = os.read(self.master, 65536)
                except OSError:
                    break
                if not data:
                    break
                self.stream.feed(self.decoder.decode(data))
        if self.process.poll() is not None:
            raise RuntimeError("K9s exited before capture:\n" + "\n".join(self.screen.display))

    def keys(self, text, wait=1):
        os.write(self.master, text.encode())
        self.drain(wait)

    def command(self, text):
        self.keys(":" + text + "\r", 2)

    def capture(self, output, name, expected):
        text = "\n".join(self.screen.display)
        for phrase in expected:
            if phrase not in text:
                raise RuntimeError(f"{name}: expected {phrase!r} in terminal:\n{text}")
        rasterize(self.screen, output / (name + ".png"))
        print(name + ".png", flush=True)

    def close(self):
        if self.process.poll() is None:
            os.killpg(self.process.pid, signal.SIGTERM)
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(self.process.pid, signal.SIGKILL)
                self.process.wait()
        os.close(self.master)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", type=Path, default=Path("assets/screenshots"))
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    api = DemoAPI()
    threading.Thread(target=api.serve_forever, daemon=True).start()
    terminal = None
    try:
        with tempfile.TemporaryDirectory(prefix="k9s-capture-") as directory:
            terminal = Terminal(args.binary.resolve(), directory, api.server_port)
            try:
                terminal.drain(5)
                terminal.capture(args.output, "flux-overview", ["flux(all)", "Reconciling", "Suspended", "Failed"])
                terminal.command("kustomizations flux-system")
                terminal.capture(args.output, "flux-kustomizations", ["kustomizations(flux-system)", "STATUS", "REVISION"])
                terminal.keys("g")
                terminal.capture(args.output, "flux-relationships", ["Flux relationships", "Source:", "Dependency:"])
                terminal.keys("\x1b")
                terminal.keys("R")
                terminal.capture(args.output, "flux-reconcile", ["Confirm Flux action", "reconcile Kustomization flux-system/apps in context demo-dev?"])
                terminal.keys("\x1b")
                terminal.command("certificates all")
                terminal.capture(args.output, "certificates-overview", ["certificates(all)", "Expired", "Expiring", "Renewal Due", "Issuing", "Ready"])
                terminal.command("certificates apps")
                # Expiration sort puts api-tls fourth, after the three warnings.
                terminal.keys("jjj")
                terminal.keys("g")
                terminal.keys("\x1b", 2)
                terminal.keys("g")
                terminal.capture(args.output, "certificate-relationships", ["Certificate relationships", "ClusterIssuer:", "Secret: apps/api-tls", "CertificateRequest: apps/api-tls-1"])
                terminal.keys("\x1b")
                terminal.keys("i")
                terminal.capture(args.output, "certificate-status", ["Certificate Status", "apps/api-tls", "DNS NAMES", "api.example.test"])
            finally:
                terminal.close()
    finally:
        api.stopped.set()
        api.shutdown()
        api.server_close()


if __name__ == "__main__":
    main()

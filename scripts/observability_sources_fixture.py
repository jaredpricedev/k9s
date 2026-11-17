#!/usr/bin/env python3
"""Synthetic Prometheus, Alertmanager and Grafana answers for the study fixture.

Everything is generated relative to the fixture start time and matches the
checkout-api story in scripts/operator-study-fixture.py: a ConfigMap edit 14
minutes before start, the revision 2 rollout 12 minutes before start (image
v1 -> v2, memory limit 512Mi -> 256Mi), crash-looping from 10 minutes before
start. No real system is contacted and no real data is included.

The PromQL evaluator understands only the canonical text produced by the
builder in internal/observability/promql.go: selectors with = != =~ !~
matchers, rate/increase/{max,min,avg}_over_time over a range selector,
sum/avg/max/min/count with an optional by (...) clause, topk,
histogram_quantile, number literals and parenthesised + - * / with one-to-one
matching on identical label sets.
"""

import math
import re
import time

MI = 1024 * 1024
GI = 1024 * MI

CONFIG_EDIT = 14 * 60     # seconds before fixture start
ROLLOUT = 12 * 60
CRASH_BEGAN = 10 * 60
NODE_B_SILENT = 20 * 60

NAMESPACE = "monitoring"
SERVICES = {"prometheus": "9090", "alertmanager": "9093", "grafana": "3000"}
DATASOURCE_UID = "prom-main"

OLD_POD = "checkout-api-6c8d7f9b5-k4m2t"       # revision 1, removed by the rollout
STABLE_POD = "checkout-api-0"                  # keeps serving throughout
NEW_POD = "checkout-api-7d9f8c6b5d-x2x9q"      # revision 2, crash-looping
PULL_POD = "checkout-api-7d9f8c6b5d-p8wm"      # revision 2, image never pulled
BUCKETS = ["0.05", "0.1", "0.25", "0.5", "1", "2.5", "5", "+Inf"]
# Cumulative share of requests at or below each bucket bound.
FAST = [0.55, 0.80, 0.96, 0.99, 0.998, 1.0, 1.0, 1.0]       # p95 near 0.24s
SLOW = [0.05, 0.12, 0.30, 0.48, 0.66, 0.93, 0.99, 1.0]      # p95 near 3.3s
STRAINED = [0.35, 0.60, 0.85, 0.95, 0.985, 0.998, 1.0, 1.0]  # p95 near 0.5s


class Series:
    def __init__(self, name, labels, fn):
        self.labels = dict(labels, __name__=name)
        self.fn = fn


class Metrics:
    """Deterministic series as functions of absolute time."""

    def __init__(self, start):
        self.t0 = float(start)
        self.series = []
        self._build()

    # -- constructors -------------------------------------------------
    def gauge(self, name, labels, fn, begin=None, end=None):
        def value(t):
            if (begin is not None and t < begin) or (end is not None and t >= end):
                return None
            return fn(t)
        self.series.append(Series(name, labels, value))

    def counter(self, name, labels, pieces, begin, end=None):
        """pieces is [(from_time, per_second_rate)], ascending; the counter is
        the integral of the rate since begin and never resets."""
        def value(t):
            if t < begin or (end is not None and t >= end):
                return None
            total = 0.0
            for index, (since, rate) in enumerate(pieces):
                lo = max(since, begin)
                hi = pieces[index + 1][0] if index + 1 < len(pieces) else t
                hi = min(hi, t)
                if hi > lo:
                    total += (hi - lo) * rate
            return total
        self.series.append(Series(name, labels, value))

    # -- data ----------------------------------------------------------
    def _build(self):
        t0 = self.t0
        rollout, crash = t0 - ROLLOUT, t0 - CRASH_BEGAN
        long_ago = t0 - 3 * 24 * 3600
        apps = {"namespace": "apps"}

        def wobble(base, spread, period, phase=0.0):
            return lambda t: base + spread * math.sin((t - t0) / period + phase)

        def sawtooth(t):
            # Revision 2 climbs to its 256Mi limit about every two minutes,
            # is restarted, and starts again from a small footprint.
            position = ((t - crash) % 120.0) / 120.0 if t >= crash else (t - rollout) / (crash - rollout)
            return (60 + 194 * min(1.0, position * 1.15)) * MI

        def restarts(t):
            # Six restarts by fixture start (matches the Pod status), then one
            # more every five minutes.
            if t < crash:
                return 0.0
            if t < t0:
                return float(min(6, 1 + int((t - crash) / 100.0)))
            return 6.0 + int((t - t0) / 300.0)

        pods = [  # pod, node, begin, end, memory fn, cpu pieces, memory limit
            (OLD_POD, "fixture-node", long_ago, rollout + 60, wobble(182 * MI, 6 * MI, 300), [(long_ago, 0.06)], 512 * MI),
            (STABLE_POD, "fixture-node", long_ago, None, wobble(188 * MI, 7 * MI, 280, 1.0),
             [(long_ago, 0.06), (rollout, 0.11)], 512 * MI),
            (NEW_POD, "fixture-node", rollout, None, sawtooth, [(rollout, 0.34)], 256 * MI),
        ]
        for pod, node, begin, end, memory, cpu, limit in pods:
            labels = dict(apps, pod=pod, container="api", node=node)
            self.gauge("container_memory_working_set_bytes", labels, memory, begin, end)
            self.counter("container_cpu_usage_seconds_total", labels, cpu, begin, end)
            # cAdvisor reports network per pod sandbox, without a container.
            # Transmit follows the request rate (see traffic below).
            sandbox = dict(apps, pod=pod, interface="eth0", node=node)
            rx, tx = (2600.0, 9000.0) if pod == NEW_POD else (4200.0, 21000.0)
            self.counter("container_network_receive_bytes_total", sandbox, [(begin, rx)], begin, end)
            self.counter("container_network_transmit_bytes_total", sandbox, [(begin, tx)], begin, end)
            self._kube_pod(pod, "apps", node, "api", begin, end, limit, 128 * MI, 0.1,
                           restarts if pod == NEW_POD else (lambda t: 0.0),
                           "ReplicaSet", "checkout-api-7d9f8c6b5d" if pod == NEW_POD else "checkout-api-6c8d7f9b5")
        self._kube_pod(PULL_POD, "apps", "fixture-node", "api", rollout, None, 256 * MI, 128 * MI, 0.1,
                       lambda t: 0.0, "ReplicaSet", "checkout-api-7d9f8c6b5d")

        # One healthy, flat workload for comparison, and restart counts for
        # the base fixture's other failing Pods.
        worker = {"namespace": "ops", "pod": "daily-worker", "container": "worker", "node": "fixture-node"}
        self.gauge("container_memory_working_set_bytes", worker, wobble(96 * MI, 2 * MI, 400), long_ago)
        self.counter("container_cpu_usage_seconds_total", worker, [(long_ago, 0.02)], long_ago)
        self._kube_pod("daily-worker", "ops", "fixture-node", "worker", long_ago, None, 256 * MI, 64 * MI, 0.05,
                       lambda t: 0.0, "Deployment", "daily-worker")
        for pod in ("investigation-api", "payments-api-with-a-deliberately-long-workload-name-0",
                    "payments-api-with-a-deliberately-long-workload-name-1"):
            self.gauge("kube_pod_container_status_restarts_total",
                       {"namespace": "apps", "pod": pod, "container": "api"}, lambda t: 7.0, long_ago)

        deployment = dict(apps, deployment="checkout-api")
        self.gauge("kube_deployment_spec_replicas", deployment, lambda t: 2.0, long_ago)
        self.gauge("kube_deployment_status_replicas_available", deployment,
                   lambda t: 2.0 if t < rollout + 60 else 1.0, long_ago)
        self.gauge("kube_deployment_status_observed_generation", deployment,
                   lambda t: 1.0, long_ago)
        self.gauge("kube_deployment_metadata_generation", deployment,
                   lambda t: 1.0 if t < rollout else 2.0, long_ago)

        # HTTP traffic: (pod, begin, end, [(since, ok/s, 5xx/s, latency shares)])
        traffic = [
            (OLD_POD, long_ago, rollout + 60, [(long_ago, 20.0, 0.04, FAST)]),
            (STABLE_POD, long_ago, None, [(long_ago, 20.0, 0.04, FAST), (rollout + 60, 29.0, 0.6, STRAINED)]),
            (NEW_POD, rollout, None, [(rollout, 0.0, 0.0, FAST), (rollout + 30, 3.0, 9.0, SLOW)]),
        ]
        for pod, begin, end, phases in traffic:
            base = dict(apps, service="checkout-api", deployment="checkout-api", pod=pod, method="GET")
            self.counter("http_requests_total", dict(base, code="200"), [(s, ok) for s, ok, _, _ in phases], begin, end)
            self.counter("http_requests_total", dict(base, code="500"), [(s, bad) for s, _, bad, _ in phases], begin, end)
            histogram = {k: v for k, v in base.items() if k != "method"}
            for index, bound in enumerate(BUCKETS):
                self.counter("http_request_duration_seconds_bucket", dict(histogram, le=bound),
                             [(s, (ok + bad) * shares[index]) for s, ok, bad, shares in phases], begin, end)
            self.counter("http_request_duration_seconds_count", histogram,
                         [(s, ok + bad) for s, ok, bad, _ in phases], begin, end)
            self.counter("http_request_duration_seconds_sum", histogram,
                         [(s, (ok + bad) * (0.09 if shares is FAST else 0.2 if shares is STRAINED else 1.4))
                          for s, ok, bad, shares in phases], begin, end)

        # Nodes. worker-b stops reporting twenty minutes before start: its
        # node-exporter series END there (missing data, not zero), while
        # kube-state-metrics keeps describing the Node object.
        silent = t0 - NODE_B_SILENT
        nodes = [  # node, last sample time, memory total, available fn, busy share, disk size, disk available fn
            ("fixture-node", None, 16 * GI, wobble(9.5 * GI, 0.3 * GI, 600), 0.42,
             100 * GI, lambda t: 61 * GI),
            ("worker-b", silent, 16 * GI, wobble(11 * GI, 0.2 * GI, 500), 0.18, 100 * GI, lambda t: 70 * GI),
            ("worker-c", None, 16 * GI, wobble(10 * GI, 0.2 * GI, 700), 0.25, 100 * GI,
             lambda t: max(3.2 * GI, 3.2 * GI + (t0 - t) * (40.0 * MI / 60.0))),
        ]
        for node, end, total, available, busy, disk, disk_free in nodes:
            labels = {"node": node, "instance": node + ":9100", "job": "node-exporter"}
            self.gauge("node_memory_MemTotal_bytes", labels, lambda t, v=total: float(v), long_ago, end)
            self.gauge("node_memory_MemAvailable_bytes", labels, available, long_ago, end)
            self.gauge("node_load1", labels, wobble(busy * 4, 0.2, 240), long_ago, end)
            mount = dict(labels, mountpoint="/", fstype="ext4")
            self.gauge("node_filesystem_size_bytes", mount, lambda t, v=disk: float(v), long_ago, end)
            self.gauge("node_filesystem_avail_bytes", mount, disk_free, long_ago, end)
            for mode, share in (("idle", 1.0 - busy), ("user", busy * 0.7), ("system", busy * 0.3)):
                self.counter("node_cpu_seconds_total", dict(labels, cpu="0", mode=mode), [(long_ago, share * 4)], long_ago, end)
            self.gauge("up", labels, lambda t, e=end: 1.0 if e is None or t < e else 0.0, long_ago)
            ready = (lambda t: 1.0) if node != "worker-b" else (lambda t: 1.0 if t < silent + 60 else 0.0)
            unknown = (lambda t: 0.0) if node != "worker-b" else (lambda t: 0.0 if t < silent + 60 else 1.0)
            kube = {"node": node, "job": "kube-state-metrics"}
            self.gauge("kube_node_status_condition", dict(kube, condition="Ready", status="true"), ready, long_ago)
            self.gauge("kube_node_status_condition", dict(kube, condition="Ready", status="unknown"), unknown, long_ago)
            self.gauge("kube_node_status_condition", dict(kube, condition="DiskPressure", status="true"),
                       (lambda t: 1.0 if t > t0 - 25 * 60 else 0.0) if node == "worker-c" else (lambda t: 0.0), long_ago)
            self.gauge("kube_node_spec_unschedulable", kube, lambda t, n=node: 1.0 if n == "worker-b" else 0.0, long_ago)
            for resource, unit, amount in (("cpu", "core", 4.0), ("memory", "byte", 15.5 * GI), ("pods", "integer", 110.0)):
                self.gauge("kube_node_status_allocatable", dict(kube, resource=resource, unit=unit),
                           lambda t, v=amount: v, long_ago)

    def _kube_pod(self, pod, namespace, node, container, begin, end, limit, request, cpu_request, restarts, owner_kind, owner):
        where = {"namespace": namespace, "pod": pod, "container": container}
        self.gauge("kube_pod_container_resource_limits", dict(where, node=node, resource="memory", unit="byte"),
                   lambda t: float(limit), begin, end)
        self.gauge("kube_pod_container_resource_requests", dict(where, node=node, resource="memory", unit="byte"),
                   lambda t: float(request), begin, end)
        self.gauge("kube_pod_container_resource_requests", dict(where, node=node, resource="cpu", unit="core"),
                   lambda t: float(cpu_request), begin, end)
        self.gauge("kube_pod_container_status_restarts_total", where, restarts, begin, end)
        self.gauge("kube_pod_info", {"namespace": namespace, "pod": pod, "node": node,
                                     "created_by_kind": owner_kind, "created_by_name": owner},
                   lambda t: 1.0, begin, end)

    def names(self):
        return sorted({s.labels["__name__"] for s in self.series})


# ---------------------------------------------------------------------------
# PromQL subset
# ---------------------------------------------------------------------------
TOKEN = re.compile(r'\s*(?:(\d+(?:\.\d+)?(?:e[+-]?\d+)?)|([A-Za-z_:][A-Za-z0-9_:]*)|("(?:[^"\\]|\\.)*")|(=~|!~|!=|[{}()\[\],=+\-*/]))')


class QueryError(Exception):
    pass


def tokenize(text):
    tokens, position = [], 0
    text = text.strip()
    while position < len(text):
        match = TOKEN.match(text, position)
        if not match:
            raise QueryError("unsupported query text near %r" % text[position:position + 20])
        number, word, string, symbol = match.groups()
        if number is not None:
            tokens.append(("num", float(number)))
        elif word is not None:
            tokens.append(("word", word))
        elif string is not None:
            tokens.append(("str", unquote(string)))
        else:
            tokens.append(("sym", symbol))
        position = match.end()
    return tokens


def unquote(literal):
    out, index, body = [], 0, literal[1:-1]
    escapes = {"n": "\n", "t": "\t", "r": "\r", '"': '"', "\\": "\\", "a": "\a", "b": "\b", "f": "\f", "v": "\v"}
    while index < len(body):
        char = body[index]
        if char != "\\":
            out.append(char)
            index += 1
            continue
        code = body[index + 1]
        if code == "x":
            out.append(chr(int(body[index + 2:index + 4], 16)))
            index += 4
        elif code == "u":
            out.append(chr(int(body[index + 2:index + 6], 16)))
            index += 6
        elif code == "U":
            out.append(chr(int(body[index + 2:index + 10], 16)))
            index += 10
        else:
            out.append(escapes.get(code, code))
            index += 2
    return "".join(out)


RANGE_FUNCTIONS = ("rate", "increase", "max_over_time", "min_over_time", "avg_over_time")
AGGREGATIONS = ("sum", "avg", "max", "min", "count")


class Parser:
    def __init__(self, text):
        self.tokens = tokenize(text)
        self.index = 0

    def peek(self):
        return self.tokens[self.index] if self.index < len(self.tokens) else (None, None)

    def take(self, kind=None, value=None):
        token = self.peek()
        if token[0] is None or (kind and token[0] != kind) or (value is not None and token[1] != value):
            raise QueryError("unexpected %r" % (token[1],))
        self.index += 1
        return token[1]

    def parse(self):
        node = self.expression()
        if self.peek()[0] is not None:
            raise QueryError("unexpected trailing %r" % (self.peek()[1],))
        return node

    def expression(self):
        kind, value = self.peek()
        if kind == "num":
            self.take()
            return ("scalar", value)
        if kind == "sym" and value == "-":
            self.take()
            return ("scalar", -self.take("num"))
        if kind == "sym" and value == "(":
            self.take()
            left = self.expression()
            operator = self.take("sym")
            if operator not in "+-*/":
                raise QueryError("unsupported operator %r" % operator)
            right = self.expression()
            self.take("sym", ")")
            return ("binary", operator, left, right)
        word = self.take("word")
        if word in RANGE_FUNCTIONS and self.peek() == ("sym", "("):
            self.take()
            selector = self.selector(self.take("word"))
            self.take("sym", "[")
            seconds = self.take("num")
            if self.take("word") != "s":
                raise QueryError("ranges are written in seconds")
            self.take("sym", "]")
            self.take("sym", ")")
            return ("range", word, selector, seconds)
        if word in AGGREGATIONS and self.peek() in (("sym", "("), ("word", "by")):
            by = None
            if self.peek() == ("word", "by"):
                self.take()
                self.take("sym", "(")
                by = []
                while self.peek() != ("sym", ")"):
                    by.append(self.take("word"))
                    if self.peek() == ("sym", ","):
                        self.take()
                self.take("sym", ")")
            self.take("sym", "(")
            inner = self.expression()
            self.take("sym", ")")
            return ("aggregate", word, by, inner)
        if word in ("topk", "histogram_quantile") and self.peek() == ("sym", "("):
            self.take()
            parameter = self.take("num")
            self.take("sym", ",")
            inner = self.expression()
            self.take("sym", ")")
            return (word, parameter, inner)
        return self.selector(word)

    def selector(self, name):
        matchers = []
        if self.peek() == ("sym", "{"):
            self.take()
            while self.peek() != ("sym", "}"):
                label = self.take("word")
                operator = self.take("sym")
                if operator not in ("=", "!=", "=~", "!~"):
                    raise QueryError("unsupported matcher %r" % operator)
                matchers.append((label, operator, self.take("str")))
                if self.peek() == ("sym", ","):
                    self.take()
            self.take("sym", "}")
        return ("select", name, matchers)


def matches(labels, matchers):
    for label, operator, expected in matchers:
        actual = labels.get(label, "")
        if operator == "=" and actual != expected:
            return False
        if operator == "!=" and actual == expected:
            return False
        if operator in ("=~", "!~"):
            found = re.fullmatch(expected, actual) is not None
            if found != (operator == "=~"):
                return False
    return True


def without_name(labels):
    return {k: v for k, v in labels.items() if k != "__name__"}


def key_of(labels):
    return tuple(sorted(labels.items()))


def evaluate(metrics, node, t):
    """Returns a float for a scalar or a list of (labels, value)."""
    kind = node[0]
    if kind == "scalar":
        return node[1]
    if kind == "select":
        out = []
        for series in metrics.series:
            if series.labels["__name__"] == node[1] and matches(series.labels, node[2]):
                value = series.fn(t)
                if value is not None:
                    out.append((dict(series.labels), value))
        return out
    if kind == "range":
        _, function, selector, seconds = node
        out = []
        for series in metrics.series:
            if series.labels["__name__"] != selector[1] or not matches(series.labels, selector[2]):
                continue
            points = [(t - seconds + seconds * i / 10.0) for i in range(11)]
            samples = [(p, series.fn(p)) for p in points]
            samples = [(p, v) for p, v in samples if v is not None]
            # A rate needs two samples; an over-time function needs one.
            if len(samples) < (2 if function in ("rate", "increase") else 1):
                continue
            values = [v for _, v in samples]
            if function == "rate":
                value = (values[-1] - values[0]) / (samples[-1][0] - samples[0][0])
            elif function == "increase":
                value = (values[-1] - values[0]) / (samples[-1][0] - samples[0][0]) * seconds
            elif function == "max_over_time":
                value = max(values)
            elif function == "min_over_time":
                value = min(values)
            else:
                value = sum(values) / len(values)
            out.append((without_name(series.labels), value))
        return out
    if kind == "aggregate":
        _, operation, by, inner = node
        vector = evaluate(metrics, inner, t)
        if isinstance(vector, float):
            raise QueryError("aggregation needs a vector")
        groups = {}
        for labels, value in vector:
            kept = {k: labels[k] for k in (by or []) if k in labels}
            groups.setdefault(key_of(kept), []).append(value)
        out = []
        for key, values in groups.items():
            value = {"sum": sum(values), "avg": sum(values) / len(values), "max": max(values),
                     "min": min(values), "count": float(len(values))}[operation]
            out.append((dict(key), value))
        return out
    if kind == "topk":
        vector = evaluate(metrics, node[2], t)
        return sorted(vector, key=lambda item: -item[1])[:int(node[1])]
    if kind == "histogram_quantile":
        vector = evaluate(metrics, node[2], t)
        groups = {}
        for labels, value in vector:
            if "le" not in labels:
                continue
            rest = {k: v for k, v in labels.items() if k not in ("le", "__name__")}
            bound = math.inf if labels["le"] == "+Inf" else float(labels["le"])
            groups.setdefault(key_of(rest), []).append((bound, value))
        out = []
        for key, buckets in groups.items():
            buckets.sort()
            total = buckets[-1][1]
            if total <= 0 or not math.isinf(buckets[-1][0]):
                continue
            rank, previous_bound, previous_count = node[1] * total, 0.0, 0.0
            for bound, count in buckets:
                if count >= rank:
                    if math.isinf(bound):
                        value = previous_bound
                    else:
                        span = count - previous_count
                        value = bound if span <= 0 else previous_bound + (bound - previous_bound) * (rank - previous_count) / span
                    out.append((dict(key), value))
                    break
                previous_bound, previous_count = bound, count
        return out
    if kind == "binary":
        _, operator, left_node, right_node = node
        left, right = evaluate(metrics, left_node, t), evaluate(metrics, right_node, t)

        def apply(a, b):
            if operator == "+":
                return a + b
            if operator == "-":
                return a - b
            if operator == "*":
                return a * b
            return a / b if b != 0 else None

        if isinstance(left, float) and isinstance(right, float):
            value = apply(left, right)
            return value if value is not None else math.nan
        if isinstance(right, float):
            return [(without_name(l), apply(v, right)) for l, v in left if apply(v, right) is not None]
        if isinstance(left, float):
            return [(without_name(l), apply(left, v)) for l, v in right if apply(left, v) is not None]
        index = {key_of(without_name(l)): v for l, v in right}
        out = []
        for labels, value in left:
            other = index.get(key_of(without_name(labels)))
            if other is not None and apply(value, other) is not None:
                out.append((without_name(labels), apply(value, other)))
        return out
    raise QueryError("unsupported expression")


def number(value):
    return repr(float(value)) if not float(value).is_integer() or abs(value) > 1e15 else str(int(value))


# ---------------------------------------------------------------------------
# HTTP-level answers
# ---------------------------------------------------------------------------
class Sources:
    """Answers GET requests for the three monitoring Services."""

    def __init__(self, start=None, deny=()):
        self.start = float(start if start is not None else time.time())
        self.deny = set(deny)
        self.metrics = Metrics(self.start)

    # Returns (http status, JSON-serialisable body).
    def handle(self, service, port, path, query):
        if SERVICES.get(service) != port:
            return 503, {"kind": "Status", "status": "Failure", "reason": "ServiceUnavailable", "code": 503,
                         "message": "no endpoints available for service"}
        if service in self.deny:
            return 403, {"message": "synthetic fixture denies " + service + " reads"}
        if service == "prometheus":
            return self.prometheus(path, query)
        if service == "alertmanager":
            return self.alertmanager(path, query)
        return self.grafana(path, query)

    @staticmethod
    def first(query, name, default=None):
        values = query.get(name)
        return values[0] if values else default

    # -- Prometheus ------------------------------------------------------
    def prometheus(self, path, query):
        if path == "/api/v1/status/buildinfo":
            return 200, {"status": "success", "data": {"version": "2.53.0-synthetic", "revision": "fixture"}}
        if path == "/api/v1/label/__name__/values":
            return 200, {"status": "success", "data": self.metrics.names()}
        if path not in ("/api/v1/query", "/api/v1/query_range"):
            return 404, {"status": "error", "errorType": "not_found", "error": "unknown path"}
        try:
            tree = Parser(self.first(query, "query", "")).parse()
            if path == "/api/v1/query":
                at = float(self.first(query, "time", time.time()))
                value = evaluate(self.metrics, tree, at)
                if isinstance(value, float):
                    return 200, {"status": "success", "data": {"resultType": "scalar", "result": [at, number(value)]}}
                result = [{"metric": labels, "value": [at, number(v)]} for labels, v in value if math.isfinite(v)]
                return 200, {"status": "success", "data": {"resultType": "vector", "result": result}}
            start, end = float(self.first(query, "start")), float(self.first(query, "end"))
            step = float(self.first(query, "step"))
            if step <= 0 or end < start or (end - start) / step > 11000:
                raise QueryError("exceeded maximum resolution of 11,000 points per timeseries")
            collected, order = {}, []
            steps = int(math.floor((end - start) / step + 1e-9))
            for index in range(steps + 1):
                at = start + index * step
                value = evaluate(self.metrics, tree, at)
                if isinstance(value, float):
                    value = [({}, value)]
                for labels, sample in value:
                    if not math.isfinite(sample):
                        continue
                    key = key_of(labels)
                    if key not in collected:
                        collected[key] = []
                        order.append((key, labels))
                    collected[key].append([round(at, 3), number(sample)])
            result = [{"metric": labels, "values": collected[key]} for key, labels in order]
            return 200, {"status": "success", "data": {"resultType": "matrix", "result": result}}
        except (QueryError, TypeError, ValueError, IndexError) as error:
            return 400, {"status": "error", "errorType": "bad_data", "error": str(error)}

    # -- Alertmanager ----------------------------------------------------
    def alerts(self):
        t0 = self.start

        def stamp(seconds_before):
            return time.strftime("%Y-%m-%dT%H:%M:%S.000Z", time.gmtime(t0 - seconds_before))

        def alert(fingerprint, started, labels, annotations, state="active", silenced=(), inhibited=()):
            return {"fingerprint": fingerprint, "labels": labels, "annotations": annotations,
                    "startsAt": stamp(started), "endsAt": stamp(-240), "updatedAt": stamp(30),
                    "generatorURL": "http://prometheus.monitoring.svc:9090/graph",
                    "receivers": [{"name": "synthetic-oncall"}],
                    "status": {"state": state, "silencedBy": list(silenced), "inhibitedBy": list(inhibited)}}

        runbooks = "https://runbooks.example.test/"
        return [
            alert("a1c0ffee00000001", 9 * 60,
                  {"alertname": "CheckoutHighErrorRate", "severity": "critical", "namespace": "apps",
                   "service": "checkout-api", "deployment": "checkout-api", "team": "payments"},
                  {"summary": "checkout-api is answering more than 5% of requests with 5xx",
                   "description": "5xx ratio over five minutes is above the 5% objective for checkout-api.",
                   "runbook_url": runbooks + "checkout-high-error-rate"}),
            alert("a1c0ffee00000002", 8 * 60,
                  {"alertname": "KubePodCrashLooping", "severity": "warning", "namespace": "apps",
                   "pod": NEW_POD, "container": "api", "job": "kube-state-metrics"},
                  {"summary": "Pod is crash looping.",
                   "description": "Pod apps/" + NEW_POD + " (api) is in waiting state (reason: CrashLoopBackOff).",
                   "runbook_url": runbooks + "kubernetes/kubepodcrashlooping"}),
            alert("a1c0ffee00000003", 2 * 60,
                  {"alertname": "KubeDeploymentRolloutStuck", "severity": "warning", "namespace": "apps",
                   "deployment": "checkout-api", "job": "kube-state-metrics"},
                  {"summary": "Deployment rollout is not progressing.",
                   "description": "Rollout of deployment apps/checkout-api is not progressing.",
                   "runbook_url": runbooks + "kubernetes/kubedeploymentrolloutstuck"}),
            # The same alert on both revision 2 pods: one row per workload in :alerts.
            alert("a1c0ffee00000008", 6 * 60,
                  {"alertname": "KubePodNotReady", "severity": "warning", "namespace": "apps",
                   "pod": NEW_POD, "job": "kube-state-metrics"},
                  {"summary": "Pod has been in a non-ready state for more than 5 minutes.",
                   "description": "Pod apps/" + NEW_POD + " has been in a non-ready state for longer than 5 minutes.",
                   "runbook_url": runbooks + "kubernetes/kubepodnotready"}),
            alert("a1c0ffee00000009", 7 * 60,
                  {"alertname": "KubePodNotReady", "severity": "warning", "namespace": "apps",
                   "pod": PULL_POD, "job": "kube-state-metrics"},
                  {"summary": "Pod has been in a non-ready state for more than 5 minutes.",
                   "description": "Pod apps/" + PULL_POD + " has been in a non-ready state for longer than 5 minutes.",
                   "runbook_url": runbooks + "kubernetes/kubepodnotready"}),
            # Names only the Service, whose own status cannot show slow answers.
            alert("a1c0ffee0000000a", 7 * 60,
                  {"alertname": "CheckoutHighLatency", "severity": "warning", "namespace": "apps",
                   "service": "checkout-api", "team": "payments"},
                  {"summary": "checkout-api p95 latency is above 1s",
                   "description": "95th percentile request duration over five minutes is above the 1s objective.",
                   "runbook_url": runbooks + "checkout-high-latency"}),
            alert("a1c0ffee00000004", 18 * 60,
                  {"alertname": "KubeNodeNotReady", "severity": "critical", "node": "worker-b",
                   "job": "kube-state-metrics"},
                  {"summary": "Node is not ready.", "description": "worker-b has been unready for more than 15 minutes.",
                   "runbook_url": runbooks + "kubernetes/kubenodenotready"}),
            alert("a1c0ffee00000005", 16 * 60,
                  {"alertname": "KubeNodeUnreachable", "severity": "warning", "node": "worker-b",
                   "job": "kube-state-metrics"},
                  {"summary": "Node is unreachable.",
                   "description": "worker-b is unreachable and some workloads may be rescheduled.",
                   "runbook_url": runbooks + "kubernetes/kubenodeunreachable"},
                  state="suppressed", inhibited=["a1c0ffee00000004"]),
            alert("a1c0ffee00000006", 25 * 60,
                  {"alertname": "NodeFilesystemAlmostOutOfSpace", "severity": "warning", "node": "worker-c",
                   "instance": "worker-c:9100", "mountpoint": "/", "job": "node-exporter"},
                  {"summary": "Filesystem has less than 5% space left.",
                   "description": "Filesystem on / at worker-c:9100 has only 3.2% available space left.",
                   "runbook_url": runbooks + "node/nodefilesystemalmostoutofspace"},
                  state="suppressed", silenced=["5f1c2d3e-0000-4000-8000-synthetic001"]),
            alert("a1c0ffee00000007", 6 * 24 * 3600,
                  {"alertname": "Watchdog", "severity": "none"},
                  {"summary": "An alert that should always be firing to certify that Alertmanager is working properly."}),
        ]

    def alertmanager(self, path, query):
        if path == "/api/v2/status":
            return 200, {"cluster": {"status": "ready"}, "versionInfo": {"version": "0.27.0-synthetic"},
                         "uptime": time.strftime("%Y-%m-%dT%H:%M:%S.000Z", time.gmtime(self.start - 6 * 24 * 3600))}
        if path != "/api/v2/alerts":
            return 404, {"message": "unknown path"}

        def flag(name, default):
            return self.first(query, name, default) == "true"

        filters = []
        for text in query.get("filter", []):
            parser = Parser("x{" + text + "}")
            try:
                filters.extend(parser.parse()[2])
            except (QueryError, IndexError):
                return 400, {"message": "bad matcher format"}
        out = []
        for item in self.alerts():
            status = item["status"]
            silenced, inhibited = bool(status["silencedBy"]), bool(status["inhibitedBy"])
            if status["state"] == "active" and not flag("active", "true"):
                continue
            if silenced and not flag("silenced", "true"):
                continue
            if inhibited and not flag("inhibited", "true"):
                continue
            if matches(item["labels"], filters):
                out.append(item)
        return 200, out

    # -- Grafana ---------------------------------------------------------
    DASHBOARDS = [
        ("k8s-pod", "Kubernetes / Compute Resources / Pod", "Kubernetes", ["kubernetes-mixin"]),
        ("k8s-workload", "Kubernetes / Compute Resources / Workload", "Kubernetes", ["kubernetes-mixin"]),
        ("k8s-namespace", "Kubernetes / Compute Resources / Namespace (Pods)", "Kubernetes", ["kubernetes-mixin"]),
        ("k8s-node", "Node Exporter / Nodes", "Kubernetes", ["node-exporter-mixin"]),
        ("checkout-slo", "Checkout / Service SLO", "Payments", ["checkout", "slo"]),
    ]

    def annotations(self):
        ms = lambda seconds_before: int((self.start - seconds_before) * 1000)
        return [
            {"id": 101, "time": ms(26 * 3600), "timeEnd": ms(25 * 3600), "text": "Load test: checkout-api baseline",
             "tags": ["loadtest", "checkout-api", "namespace:apps"], "dashboardUID": "checkout-slo", "panelId": 2},
            {"id": 102, "time": ms(CONFIG_EDIT), "timeEnd": 0, "text": "ConfigMap apps/checkout-config edited",
             "tags": ["config", "checkout-api", "namespace:apps"], "dashboardUID": "", "panelId": 0},
            {"id": 103, "time": ms(ROLLOUT), "timeEnd": 0,
             "text": "Deploy checkout-api revision 2: example.test/checkout:v1 -> v2",
             "tags": ["deploy", "checkout-api", "namespace:apps"], "dashboardUID": "", "panelId": 0},
            {"id": 104, "time": ms(9 * 60), "timeEnd": 0, "alertId": 7, "alertName": "CheckoutHighErrorRate",
             "newState": "alerting", "prevState": "ok", "text": "CheckoutHighErrorRate {namespace=apps} - alerting",
             "tags": ["alert", "checkout-api", "namespace:apps"], "dashboardUID": "checkout-slo", "panelId": 4},
            {"id": 105, "time": ms(NODE_B_SILENT - 120), "timeEnd": 0, "alertId": 9, "alertName": "KubeNodeNotReady",
             "newState": "alerting", "prevState": "ok", "text": "KubeNodeNotReady {node=worker-b} - alerting",
             "tags": ["alert", "node:worker-b"], "dashboardUID": "k8s-node", "panelId": 1},
        ]

    def grafana(self, path, query):
        prefix = "/api/datasources/proxy/uid/"
        if path.startswith(prefix):
            uid, _, rest = path[len(prefix):].partition("/")
            if uid != DATASOURCE_UID:
                return 404, {"message": "Data source not found"}
            return self.prometheus("/" + rest, query)
        if path == "/api/health":
            return 200, {"database": "ok", "version": "11.2.0-synthetic", "commit": "fixture"}
        if path == "/api/search":
            text = (self.first(query, "query", "") or "").lower()
            tags = set(query.get("tag", []))
            limit = int(self.first(query, "limit", "1000"))
            out = []
            for index, (uid, title, folder, dashboard_tags) in enumerate(self.DASHBOARDS):
                if text and text not in title.lower():
                    continue
                if tags and not tags.issubset(dashboard_tags):
                    continue
                out.append({"id": index + 1, "uid": uid, "title": title, "uri": "db/" + uid,
                            "url": "/d/" + uid + "/" + re.sub(r"[^a-z0-9]+", "-", title.lower()).strip("-"),
                            "type": "dash-db", "tags": dashboard_tags, "folderTitle": folder, "isStarred": False})
            return 200, out[:limit]
        if path == "/api/annotations":
            low = float(self.first(query, "from", "0"))
            high = float(self.first(query, "to", str(time.time() * 1000 + 1)))
            tags = set(query.get("tags", []))
            any_tag = self.first(query, "matchAny", "false") == "true"
            dashboard = self.first(query, "dashboardUID")
            limit = int(self.first(query, "limit", "100"))
            out = []
            for item in self.annotations():
                if not low <= item["time"] <= high:
                    continue
                if dashboard and item["dashboardUID"] != dashboard:
                    continue
                if tags and not (tags & set(item["tags"]) if any_tag else tags.issubset(item["tags"])):
                    continue
                out.append(item)
            out.sort(key=lambda item: -item["time"])  # Grafana answers newest first
            return 200, out[:limit]
        return 404, {"message": "Not found"}


PROXY_PATH = re.compile(r"^/api/v1/namespaces/([a-z0-9-]+)/services/(?:(https?):)?([a-z0-9-]+):([a-z0-9-]+)/proxy(/.*)?$")


def service_objects(now):
    """Service objects so an explicit suggestion finds the three sources."""
    out = []
    for name, port in SERVICES.items():
        out.append({"apiVersion": "v1", "kind": "Service", "metadata": {
            "name": name, "namespace": NAMESPACE, "uid": "study-service-" + name, "resourceVersion": "100",
            "creationTimestamp": now, "labels": {"app.kubernetes.io/name": name}},
            "spec": {"type": "ClusterIP", "clusterIP": "10.0.1.%d" % (10 + len(out)),
                     "selector": {"app.kubernetes.io/name": name},
                     "ports": [{"name": "http", "port": int(port), "targetPort": int(port)}]},
            "status": {"loadBalancer": {}}})
    return out


CONTEXT_CONFIG = """k9s:
  cluster: study-loopback
  observability:
    prometheus:
      service: monitoring/prometheus:9090
    alertmanager:
      service: monitoring/alertmanager:9093
    grafana:
      service: monitoring/grafana:3000
      publicURL: https://grafana.study.example.test
      datasource: prom-main
      lokiDatasource: loki-main
      dashboards:
        pod:
          uid: k8s-pod
          namespaceVar: namespace
          podVar: pod
        workload:
          uid: k8s-workload
          namespaceVar: namespace
          workloadVar: workload
        node:
          uid: k8s-node
          nodeVar: instance
        namespace:
          uid: k8s-namespace
          namespaceVar: namespace
"""

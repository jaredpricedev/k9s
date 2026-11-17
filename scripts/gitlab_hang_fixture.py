#!/usr/bin/env python3
"""Synthetic "GitLab intermittent hang" scenario for the study fixture.

Namespace gitlab is modelled on the official GitLab Helm chart on a cluster
whose networking is Cilium. Nothing crash-loops and every Pod is Ready: the
story is only visible in metrics and in how long one Pod takes to answer.

Three stall episodes end before the fixture starts (STALLS, seconds before
start). During each one exactly one webservice Pod (OUTLIER) is CPU
throttled at its limit, its Puma threads are all busy, requests queue and
its request duration p99 goes from about 0.3s to tens of seconds. The other
two webservice Pods, the ingress controller, PostgreSQL, Redis, Gitaly and
DNS stay normal. The ingress sees HTTP 499 (the client gave up waiting) in
the same minutes and almost no 5xx. One small, unrelated Hubble drop blip
(DROP_BLIP) happens between two episodes and hits a different Pod.

No real system is contacted and no real data is included.
"""

import math
import time

MI = 1024 * 1024
GI = 1024 * MI

NAMESPACE = "gitlab"
DNS_NAMESPACE = "kube-system"
SERVICE = "gitlab-webservice-default"
INGRESS = "gitlab-webservice-default"
HOST = "gitlab.study.example.test"
WEB_SET = SERVICE + "-6f9c7d5b84"
NGINX = "gitlab-nginx-ingress-controller"
NGINX_SET = NGINX + "-5d8f6c7b9"
COREDNS_SET = "coredns-76f75df574"

# (pod, node, pod IP)
WEB_PODS = [
    (WEB_SET + "-2bq7k", "worker-d", "10.42.3.21"),
    (WEB_SET + "-8wzpd", "worker-e", "10.42.4.17"),
    (WEB_SET + "-hx4mt", "worker-f", "10.42.5.9"),
]
OUTLIER = WEB_SET + "-hx4mt"
NGINX_PODS = [(NGINX_SET + "-k7p2m", "worker-d", "10.42.3.30"), (NGINX_SET + "-w9x4c", "worker-e", "10.42.4.31")]
COREDNS_PODS = [(COREDNS_SET + "-6xk2p", "worker-d", "10.42.3.5"), (COREDNS_SET + "-q8n4v", "worker-e", "10.42.4.5")]
# StatefulSet, port, port name, pod IP, node, exporter Service, container
STATEFUL = [
    ("gitlab-postgresql", 5432, "tcp-postgresql", "10.42.3.40", "worker-d"),
    ("gitlab-redis-master", 6379, "tcp-redis", "10.42.4.41", "worker-e"),
    ("gitlab-gitaly", 8075, "grpc-gitaly", "10.42.3.42", "worker-d"),
]
# node, zone, instance type, busy CPU share
NODES = [
    ("worker-d", "study-zone-a", "study.standard-8", 0.31),
    ("worker-e", "study-zone-b", "study.standard-8", 0.28),
    ("worker-f", "study-zone-c", "study.standard-8", 0.62),
]
CILIUM_AGENTS = {"worker-d": "cilium-4kq7n", "worker-e": "cilium-9hw2c", "worker-f": "cilium-tz6vd"}

# Stall episodes as (begin, end) in seconds before fixture start: 3, 4 and 2
# minutes long, all inside the last hour. The hour before that is quiet.
STALLS = [(52 * 60, 49 * 60), (33 * 60, 29 * 60), (7 * 60, 5 * 60)]
# One unrelated Hubble drop blip, between the first two stalls.
DROP_BLIP = (41 * 60, 40 * 60)
DROP_BLIP_POD = WEB_SET + "-2bq7k"   # not the outlier
# The outlier fails its readiness probe from 90s into a stall until 20s
# after it, in stalls of three minutes or more. Its Pod is Ready at start.
UNREADY_AFTER, READY_AFTER, UNREADY_MIN_STALL = 90, 20, 180

RAILS_BUCKETS = ["0.05", "0.1", "0.25", "0.5", "0.7", "1", "2.5", "5", "10", "25", "60", "+Inf"]
RAILS_FAST = [0.30, 0.62, 0.985, 0.998, 0.9995, 1, 1, 1, 1, 1, 1, 1]                      # p99 near 0.35s
RAILS_STALL = [0.10, 0.20, 0.35, 0.42, 0.45, 0.48, 0.55, 0.63, 0.74, 0.972, 0.9995, 1]    # p99 near 48s
NGINX_BUCKETS = ["0.005", "0.01", "0.025", "0.05", "0.1", "0.25", "0.5", "1", "2.5", "5", "10", "+Inf"]
NGINX_OK = [0.02, 0.06, 0.20, 0.45, 0.75, 0.975, 0.995, 0.999, 1, 1, 1, 1]                 # p99 near 0.44s
NGINX_OK_STALL = [0.016, 0.05, 0.165, 0.37, 0.62, 0.80, 0.82, 0.83, 0.85, 0.88, 0.92, 1]   # p99 above 10s
NGINX_GONE = [0, 0, 0.10, 0.30, 0.50, 0.70, 0.80, 0.90, 0.95, 1, 1, 1]
NGINX_GONE_STALL = [0, 0, 0, 0, 0, 0, 0, 0.01, 0.03, 0.10, 0.30, 1]                        # most waited over 10s
GRPC_BUCKETS = NGINX_BUCKETS
GRPC_SHARES = [0.30, 0.55, 0.80, 0.93, 0.992, 0.999, 1, 1, 1, 1, 1, 1]                     # p99 near 0.1s
COREDNS_BUCKETS = ["0.00025", "0.0005", "0.001", "0.002", "0.004", "0.008", "0.016", "0.032", "0.064", "0.128",
                   "0.256", "0.512", "1.024", "2.048", "4.096", "8.192", "+Inf"]
COREDNS_SHARES = [0.35, 0.60, 0.80, 0.93, 0.985, 0.997, 0.999] + [1] * 10                  # p99 near 6ms

READINESS_PORT = 8080
PROBE_PATHS = {"/-/readiness": {"status": "ok", "master_check": [{"status": "ok"}]},
               "/-/liveness": {"status": "ok"}}


def stall_windows(t0):
    return [(t0 - begin, t0 - end) for begin, end in STALLS]


def in_stall(t0, t):
    return any(lo <= t < hi for lo, hi in stall_windows(t0))


def unready_windows(t0):
    return [(lo + UNREADY_AFTER, hi + READY_AFTER) for lo, hi in stall_windows(t0) if hi - lo >= UNREADY_MIN_STALL]


# ---------------------------------------------------------------------------
# Prometheus series
# ---------------------------------------------------------------------------
def add_series(metrics):
    """Adds every gitlab scenario series to an observability Metrics."""
    t0 = metrics.t0
    long_ago = t0 - 6 * 24 * 3600
    stalls = stall_windows(t0)

    def phases(normal, stalled):
        pieces = [(long_ago, normal)]
        for lo, hi in stalls:
            pieces += [(lo, stalled), (hi, normal)]
        return pieces

    def flat(rate):
        return [(long_ago, rate)]

    def counter(name, labels, pieces, ripple=0.12, period=97.0, phase=0.0):
        """A counter whose rate follows pieces and breathes by +-ripple, so a
        drawn rate is not a ruler line. It never decreases."""
        def value(t):
            if t < long_ago:
                return None
            total = 0.0
            for index, (since, rate) in enumerate(pieces):
                hi = min(pieces[index + 1][0] if index + 1 < len(pieces) else t, t)
                if hi > since:
                    wave = period * (math.cos(since / period + phase) - math.cos(hi / period + phase))
                    total += rate * ((hi - since) + ripple * wave)
            return total
        metrics.add(name, labels, value)

    def gauge(name, labels, fn):
        metrics.gauge(name, labels, fn, long_ago)

    def const(name, labels, value):
        gauge(name, labels, lambda t, v=float(value): v)

    def wobble(base, spread, period, phase=0.0):
        return lambda t: base + spread * math.sin((t - t0) / period + phase)

    def histogram(name, labels, buckets, pieces, phase=0.0, mean=None):
        """pieces is [(since, rate, cumulative shares)]."""
        for index, bound in enumerate(buckets):
            counter(name + "_bucket", dict(labels, le=bound), [(s, r * shares[index]) for s, r, shares in pieces], phase=phase)
        counter(name + "_count", labels, [(s, r) for s, r, _ in pieces], phase=phase)
        if mean is not None:
            counter(name + "_sum", labels, [(s, r * mean(shares)) for s, r, shares in pieces], phase=phase)

    def shaped(normal_rate, normal_shares, stall_rate, stall_shares):
        pieces = [(long_ago, normal_rate, normal_shares)]
        for lo, hi in stalls:
            pieces += [(lo, stall_rate, stall_shares), (hi, normal_rate, normal_shares)]
        return pieces

    # -- webservice Pods: Rails, Puma, cAdvisor, kube-state-metrics --------
    for index, (pod, node, ip) in enumerate(WEB_PODS):
        slow = pod == OUTLIER
        phase = 1.7 * index
        app = {"namespace": NAMESPACE, "pod": pod, "service": SERVICE, "container": "webservice", "job": "gitlab-webservice"}
        http = dict(app, method="get")
        counter("http_requests_total", dict(http, status="200"), phases(12.0, 1.5) if slow else flat(12.0), phase=phase)
        counter("http_requests_total", dict(http, status="500"), flat(0.004), phase=phase)
        rails = shaped(12.0, RAILS_FAST, 1.5, RAILS_STALL) if slow else [(long_ago, 12.0, RAILS_FAST)]
        histogram("http_request_duration_seconds", http, RAILS_BUCKETS, rails, phase,
                  mean=lambda shares: 0.09 if shares is RAILS_FAST else 9.0)

        for worker in ("puma_0", "puma_1"):
            puma = dict(app, worker=worker)
            shift = phase + (0.9 if worker == "puma_1" else 0.0)

            def active(t, shift=shift, slow=slow):
                if slow and in_stall(t0, t):
                    return 4.0
                return float(min(3, max(0, round((1.6 if slow else 1.2) + 0.9 * math.sin((t - t0) / 71.0 + shift)))))

            def queued(t, slow=slow):
                if not slow:
                    return 0.0
                for lo, hi in stalls:
                    if lo <= t < hi:
                        return float(round(6 + 14 * min(1.0, (t - lo) / 90.0)))
                return 0.0

            const("puma_max_threads", puma, 4)
            const("puma_running", puma, 4)
            gauge("puma_active_connections", puma, active)
            gauge("puma_pool_capacity", puma, lambda t, active=active: 4.0 - active(t))
            gauge("puma_queued_connections", puma, queued)
        const("puma_workers", app, 2)
        const("puma_running_workers", app, 2)

        cgroup = {"namespace": NAMESPACE, "pod": pod, "node": node}
        web = dict(cgroup, container="webservice")
        counter("container_cpu_usage_seconds_total", web, phases(0.48, 1.47) if slow else flat(0.35), ripple=0.05 if slow else 0.12, phase=phase)
        counter("container_cpu_cfs_periods_total", web, flat(10.0), ripple=0.0)
        counter("container_cpu_cfs_throttled_periods_total", web, phases(0.4, 8.6) if slow else flat(0.12), ripple=0.05, phase=phase)
        counter("container_cpu_cfs_throttled_seconds_total", web, phases(0.02, 1.9) if slow else flat(0.006), ripple=0.05, phase=phase)
        gauge("container_memory_working_set_bytes", web, wobble(1.9 * GI, 0.05 * GI, 310, phase))
        horse = dict(cgroup, container="gitlab-workhorse")
        counter("container_cpu_usage_seconds_total", horse, flat(0.03), phase=phase)
        gauge("container_memory_working_set_bytes", horse, wobble(62 * MI, 2 * MI, 260, phase))
        sandbox = dict(cgroup, interface="eth0")
        counter("container_network_receive_bytes_total", sandbox, phases(42000.0, 9000.0) if slow else flat(42000.0), phase=phase)
        counter("container_network_transmit_bytes_total", sandbox, phases(310000.0, 40000.0) if slow else flat(310000.0), phase=phase)

        _kube_pod(metrics, long_ago, NAMESPACE, pod, node, ip, "ReplicaSet", WEB_SET,
                  {"app": "webservice", "release": "gitlab"},
                  [("webservice", 0.9, 2.5 * GI, 1.5, 3 * GI), ("gitlab-workhorse", 0.1, 100 * MI, None, None)],
                  unready=unready_windows(t0) if slow else ())

    for name, replicas in ((SERVICE, 3), (NGINX, 2)):
        deployment = {"namespace": NAMESPACE, "deployment": name}
        const("kube_deployment_spec_replicas", deployment, replicas)
        const("kube_deployment_status_replicas_available", deployment, replicas)
        const("kube_deployment_status_observed_generation", deployment, 1)
        const("kube_deployment_metadata_generation", deployment, 1)

    # -- ingress-nginx ----------------------------------------------------
    for index, (pod, node, ip) in enumerate(NGINX_PODS):
        phase = 0.6 + 2.1 * index
        base = {"controller_class": "k8s.io/ingress-nginx", "controller_namespace": NAMESPACE, "controller_pod": pod,
                "namespace": NAMESPACE, "ingress": INGRESS, "service": SERVICE, "host": HOST, "method": "GET", "path": "/",
                "pod": pod, "container": "controller", "job": NGINX + "-metrics"}
        for status, normal, stalled in (("200", 18.0, 14.5), ("499", 0.004, 1.1), ("302", 1.5, 1.5),
                                        ("404", 0.3, 0.3), ("502", 0.001, 0.001)):
            counter("nginx_ingress_controller_requests", dict(base, status=status), phases(normal, stalled), phase=phase)
        for metric in ("nginx_ingress_controller_request_duration_seconds", "nginx_ingress_controller_response_duration_seconds"):
            histogram(metric, dict(base, status="200"), NGINX_BUCKETS, shaped(18.0, NGINX_OK, 14.5, NGINX_OK_STALL), phase,
                      mean=lambda shares: 0.08 if shares is NGINX_OK else 1.6)
            histogram(metric, dict(base, status="499"), NGINX_BUCKETS, shaped(0.004, NGINX_GONE, 1.1, NGINX_GONE_STALL), phase,
                      mean=lambda shares: 0.3 if shares is NGINX_GONE else 24.0)
        const("nginx_ingress_controller_nginx_process_connections", {
            "controller_class": "k8s.io/ingress-nginx", "controller_namespace": NAMESPACE, "controller_pod": pod,
            "state": "active", "pod": pod, "namespace": NAMESPACE}, 180)
        cgroup = {"namespace": NAMESPACE, "pod": pod, "node": node, "container": "controller"}
        counter("container_cpu_usage_seconds_total", cgroup, flat(0.11), phase=phase)
        gauge("container_memory_working_set_bytes", cgroup, wobble(210 * MI, 4 * MI, 290, phase))
        _kube_pod(metrics, long_ago, NAMESPACE, pod, node, ip, "ReplicaSet", NGINX_SET,
                  {"app": "nginx-ingress", "release": "gitlab"}, [("controller", 0.1, 100 * MI, None, None)])

    # -- dependencies: healthy and flat ------------------------------------
    def exporter(statefulset, container="metrics"):
        return {"namespace": NAMESPACE, "pod": statefulset + "-0", "service": statefulset + "-metrics",
                "container": container, "job": statefulset + "-metrics"}

    pg = exporter("gitlab-postgresql")
    const("pg_up", pg, 1)
    const("pg_settings_max_connections", pg, 100)
    database = dict(pg, datname="gitlabhq_production")
    gauge("pg_stat_activity_count", dict(database, state="active"), lambda t: float(round(6 + 1.2 * math.sin((t - t0) / 83.0))))
    gauge("pg_stat_activity_count", dict(database, state="idle"), lambda t: float(round(34 + 2.0 * math.sin((t - t0) / 131.0))))
    const("pg_stat_activity_count", dict(database, state="idle in transaction"), 1)
    counter("pg_stat_database_xact_commit", database, flat(140.0))

    redis = exporter("gitlab-redis-master")
    const("redis_up", redis, 1)
    gauge("redis_connected_clients", redis, lambda t: float(round(42 + 2.0 * math.sin((t - t0) / 117.0))))
    for cmd, rate, seconds in (("get", 300.0, 0.00009), ("set", 120.0, 0.00011), ("evalsha", 40.0, 0.0004)):
        counter("redis_commands_total", dict(redis, cmd=cmd), flat(rate))
        counter("redis_commands_duration_seconds_total", dict(redis, cmd=cmd), flat(rate * seconds))

    gitaly = {"namespace": NAMESPACE, "pod": "gitlab-gitaly-0", "service": "gitlab-gitaly", "container": "gitaly",
              "job": "gitlab-gitaly", "grpc_type": "unary"}
    for service, method, rate in (("gitaly.CommitService", "FindCommit", 18.0), ("gitaly.RefService", "RefExists", 7.0)):
        call = dict(gitaly, grpc_service=service, grpc_method=method)
        histogram("grpc_server_handling_seconds", call, GRPC_BUCKETS, [(long_ago, rate, GRPC_SHARES)], mean=lambda shares: 0.018)
        counter("grpc_server_handled_total", dict(call, grpc_code="OK"), flat(rate))

    for statefulset, _, _, ip, node in STATEFUL:
        container = statefulset.split("-")[1]
        cgroup = {"namespace": NAMESPACE, "pod": statefulset + "-0", "node": node, "container": container}
        counter("container_cpu_usage_seconds_total", cgroup, flat(0.2))
        gauge("container_memory_working_set_bytes", cgroup, wobble(700 * MI, 10 * MI, 330))
        _kube_pod(metrics, long_ago, NAMESPACE, statefulset + "-0", node, ip, "StatefulSet", statefulset,
                  {"app": container, "release": "gitlab"}, [(container, 0.25, 512 * MI, None, 2 * GI)])

    # -- DNS: healthy -------------------------------------------------------
    for index, (pod, node, ip) in enumerate(COREDNS_PODS):
        dns = {"namespace": DNS_NAMESPACE, "pod": pod, "service": "kube-dns", "container": "coredns", "job": "coredns",
               "server": "dns://:53", "zone": "."}
        histogram("coredns_dns_request_duration_seconds", dns, COREDNS_BUCKETS, [(long_ago, 22.5, COREDNS_SHARES)],
                  phase=index, mean=lambda shares: 0.0009)
        counter("coredns_dns_requests_total", dict(dns, proto="udp", family="1", type="A"), flat(22.5), phase=index)
        counter("coredns_dns_responses_total", dict(dns, rcode="NOERROR", plugin=""), flat(19.0), phase=index)
        counter("coredns_dns_responses_total", dict(dns, rcode="NXDOMAIN", plugin=""), flat(3.5), phase=index)
        # A handful of SERVFAIL answers days ago: present, and not rising.
        const("coredns_dns_responses_total", dict(dns, rcode="SERVFAIL", plugin=""), 3)
        _kube_pod(metrics, long_ago, DNS_NAMESPACE, pod, node, ip, "ReplicaSet", COREDNS_SET,
                  {"k8s_app": "kube-dns"}, [("coredns", 0.1, 70 * MI, None, 170 * MI)])

    # -- Cilium / Hubble: flat, near zero, one unrelated blip ---------------
    def hubble(node):
        return {"namespace": DNS_NAMESPACE, "pod": CILIUM_AGENTS[node], "job": "hubble-metrics", "node": node}

    for pod, node, _ in WEB_PODS:
        for source, _, _ in NGINX_PODS:
            flow = dict(hubble(node), source_namespace=NAMESPACE, source_pod=source,
                        destination_namespace=NAMESPACE, destination_pod=pod)
            counter("hubble_flows_processed_total", dict(flow, type="Trace", subtype="to-endpoint", verdict="FORWARDED",
                                                         protocol="TCP"), flat(60.0))
            for flag, rate in (("SYN", 6.0), ("SYN-ACK", 6.0), ("FIN", 12.0), ("RST", 0.01)):
                counter("hubble_tcp_flags_total", dict(flow, flag=flag, family="IPv4"), flat(rate))
        counter("hubble_drop_total", dict(hubble(node), reason="STALE_OR_UNROUTABLE_IP", protocol="TCP",
                                          source_namespace=NAMESPACE, source_pod=NGINX_PODS[0][0],
                                          destination_namespace=NAMESPACE, destination_pod=pod), flat(0.002))
        answer = dict(hubble(node), destination_namespace=NAMESPACE, destination_pod=pod, qtypes="A")
        counter("hubble_dns_responses_total", dict(answer, rcode="No Error", ips_returned="1"), flat(3.0))
        counter("hubble_dns_responses_total", dict(answer, rcode="Non-Existent Domain", ips_returned="0"), flat(0.4))
        counter("hubble_dns_queries_total", dict(answer, query="gitlab-postgresql.gitlab.svc.cluster.local."), flat(3.4))
    blip_node = next(node for pod, node, _ in WEB_PODS if pod == DROP_BLIP_POD)
    blip = dict(hubble(blip_node), source_namespace="ops", source_pod="daily-worker",
                destination_namespace=NAMESPACE, destination_pod=DROP_BLIP_POD)
    blip_pieces = [(long_ago, 0.0), (t0 - DROP_BLIP[0], 0.8), (t0 - DROP_BLIP[1], 0.0)]
    counter("hubble_drop_total", dict(blip, reason="POLICY_DENIED", protocol="TCP"), blip_pieces, ripple=0.0)
    counter("hubble_flows_processed_total", dict(blip, type="Drop", subtype="Policy denied", verdict="DROPPED",
                                                 protocol="TCP"), blip_pieces, ripple=0.0)

    # -- nodes ----------------------------------------------------------------
    for node, zone, instance, busy in NODES:
        labels = {"node": node, "instance": node + ":9100", "job": "node-exporter"}
        const("node_memory_MemTotal_bytes", labels, 32 * GI)
        gauge("node_memory_MemAvailable_bytes", labels, wobble((21 - 9 * busy) * GI, 0.3 * GI, 600))
        gauge("node_load1", labels, wobble(busy * 8, 0.3, 240))
        mount = dict(labels, mountpoint="/", fstype="ext4")
        const("node_filesystem_size_bytes", mount, 200 * GI)
        const("node_filesystem_avail_bytes", mount, 120 * GI)
        for mode, share in (("idle", 1.0 - busy), ("user", busy * 0.7), ("system", busy * 0.3)):
            counter("node_cpu_seconds_total", dict(labels, cpu="0", mode=mode), flat(share * 8), ripple=0.03)
        const("up", labels, 1)
        kube = {"node": node, "job": "kube-state-metrics"}
        const("kube_node_status_condition", dict(kube, condition="Ready", status="true"), 1)
        const("kube_node_status_condition", dict(kube, condition="Ready", status="unknown"), 0)
        const("kube_node_status_condition", dict(kube, condition="DiskPressure", status="true"), 0)
        const("kube_node_spec_unschedulable", kube, 0)
        const("kube_node_info", dict(kube, kubelet_version="v1.33.1", container_runtime_version="containerd://2.0.4"), 1)
        const("kube_node_labels", dict(kube, label_topology_kubernetes_io_zone=zone,
                                       label_node_kubernetes_io_instance_type=instance), 1)
        for resource, unit, amount in (("cpu", "core", 8.0), ("memory", "byte", 31 * GI), ("pods", "integer", 110.0)):
            const("kube_node_status_allocatable", dict(kube, resource=resource, unit=unit), amount)


def _kube_pod(metrics, begin, namespace, pod, node, ip, owner_kind, owner, labels, containers, unready=()):
    """kube-state-metrics series for one Pod. containers is
    [(name, cpu request, memory request, cpu limit or None, memory limit or None)]."""
    def const(name, series_labels, value):
        metrics.gauge(name, series_labels, lambda t, v=float(value): v, begin)

    where = {"namespace": namespace, "pod": pod}
    const("kube_pod_info", dict(where, node=node, created_by_kind=owner_kind, created_by_name=owner,
                                pod_ip=ip, host_network="false"), 1)
    const("kube_pod_owner", dict(where, owner_kind=owner_kind, owner_name=owner, owner_is_controller="true"), 1)
    const("kube_pod_labels", dict(where, **{"label_" + key: value for key, value in labels.items()}), 1)
    const("kube_pod_status_phase", dict(where, phase="Running"), 1)

    def ready(t):
        return 0.0 if any(lo <= t < hi for lo, hi in unready) else 1.0

    metrics.gauge("kube_pod_status_ready", dict(where, condition="true"), ready, begin)
    metrics.gauge("kube_pod_status_ready", dict(where, condition="false"), lambda t: 1.0 - ready(t), begin)
    for name, cpu_request, memory_request, cpu_limit, memory_limit in containers:
        container = dict(where, container=name)
        resource = dict(container, node=node)
        const("kube_pod_container_resource_requests", dict(resource, resource="cpu", unit="core"), cpu_request)
        const("kube_pod_container_resource_requests", dict(resource, resource="memory", unit="byte"), memory_request)
        if cpu_limit is not None:
            const("kube_pod_container_resource_limits", dict(resource, resource="cpu", unit="core"), cpu_limit)
        if memory_limit is not None:
            const("kube_pod_container_resource_limits", dict(resource, resource="memory", unit="byte"), memory_limit)
        const("kube_pod_container_status_restarts_total", container, 0)
        const("kube_pod_container_status_ready", container, 1)


# ---------------------------------------------------------------------------
# Kubernetes objects
# ---------------------------------------------------------------------------
def uid(kind, name):
    return "study-" + kind.lower() + "-" + name


def objects(started):
    """Kubernetes objects for the scenario. started is the fixture start
    (an aware datetime); every time below is relative to it."""
    from datetime import timedelta

    def stamp(seconds_before):
        return (started - timedelta(seconds=seconds_before)).isoformat().replace("+00:00", "Z")

    # Not a whole number of days, so a clock time never coincides with now.
    created = stamp(6 * 24 * 3600 + 5 * 3600 + 17 * 60)
    out = []

    def obj(api_version, kind, name, namespace, labels, spec=None, status=None, owner=None, annotations=None):
        metadata = {"name": name, "uid": uid(kind, name), "resourceVersion": "100", "generation": 1,
                    "creationTimestamp": created, "labels": labels}
        if namespace:
            metadata["namespace"] = namespace
        if annotations:
            metadata["annotations"] = annotations
        if owner:
            owner_kind, owner_name = owner
            metadata["ownerReferences"] = [{"apiVersion": "v1" if owner_kind == "Service" else "apps/v1", "kind": owner_kind,
                                            "name": owner_name, "uid": uid(owner_kind, owner_name), "controller": True,
                                            "blockOwnerDeletion": True}]
        value = {"apiVersion": api_version, "kind": kind, "metadata": metadata}
        if spec is not None:
            value["spec"] = spec
        if status is not None:
            value["status"] = status
        out.append(value)
        return value

    for namespace in (NAMESPACE, DNS_NAMESPACE):
        value = obj("v1", "Namespace", namespace, "", {"kubernetes.io/metadata.name": namespace}, {}, {"phase": "Active"})
        value["metadata"]["uid"] = "study-namespace-" + namespace

    for node, zone, instance, _ in NODES:
        obj("v1", "Node", node, "", {"kubernetes.io/role": "worker", "kubernetes.io/hostname": node,
                                     "topology.kubernetes.io/zone": zone, "node.kubernetes.io/instance-type": instance},
            {"unschedulable": False},
            {"conditions": [
                {"type": "MemoryPressure", "status": "False", "reason": "KubeletHasSufficientMemory", "lastTransitionTime": created},
                {"type": "DiskPressure", "status": "False", "reason": "KubeletHasNoDiskPressure", "lastTransitionTime": created},
                {"type": "Ready", "status": "True", "reason": "KubeletReady", "lastTransitionTime": created,
                 "message": "kubelet is posting ready status"}],
             "capacity": {"cpu": "8", "memory": "32Gi", "pods": "110"},
             "allocatable": {"cpu": "8", "memory": "31Gi", "pods": "110"},
             "addresses": [{"type": "InternalIP", "address": "10.0.2.%d" % (14 + len(out))}, {"type": "Hostname", "address": node}],
             "nodeInfo": {"kubeletVersion": "v1.33.1", "architecture": "amd64", "osImage": "Synthetic Linux",
                          "containerRuntimeVersion": "containerd://2.0.4"}})

    def pod(name, namespace, node, ip, labels, containers, owner, ready_since=None):
        ready_since = ready_since or created
        statuses = [{"name": c["name"], "image": c["image"], "ready": True, "started": True, "restartCount": 0,
                     "state": {"running": {"startedAt": created}}} for c in containers]
        return obj("v1", "Pod", name, namespace, labels,
                   {"nodeName": node, "dnsPolicy": "ClusterFirst", "restartPolicy": "Always",
                    "serviceAccountName": "default", "containers": containers},
                   {"phase": "Running", "podIP": ip, "podIPs": [{"ip": ip}], "hostIP": "10.0.2.1", "qosClass": "Burstable",
                    "startTime": created,
                    "conditions": [{"type": "PodScheduled", "status": "True", "lastTransitionTime": created},
                                   {"type": "Initialized", "status": "True", "lastTransitionTime": created},
                                   {"type": "ContainersReady", "status": "True", "lastTransitionTime": ready_since},
                                   {"type": "Ready", "status": "True", "lastTransitionTime": ready_since}],
                    "containerStatuses": statuses}, owner=owner)

    def workload(kind, name, namespace, labels, selector, replicas, template, extra_spec=None):
        spec = dict({"replicas": replicas, "selector": {"matchLabels": selector}, "template": template}, **(extra_spec or {}))
        status = {"observedGeneration": 1, "replicas": replicas, "readyReplicas": replicas, "updatedReplicas": replicas,
                  "availableReplicas": replicas}
        if kind == "Deployment":
            status["conditions"] = [
                {"type": "Available", "status": "True", "reason": "MinimumReplicasAvailable", "lastTransitionTime": created,
                 "message": "Deployment has minimum availability."},
                {"type": "Progressing", "status": "True", "reason": "NewReplicaSetAvailable", "lastTransitionTime": created,
                 "message": "ReplicaSet has successfully progressed."}]
        if kind == "StatefulSet":
            status.update(currentReplicas=replicas, currentRevision=name + "-7c9", updateRevision=name + "-7c9")
        return obj("apps/v1", kind, name, namespace, labels, spec, status)

    def replica_set(name, deployment, namespace, labels, selector, replicas, template):
        return obj("apps/v1", "ReplicaSet", name, namespace, labels,
                   {"replicas": replicas, "selector": {"matchLabels": selector}, "template": template},
                   {"observedGeneration": 1, "replicas": replicas, "readyReplicas": replicas, "availableReplicas": replicas,
                    "fullyLabeledReplicas": replicas}, owner=("Deployment", deployment),
                   annotations={"deployment.kubernetes.io/revision": "1",
                                "deployment.kubernetes.io/desired-replicas": str(replicas)})

    def service(name, namespace, labels, selector, ports, cluster_ip, kind="ClusterIP", status=None):
        return obj("v1", "Service", name, namespace, labels,
                   {"type": kind, "clusterIP": cluster_ip, "clusterIPs": [] if cluster_ip == "None" else [cluster_ip],
                    "selector": selector, "ports": ports}, status or {"loadBalancer": {}})

    def endpoint_slice(service_name, namespace, ports, pods):
        return obj("discovery.k8s.io/v1", "EndpointSlice", service_name + "-x7k2p", namespace,
                   {"kubernetes.io/service-name": service_name,
                    "endpointslice.kubernetes.io/managed-by": "endpointslice-controller.k8s.io"},
                   owner=("Service", service_name)) | {
            "addressType": "IPv4", "ports": ports,
            "endpoints": [{"addresses": [ip], "conditions": {"ready": True, "serving": True, "terminating": False},
                           "nodeName": node, "zone": next((z for n, z, _, _ in NODES if n == node), ""),
                           "targetRef": {"apiVersion": "v1", "kind": "Pod", "namespace": namespace, "name": name,
                                         "uid": uid("Pod", name)}} for name, node, ip in pods]}

    def publish(value):
        # obj() already appended the dict that "|" copied; replace it.
        out[-1] = value
        return value

    # -- webservice ---------------------------------------------------------
    web_labels = {"app": "webservice", "release": "gitlab", "chart": "webservice-9.5.1", "heritage": "Helm",
                  "gitlab.com/webservice-name": "default"}
    web_selector = {"app": "webservice", "release": "gitlab", "gitlab.com/webservice-name": "default"}
    probe = lambda path: {"httpGet": {"path": path, "port": READINESS_PORT, "scheme": "HTTP"}, "periodSeconds": 10,
                          "timeoutSeconds": 2, "failureThreshold": 3, "successThreshold": 1}
    web_containers = [
        {"name": "webservice", "image": "registry.example.test/gitlab-org/build/cng/gitlab-webservice-ee:v18.5.1",
         "ports": [{"name": "http-webservice", "containerPort": READINESS_PORT, "protocol": "TCP"},
                   {"name": "http-metrics-ws", "containerPort": 8083, "protocol": "TCP"}],
         "readinessProbe": probe("/-/readiness"), "livenessProbe": dict(probe("/-/liveness"), initialDelaySeconds=20),
         "resources": {"requests": {"cpu": "900m", "memory": "2560Mi"}, "limits": {"cpu": "1500m", "memory": "3Gi"}}},
        {"name": "gitlab-workhorse", "image": "registry.example.test/gitlab-org/build/cng/gitlab-workhorse-ee:v18.5.1",
         "ports": [{"name": "http-workhorse", "containerPort": 8181, "protocol": "TCP"},
                   {"name": "http-metrics-wh", "containerPort": 9229, "protocol": "TCP"}],
         "resources": {"requests": {"cpu": "100m", "memory": "100Mi"}}}]
    web_template = {"metadata": {"labels": dict(web_selector, **{"pod-template-hash": "6f9c7d5b84"}),
                                 "annotations": {"prometheus.io/scrape": "true", "prometheus.io/port": "8083"}},
                    "spec": {"containers": web_containers}}
    workload("Deployment", SERVICE, NAMESPACE, web_labels, web_selector, 3, web_template,
             {"strategy": {"type": "RollingUpdate"}, "revisionHistoryLimit": 10})
    replica_set(WEB_SET, SERVICE, NAMESPACE, dict(web_labels, **{"pod-template-hash": "6f9c7d5b84"}), web_selector, 3, web_template)
    last_unready = unready_windows(started.timestamp())[-1][1]
    for name, node, ip in WEB_PODS:
        ready_since = stamp(started.timestamp() - last_unready) if name == OUTLIER else None
        pod(name, NAMESPACE, node, ip, dict(web_labels, **{"pod-template-hash": "6f9c7d5b84"}), web_containers,
            ("ReplicaSet", WEB_SET), ready_since)
    service(SERVICE, NAMESPACE, web_labels, web_selector,
            [{"name": "http-workhorse", "port": 8181, "protocol": "TCP", "targetPort": "http-workhorse"},
             {"name": "http-metrics-ws", "port": 8083, "protocol": "TCP", "targetPort": "http-metrics-ws"}], "10.0.3.20")
    publish(endpoint_slice(SERVICE, NAMESPACE, [{"name": "http-workhorse", "port": 8181, "protocol": "TCP"},
                                                {"name": "http-metrics-ws", "port": 8083, "protocol": "TCP"}], WEB_PODS))
    obj("networking.k8s.io/v1", "Ingress", INGRESS, NAMESPACE, web_labels,
        {"ingressClassName": "gitlab-nginx", "tls": [{"hosts": [HOST], "secretName": "gitlab-wildcard-tls"}],
         "rules": [{"host": HOST, "http": {"paths": [{"path": "/", "pathType": "Prefix", "backend": {
             "service": {"name": SERVICE, "port": {"number": 8181}}}}]}}]},
        {"loadBalancer": {"ingress": [{"ip": "203.0.113.10"}]}},
        annotations={"nginx.ingress.kubernetes.io/proxy-read-timeout": "3600",
                     "nginx.ingress.kubernetes.io/proxy-body-size": "512m"})

    # -- ingress controller -------------------------------------------------
    nginx_labels = {"app": "nginx-ingress", "release": "gitlab", "component": "controller"}
    nginx_containers = [{"name": "controller", "image": "registry.example.test/ingress-nginx/controller:v1.11.8",
                         "ports": [{"name": "http", "containerPort": 80, "protocol": "TCP"},
                                   {"name": "https", "containerPort": 443, "protocol": "TCP"},
                                   {"name": "metrics", "containerPort": 10254, "protocol": "TCP"}],
                         "readinessProbe": {"httpGet": {"path": "/healthz", "port": 10254, "scheme": "HTTP"}},
                         "resources": {"requests": {"cpu": "100m", "memory": "100Mi"}}}]
    nginx_template = {"metadata": {"labels": dict(nginx_labels, **{"pod-template-hash": "5d8f6c7b9"})},
                      "spec": {"containers": nginx_containers}}
    workload("Deployment", NGINX, NAMESPACE, nginx_labels, nginx_labels, 2, nginx_template)
    replica_set(NGINX_SET, NGINX, NAMESPACE, dict(nginx_labels, **{"pod-template-hash": "5d8f6c7b9"}), nginx_labels, 2, nginx_template)
    for name, node, ip in NGINX_PODS:
        pod(name, NAMESPACE, node, ip, dict(nginx_labels, **{"pod-template-hash": "5d8f6c7b9"}), nginx_containers,
            ("ReplicaSet", NGINX_SET))
    service(NGINX, NAMESPACE, nginx_labels, nginx_labels,
            [{"name": "http", "port": 80, "protocol": "TCP", "targetPort": "http"},
             {"name": "https", "port": 443, "protocol": "TCP", "targetPort": "https"}], "10.0.3.10", "LoadBalancer",
            {"loadBalancer": {"ingress": [{"ip": "203.0.113.10"}]}})
    publish(endpoint_slice(NGINX, NAMESPACE, [{"name": "http", "port": 80, "protocol": "TCP"},
                                              {"name": "https", "port": 443, "protocol": "TCP"}], NGINX_PODS))

    # -- PostgreSQL, Redis, Gitaly ------------------------------------------
    for index, (name, port, port_name, ip, node) in enumerate(STATEFUL):
        short = name.split("-")[1]
        labels = {"app": short, "release": "gitlab"}
        containers = [{"name": short, "image": "registry.example.test/gitlab/%s:study" % short,
                       "ports": [{"name": port_name, "containerPort": port, "protocol": "TCP"}],
                       "resources": {"requests": {"cpu": "250m", "memory": "512Mi"}, "limits": {"memory": "2Gi"}}}]
        template = {"metadata": {"labels": labels}, "spec": {"containers": containers}}
        workload("StatefulSet", name, NAMESPACE, labels, labels, 1, template, {"serviceName": name})
        pod(name + "-0", NAMESPACE, node, ip, dict(labels, **{"statefulset.kubernetes.io/pod-name": name + "-0"}),
            containers, ("StatefulSet", name))
        service(name, NAMESPACE, labels, labels, [{"name": port_name, "port": port, "protocol": "TCP", "targetPort": port_name}],
                "None" if short == "gitaly" else "10.0.3.%d" % (30 + index))
        publish(endpoint_slice(name, NAMESPACE, [{"name": port_name, "port": port, "protocol": "TCP"}],
                               [(name + "-0", node, ip)]))

    # -- cluster DNS --------------------------------------------------------
    dns_labels = {"k8s-app": "kube-dns"}
    dns_containers = [{"name": "coredns", "image": "registry.example.test/coredns/coredns:v1.12.0",
                       "ports": [{"name": "dns", "containerPort": 53, "protocol": "UDP"},
                                 {"name": "dns-tcp", "containerPort": 53, "protocol": "TCP"},
                                 {"name": "metrics", "containerPort": 9153, "protocol": "TCP"}],
                       "resources": {"requests": {"cpu": "100m", "memory": "70Mi"}, "limits": {"memory": "170Mi"}}}]
    dns_template = {"metadata": {"labels": dict(dns_labels, **{"pod-template-hash": "76f75df574"})},
                    "spec": {"containers": dns_containers}}
    workload("Deployment", "coredns", DNS_NAMESPACE, dns_labels, dns_labels, 2, dns_template)
    replica_set(COREDNS_SET, "coredns", DNS_NAMESPACE, dict(dns_labels, **{"pod-template-hash": "76f75df574"}), dns_labels, 2,
                dns_template)
    for name, node, ip in COREDNS_PODS:
        pod(name, DNS_NAMESPACE, node, ip, dict(dns_labels, **{"pod-template-hash": "76f75df574"}), dns_containers,
            ("ReplicaSet", COREDNS_SET))
    service("kube-dns", DNS_NAMESPACE, dict(dns_labels, **{"kubernetes.io/name": "CoreDNS"}), dns_labels,
            [{"name": "dns", "port": 53, "protocol": "UDP", "targetPort": 53},
             {"name": "dns-tcp", "port": 53, "protocol": "TCP", "targetPort": 53},
             {"name": "metrics", "port": 9153, "protocol": "TCP", "targetPort": 9153}], "10.0.0.10")
    publish(endpoint_slice("kube-dns", DNS_NAMESPACE, [{"name": "dns", "port": 53, "protocol": "UDP"},
                                                       {"name": "dns-tcp", "port": 53, "protocol": "TCP"},
                                                       {"name": "metrics", "port": 9153, "protocol": "TCP"}], COREDNS_PODS))
    return out


# ---------------------------------------------------------------------------
# Pod proxy (the read-only GET a probe sends through the API server)
# ---------------------------------------------------------------------------
class PodProxy:
    """Answers GET /api/v1/namespaces/NS/pods/[http:]NAME[:PORT]/proxy/PATH.

    The outlier answers a probe path after 1.5-4s, and about one request in
    eight after 5.5-7.5s; the other webservice Pods answer in 5-40ms. The
    sequence is deterministic per Pod. With deny=True every pods/proxy read
    is refused with HTTP 403, as a role without pods/proxy would be."""

    def __init__(self, deny=False):
        self.deny = deny
        self.counts = {}

    @staticmethod
    def _unit(pod, count, salt):
        value = 0
        for char in "%s|%d|%s" % (pod, count, salt):
            value = (value * 131 + ord(char)) % 1000003
        return value / 1000003.0

    def delay(self, pod):
        count = self.counts.get(pod, 0)
        self.counts[pod] = count + 1
        if pod != OUTLIER:
            return 0.005 + 0.035 * self._unit(pod, count, "fast")
        if self._unit(pod, count, "tail") < 0.125:
            return 5.5 + 2.0 * self._unit(pod, count, "slowest")
        return 1.5 + 2.5 * self._unit(pod, count, "slow")

    def handle(self, namespace, target, path):
        """Returns (status, JSON body, seconds to wait before answering)."""
        parts = target.split(":")
        scheme, name, port = "", parts[0], ""
        if len(parts) == 3:
            scheme, name, port = parts
        elif len(parts) == 2:
            name, port = parts
        if self.deny:
            return 403, self._status(403, "Forbidden", 'pods "%s" is forbidden: User "study-user" cannot get resource '
                                     '"pods/proxy" in API group "" in the namespace "%s"' % (name, namespace)), 0.0
        known = {pod: "web" for pod, _, _ in WEB_PODS}
        known.update({pod: "nginx" for pod, _, _ in NGINX_PODS})
        known.update({statefulset + "-0": "tcp" for statefulset, _, _, _, _ in STATEFUL})
        if namespace != NAMESPACE or name not in known or scheme not in ("", "http"):
            return 404, self._status(404, "NotFound", 'pods "%s" not found' % name), 0.0
        kind = known[name]
        refused = self._status(503, "ServiceUnavailable",
                               "error trying to reach service: dial tcp: connect: connection refused")
        if kind == "tcp":
            # A database or gRPC port does not speak HTTP to the proxy.
            return 503, refused, 0.0
        if kind == "nginx":
            if port not in ("10254", "metrics"):
                return 503, refused, 0.0
            if path == "/healthz":
                return 200, {"status": "ok"}, 0.004
            return 404, {"error": "not found"}, 0.004
        if port not in (str(READINESS_PORT), "http-webservice", ""):
            return 503, refused, 0.0
        if path in PROBE_PATHS:
            return 200, PROBE_PATHS[path], self.delay(name)
        return 404, {"status": 404, "error": "Not Found"}, 0.008

    @staticmethod
    def _status(code, reason, message):
        return {"kind": "Status", "apiVersion": "v1", "metadata": {}, "status": "Failure", "message": message,
                "reason": reason, "code": code}


# ---------------------------------------------------------------------------
# Grafana dashboards
# ---------------------------------------------------------------------------
DATASOURCE = {"type": "prometheus", "uid": "${datasource}"}
# uid, title, folder, tags
DASHBOARDS = [
    ("gitlab-webservice", "GitLab / Webservice overview", "GitLab", ["gitlab", "webservice"]),
    ("gitlab-ingress", "GitLab / NGINX ingress", "GitLab", ["gitlab", "ingress-nginx"]),
]


def _panel(panel_id, kind, title, x, y, w, h, targets, unit=None, description="", **extra):
    defaults = {"color": {"mode": "palette-classic" if kind == "timeseries" else "thresholds"}, "mappings": [],
                "thresholds": {"mode": "absolute", "steps": [{"color": "green", "value": None}]}}
    if unit:
        defaults["unit"] = unit
    defaults.update(extra.pop("defaults", {}))
    panel = {"id": panel_id, "type": kind, "title": title, "description": description,
             "gridPos": {"h": h, "w": w, "x": x, "y": y}, "datasource": DATASOURCE,
             "fieldConfig": {"defaults": defaults, "overrides": extra.pop("overrides", [])},
             "targets": [dict({"refId": chr(ord("A") + index), "datasource": DATASOURCE, "editorMode": "code",
                               "range": True}, **target) for index, target in enumerate(targets)]}
    if kind == "timeseries":
        defaults.setdefault("custom", {"drawStyle": "line", "lineInterpolation": "linear", "lineWidth": 1, "fillOpacity": 10,
                                       "showPoints": "never", "spanNulls": False, "stacking": {"mode": "none"}})
        panel["options"] = {"legend": {"displayMode": "table", "placement": "bottom", "calcs": ["mean", "max"]},
                            "tooltip": {"mode": "multi", "sort": "desc"}}
    elif kind == "stat":
        panel["options"] = {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                            "colorMode": "value", "graphMode": "area", "textMode": "auto", "justifyMode": "auto"}
    elif kind == "gauge":
        panel["options"] = {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                            "showThresholdLabels": False, "showThresholdMarkers": True}
    panel.update(extra)
    return panel


def _row(panel_id, title, y):
    return {"id": panel_id, "type": "row", "title": title, "collapsed": False,
            "gridPos": {"h": 1, "w": 24, "x": 0, "y": y}, "panels": []}


def _variables(pod_pattern):
    return {"list": [
        {"name": "datasource", "label": "Data source", "type": "datasource", "query": "prometheus", "hide": 0,
         "current": {"selected": False, "text": "Prometheus", "value": "prom-main"}},
        {"name": "namespace", "label": "Namespace", "type": "query", "datasource": DATASOURCE, "refresh": 2,
         "definition": "label_values(kube_pod_info, namespace)",
         "query": {"qryType": 1, "query": "label_values(kube_pod_info, namespace)", "refId": "PrometheusVariableQueryEditor-VariableQuery"},
         "includeAll": False, "multi": False, "sort": 1,
         "current": {"selected": False, "text": NAMESPACE, "value": NAMESPACE}},
        {"name": "pod", "label": "Pod", "type": "query", "datasource": DATASOURCE, "refresh": 2,
         "definition": 'label_values(kube_pod_info{namespace="$namespace", pod=~"%s"}, pod)' % pod_pattern,
         "query": {"qryType": 1, "query": 'label_values(kube_pod_info{namespace="$namespace", pod=~"%s"}, pod)' % pod_pattern,
                   "refId": "PrometheusVariableQueryEditor-VariableQuery"},
         "includeAll": True, "multi": True, "allValue": ".*", "sort": 1,
         "current": {"selected": True, "text": ["All"], "value": ["$__all"]}}]}


def _webservice_panels():
    scope = 'namespace="$namespace", pod=~"$pod"'
    web = scope + ', container="webservice"'
    red = {"mode": "absolute", "steps": [{"color": "green", "value": None}, {"color": "orange", "value": 0.25},
                                         {"color": "red", "value": 0.5}]}
    return [
        _row(1, "Requests", 0),
        _panel(2, "stat", "Requests per second", 0, 1, 6, 4,
               [{"expr": "sum(rate(http_requests_total{%s}[$__rate_interval]))" % scope, "legendFormat": "requests"}], "reqps"),
        _panel(3, "stat", "Slowest pod p99", 6, 1, 6, 4,
               [{"expr": "max(histogram_quantile(0.99, sum by (pod, le) (rate(http_request_duration_seconds_bucket{%s}"
                         "[$__rate_interval]))))" % scope, "legendFormat": "p99"}], "s",
               defaults={"thresholds": {"mode": "absolute", "steps": [{"color": "green", "value": None},
                                                                      {"color": "orange", "value": 1},
                                                                      {"color": "red", "value": 10}]}}),
        _panel(4, "gauge", "Puma threads in use", 12, 1, 6, 4,
               [{"expr": "(sum(puma_active_connections{%s}) / sum(puma_max_threads{%s}))" % (scope, scope),
                 "legendFormat": "in use", "instant": True}], "percentunit",
               defaults={"min": 0, "max": 1, "thresholds": {"mode": "absolute", "steps": [
                   {"color": "green", "value": None}, {"color": "orange", "value": 0.7}, {"color": "red", "value": 0.9}]}}),
        _panel(5, "gauge", "CPU throttled, worst pod", 18, 1, 6, 4,
               [{"expr": "max(sum by (pod) (rate(container_cpu_cfs_throttled_periods_total{%s}[$__rate_interval])) / "
                         "sum by (pod) (rate(container_cpu_cfs_periods_total{%s}[$__rate_interval])))" % (web, web),
                 "legendFormat": "throttled", "instant": True}], "percentunit",
               defaults={"min": 0, "max": 1, "thresholds": red}),
        _panel(6, "timeseries", "Request duration p99 by pod", 0, 5, 12, 8,
               [{"expr": "histogram_quantile(0.99, sum by (pod, le) (rate(http_request_duration_seconds_bucket{%s}"
                         "[$__rate_interval])))" % scope, "legendFormat": "{{pod}}"}], "s",
               description="Rails request duration, 99th percentile, one line per pod.", defaults={"min": 0}),
        _panel(7, "timeseries", "Requests by pod", 12, 5, 12, 8,
               [{"expr": "sum by (pod) (rate(http_requests_total{%s}[$__rate_interval]))" % scope, "legendFormat": "{{pod}}"}],
               "reqps", defaults={"min": 0}),
        _row(8, "Puma and CPU", 13),
        _panel(9, "timeseries", "Puma queued connections", 0, 14, 8, 8,
               [{"expr": "sum by (pod) (puma_queued_connections{%s})" % scope, "legendFormat": "{{pod}} queued"},
                {"expr": "sum by (pod) (puma_pool_capacity{%s})" % scope, "legendFormat": "{{pod}} free threads"}],
               "short", defaults={"min": 0}),
        _panel(10, "timeseries", "CPU usage against limit", 8, 14, 8, 8,
               [{"expr": "sum by (pod) (rate(container_cpu_usage_seconds_total{%s}[$__rate_interval]))" % web,
                 "legendFormat": "{{pod}}"},
                {"expr": 'min(kube_pod_container_resource_limits{%s, resource="cpu"})' % web, "legendFormat": "limit"}],
               "cores", defaults={"min": 0}),
        _panel(11, "timeseries", "CPU throttled periods", 16, 14, 8, 8,
               [{"expr": "sum by (pod) (rate(container_cpu_cfs_throttled_periods_total{%s}[$__rate_interval])) / "
                         "sum by (pod) (rate(container_cpu_cfs_periods_total{%s}[$__rate_interval]))" % (web, web),
                 "legendFormat": "{{pod}}"}], "percentunit", defaults={"min": 0, "max": 1}),
        _row(12, "Detail", 22),
        _panel(13, "table", "Pods and nodes", 0, 23, 12, 7,
               [{"expr": "kube_pod_info{%s}" % scope, "format": "table", "instant": True, "range": False}],
               transformations=[{"id": "organize", "options": {"excludeByName": {"Time": True, "Value": True, "__name__": True}}}],
               options={"showHeader": True, "cellHeight": "sm"}),
        dict(_panel(14, "logs", "Webservice log lines with timeout", 12, 23, 12, 7,
                    [{"expr": '{namespace="$namespace", pod=~"$pod", container="webservice"} |= "timeout"'}],
                    options={"showTime": True, "wrapLogMessage": True, "sortOrder": "Descending", "dedupStrategy": "none"}),
             datasource={"type": "loki", "uid": "loki-main"}),
    ]


def _ingress_panels():
    scope = 'namespace="$namespace", ingress="gitlab-webservice-default"'
    return [
        _panel(1, "stat", "Requests per second", 0, 0, 6, 4,
               [{"expr": "sum(rate(nginx_ingress_controller_requests{%s}[$__rate_interval]))" % scope, "legendFormat": "requests"}],
               "reqps"),
        _panel(2, "stat", "499 client closed, per second", 6, 0, 6, 4,
               [{"expr": 'sum(rate(nginx_ingress_controller_requests{%s, status="499"}[$__rate_interval]))' % scope,
                 "legendFormat": "499"}], "reqps",
               defaults={"thresholds": {"mode": "absolute", "steps": [{"color": "green", "value": None},
                                                                      {"color": "red", "value": 0.5}]}}),
        _panel(3, "gauge", "Share of 5xx", 12, 0, 6, 4,
               [{"expr": '(sum(rate(nginx_ingress_controller_requests{%s, status=~"5.."}[$__rate_interval])) / '
                         "sum(rate(nginx_ingress_controller_requests{%s}[$__rate_interval])))" % (scope, scope),
                 "legendFormat": "5xx", "instant": True}], "percentunit", defaults={"min": 0, "max": 1}),
        _panel(4, "timeseries", "Requests by status", 0, 4, 12, 8,
               [{"expr": "sum by (status) (rate(nginx_ingress_controller_requests{%s}[$__rate_interval]))" % scope,
                 "legendFormat": "{{status}}"}], "reqps", defaults={"min": 0}),
        _panel(5, "timeseries", "Request duration", 12, 4, 12, 8,
               [{"expr": "histogram_quantile(0.99, sum by (le) (rate(nginx_ingress_controller_request_duration_seconds_bucket"
                         "{%s}[$__rate_interval])))" % scope, "legendFormat": "p99"},
                {"expr": "histogram_quantile(0.5, sum by (le) (rate(nginx_ingress_controller_request_duration_seconds_bucket"
                         "{%s}[$__rate_interval])))" % scope, "legendFormat": "p50"}], "s", defaults={"min": 0}),
        _panel(6, "heatmap", "Request duration buckets", 0, 12, 24, 8,
               [{"expr": "sum by (le) (rate(nginx_ingress_controller_request_duration_seconds_bucket{%s}[$__rate_interval]))" % scope,
                 "format": "heatmap", "legendFormat": "{{le}}"}], "s",
               options={"calculate": False, "yAxis": {"unit": "s"}, "cellGap": 1}),
    ]


def dashboard(uid, title, folder, tags, start):
    """The body of GET /api/dashboards/uid/<uid>, or None for an unknown uid."""
    if uid == "gitlab-webservice":
        panels, pods = _webservice_panels(), "gitlab-webservice-default-.*"
    elif uid == "gitlab-ingress":
        panels, pods = _ingress_panels(), "gitlab-nginx-ingress-controller-.*"
    else:
        # The other listed dashboards carry no panels the study needs: one
        # text panel says so instead of pretending to be a real dashboard.
        panels, pods = [{"id": 1, "type": "text", "title": "About", "gridPos": {"h": 4, "w": 24, "x": 0, "y": 0},
                         "options": {"mode": "markdown", "content": "Synthetic placeholder: this study dashboard has no panels."}}], ".*"
    slug = "".join(char if char.isalnum() else "-" for char in title.lower()).strip("-")
    while "--" in slug:
        slug = slug.replace("--", "-")
    stamp = lambda seconds_before: time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(start - seconds_before))
    return {
        "meta": {"type": "db", "canSave": False, "canEdit": False, "canAdmin": False, "canStar": True, "canDelete": False,
                 "slug": slug, "url": "/d/%s/%s" % (uid, slug), "expires": "0001-01-01T00:00:00Z",
                 "created": stamp(40 * 24 * 3600), "updated": stamp(9 * 24 * 3600), "version": 7,
                 "folderTitle": folder, "folderUid": "folder-" + folder.lower(), "provisioned": True,
                 "provisionedExternalId": slug + ".json"},
        "dashboard": {
            "uid": uid, "title": title, "tags": tags, "timezone": "utc", "editable": False, "graphTooltip": 1,
            "schemaVersion": 41, "version": 7, "refresh": "30s", "time": {"from": "now-1h", "to": "now"},
            "timepicker": {"refresh_intervals": ["10s", "30s", "1m", "5m"]},
            "annotations": {"list": [{"builtIn": 1, "datasource": {"type": "grafana", "uid": "-- Grafana --"}, "enable": True,
                                      "hide": True, "iconColor": "rgba(0, 211, 255, 1)", "name": "Annotations & Alerts",
                                      "type": "dashboard"}]},
            "templating": _variables(pods), "links": [], "panels": panels}}

#!/usr/bin/env python3
"""Run a manual read-only usability session against synthetic loopback data.

Requires Python 3. This starts the actual local API handler, adds named study
objects, and launches the application in an isolated temporary runtime. No
cluster or cloud credentials are used. See docs/operator-task-study-2026-10-04.md.
"""

import argparse
from copy import deepcopy
from datetime import datetime, timedelta, timezone
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import types
from urllib.error import HTTPError
from urllib.parse import parse_qs, quote
from urllib.request import Request, urlopen


HERE = Path(__file__).resolve().parent
# Importing the journey module also imports its PTY rendering libraries, but
# this helper reuses only its API handlers and never invokes its renderer.
for dependency in ("pyte", "PIL", "PIL.Image", "PIL.ImageDraw", "PIL.ImageFont"):
    try:
        __import__(dependency)
    except ModuleNotFoundError:
        if dependency == "PIL":
            package = types.ModuleType("PIL")
            package.__path__ = []
            sys.modules["PIL"] = package
        elif dependency.startswith("PIL."):
            module = types.ModuleType(dependency)
            sys.modules[dependency] = module
            setattr(sys.modules["PIL"], dependency.rsplit(".", 1)[1], module)
        else:
            sys.modules[dependency] = types.ModuleType(dependency)
spec = importlib.util.spec_from_file_location("daily_workspace_journeys", HERE / "daily-workspace-journeys.py")
daily = importlib.util.module_from_spec(spec)
spec.loader.exec_module(daily)
demo = daily.journeys.demo
sys.path.insert(0, str(HERE))
import observability_sources_fixture as observed  # noqa: E402


class StudyHandler(daily.WorkspaceHandler):
    """Add explicit missing-coverage states while retaining existing guards."""

    def do_GET(self):
        proxied = observed.PROXY_PATH.match(self.path.split("?", 1)[0])
        if proxied and proxied.group(1) == observed.NAMESPACE:
            # Declared observability sources, reached the way the app reaches
            # them: through the API server service proxy. Read-only.
            parsed = self.record("GET")
            code, body = self.server.observability.handle(
                proxied.group(3), proxied.group(4), proxied.group(5) or "/", parse_qs(parsed.query))
            payload = json.dumps(body).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return None
        if self.path.startswith("/apis/metrics.k8s.io/"):
            self.record("GET")
            return self.failure(403, "Forbidden", "Synthetic fixture denies metrics API reads")
        if self.path.split("?", 1)[0] == "/api/v1/namespaces/ops/events":
            self.record("GET")
            return self.failure(403, "Forbidden", "Synthetic fixture denies ops event reads")
        # Only the one-page :problems read is denied (it sends neither a
        # resourceVersion nor a watch), so the DaemonSet list itself still
        # opens and shows its WHY column.
        if (self.path.split("?", 1)[0] == "/apis/apps/v1/daemonsets"
                and "resourceVersion=" not in self.path and "watch=" not in self.path):
            self.record("GET")
            return self.failure(403, "Forbidden", "Synthetic fixture denies cluster-wide DaemonSet reads")
        if self.path.split("?", 1)[0] == "/api/v1/namespaces/apps/events" and "fieldSelector=type%3DWarning" in self.path:
            # :problems lists Warning events by type; the inherited handler
            # only serves UID-scoped event reads for this namespace.
            self.record("GET")
            items = [deepcopy(o) for o in self.server.objects if o["kind"] == "Event"
                     and o["metadata"].get("namespace") == "apps" and o.get("type") == "Warning"]
            return self.reply({"apiVersion": "v1", "kind": "EventList",
                               "metadata": {"resourceVersion": "100"}, "items": items})
        if self.path.split("?", 1)[0] == "/api/v1/namespaces/apps/events" and "fieldSelector" not in self.path:
            # The inherited handler serves only UID-scoped events. A plain
            # namespace LIST (the :timeline read) returns every apps event.
            self.record("GET")
            items = [deepcopy(o) for o in self.server.objects if o["kind"] == "Event"
                     and o["metadata"].get("namespace") == "apps"]
            return self.reply({"apiVersion": "v1", "kind": "EventList",
                               "metadata": {"resourceVersion": "100"}, "items": items})
        return super().do_GET()


def study_objects(api, deny_sources=()):
    started = datetime.now(timezone.utc).replace(microsecond=0)
    now = started.isoformat().replace("+00:00", "Z")
    # Prometheus, Alertmanager and Grafana answers share the start time that
    # every "minutes ago" below is measured from.
    api.observability = observed.Sources(started.timestamp(), deny_sources)
    api.objects.extend(observed.service_objects(now))

    def obj(api_version, kind, name, namespace, labels, spec=None, status=None):
        return {"apiVersion": api_version, "kind": kind, "metadata": {
            "name": name, "namespace": namespace, "uid": "study-" + kind.lower() + "-" + name,
            "resourceVersion": "100", "generation": 2, "creationTimestamp": now, "labels": labels},
            "spec": spec or {}, "status": status or {}}

    def ago(minutes):
        return (datetime.now(timezone.utc).replace(microsecond=0) - timedelta(minutes=minutes)
                ).isoformat().replace("+00:00", "Z")

    def checkout_template(revision):
        """Two pod templates for :timeline: revision 2 changes the image, an
        environment key, a Secret key reference and the memory limit."""
        env = [{"name": "LOG_LEVEL", "value": "info"},
               {"name": "PAYMENT_TOKEN", "valueFrom": {"secretKeyRef": {
                   "name": "checkout-secrets", "key": "token-v%d" % revision}}}]
        if revision == 2:
            env.append({"name": "FEATURE_FLAGS", "value": "synthetic-flag-value-must-not-be-shown"})
        return {"metadata": {"labels": {"app": "checkout"}}, "spec": {"containers": [{
            "name": "api", "image": "example.test/checkout:v%d" % revision, "env": env,
            "envFrom": [{"configMapRef": {"name": "checkout-config"}}],
            "resources": {"requests": {"cpu": "100m", "memory": "128Mi"},
                          "limits": {"memory": "512Mi" if revision == 1 else "256Mi"}}}]}}

    def timed(value, created, **metadata):
        value["metadata"]["creationTimestamp"] = created
        value["metadata"].update(metadata)
        return value

    def event(name, minutes_first, minutes_last, kind, involved, reason, kind_type, count, message, component):
        return {"apiVersion": "v1", "kind": "Event", "metadata": {
            "name": name, "namespace": "apps", "uid": "study-event-" + name, "resourceVersion": "100",
            "creationTimestamp": ago(minutes_first)},
            "involvedObject": {"apiVersion": "v1", "kind": kind, "name": involved, "namespace": "apps",
                               "uid": "study-" + kind.lower() + "-" + involved},
            "reason": reason, "type": kind_type, "count": count,
            "firstTimestamp": ago(minutes_first), "lastTimestamp": ago(minutes_last),
            "source": {"component": component}, "message": message}

    deployment_owner = [{"apiVersion": "apps/v1", "kind": "Deployment", "name": "checkout-api",
                         "uid": "study-deployment-checkout-api", "controller": True}]
    new_set, new_pod = "checkout-api-7d9f8c6b5d", "checkout-api-7d9f8c6b5d-x2x9q"

    # The base fixture already contains investigation-api with current
    # CrashLoopBackOff plus a previous OOMKilled termination and retained event.
    # Add stable names for the remaining participant tasks.
    api.objects.extend([
        obj("v1", "Pod", "worker", "ops", {"app": "daily", "role": "worker"},
            {"containers": [{"name": "worker", "image": "example.test/worker:v1"}]},
            {"phase": "Pending", "conditions": [
                {"type": "PodScheduled", "status": "False", "reason": "Unschedulable",
                 "message": "0/1 nodes available: insufficient memory"},
                {"type": "Ready", "status": "False", "reason": "PodScheduled"}]}),
        obj("apps/v1", "Deployment", "checkout-api", "apps", {"app": "daily", "component": "checkout"},
            {"replicas": 2, "selector": {"matchLabels": {"app": "checkout"}},
             "template": checkout_template(2)},
            {"observedGeneration": 1, "replicas": 2, "updatedReplicas": 1,
             "readyReplicas": 1, "availableReplicas": 1, "unavailableReplicas": 1,
             "conditions": [{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded",
                             "lastTransitionTime": ago(2),
                             "message": "Synthetic rollout has not completed"}]}),
        obj("v1", "Service", "checkout-api", "apps", {"app": "daily", "component": "checkout"},
            {"type": "ClusterIP", "clusterIP": "10.0.0.20", "selector": {"app": "checkout"},
             "ports": [{"name": "http", "port": 8080, "targetPort": 8080}]}),
        obj("v1", "Pod", "checkout-api-0", "apps", {"app": "checkout", "component": "checkout"},
            {"containers": [{"name": "api", "image": "example.test/checkout:v2"}]},
            {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}],
             "containerStatuses": [{"name": "api", "ready": True, "restartCount": 0,
                                   "state": {"running": {"startedAt": now}}}]}),
    ])
    # One object per list that reports a cause in its own status, so the WHY
    # column and the list summary can be seen for every workload kind.
    def node(name, conditions, unschedulable=False):
        value = obj("v1", "Node", name, "", {"kubernetes.io/role": "worker"},
                    {"unschedulable": unschedulable},
                    {"conditions": conditions, "nodeInfo": {"kubeletVersion": "v1.33.1", "architecture": "arm64"}})
        del value["metadata"]["namespace"]
        return value

    pod_template = {"metadata": {"labels": {"app": "study"}},
                    "spec": {"containers": [{"name": "main", "image": "example.test/study:v1"}]}}
    api.objects.extend([
        node("worker-b", [
            {"type": "MemoryPressure", "status": "Unknown", "reason": "NodeStatusUnknown"},
            {"type": "Ready", "status": "Unknown", "reason": "NodeStatusUnknown",
             "message": "Kubelet stopped posting node status."}], unschedulable=True),
        node("worker-c", [
            {"type": "DiskPressure", "status": "True", "reason": "KubeletHasDiskPressure",
             "message": "kubelet has disk pressure"},
            {"type": "Ready", "status": "True", "reason": "KubeletReady", "message": "kubelet is posting ready status"}]),
        obj("batch/v1", "Job", "report-export", "ops", {"app": "study"},
            {"backoffLimit": 6, "completions": 1, "template": pod_template},
            {"active": 1, "failed": 2, "startTime": now}),
        obj("batch/v1", "Job", "ledger-reconcile", "ops", {"app": "study"},
            {"backoffLimit": 2, "completions": 1, "activeDeadlineSeconds": 600, "template": pod_template},
            {"failed": 1, "startTime": now, "conditions": [
                {"type": "Failed", "status": "True", "reason": "DeadlineExceeded",
                 "message": "Job was active longer than specified deadline"}]}),
        obj("apps/v1", "StatefulSet", "ledger-db", "ops", {"app": "study"},
            {"replicas": 3, "serviceName": "ledger-db", "selector": {"matchLabels": {"app": "study"}},
             "template": pod_template},
            {"observedGeneration": 2, "replicas": 3, "readyReplicas": 1, "updatedReplicas": 1,
             "currentRevision": "ledger-db-7c9", "updateRevision": "ledger-db-5f4"}),
        obj("apps/v1", "DaemonSet", "log-agent", "ops", {"app": "study"},
            {"selector": {"matchLabels": {"app": "study"}}, "template": pod_template},
            {"observedGeneration": 2, "desiredNumberScheduled": 3, "currentNumberScheduled": 3,
             "updatedNumberScheduled": 3, "numberReady": 2, "numberAvailable": 2, "numberUnavailable": 1}),
        obj("v1", "PersistentVolumeClaim", "ledger-data", "ops", {"app": "study"},
            {"accessModes": ["ReadWriteOnce"], "storageClassName": "fast-ssd",
             "resources": {"requests": {"storage": "20Gi"}}},
            {"phase": "Pending"}),
        obj("v1", "PersistentVolumeClaim", "archive-data", "ops", {"app": "study"},
            {"accessModes": ["ReadWriteOnce"], "storageClassName": "standard", "volumeName": "pv-archive-01",
             "resources": {"requests": {"storage": "5Gi"}}},
            {"phase": "Bound", "accessModes": ["ReadWriteOnce"], "capacity": {"storage": "5Gi"}}),
        obj("v1", "Service", "public-gateway", "ops", {"app": "study"},
            {"type": "LoadBalancer", "clusterIP": "10.0.0.40", "selector": {"app": "study"},
             "ports": [{"name": "https", "port": 443, "targetPort": 8443}]},
            {"loadBalancer": {}}),
    ])
    for group, entry in (("v1", ("persistentvolumeclaims", "PersistentVolumeClaim", True)),
                         ("apps/v1", ("statefulsets", "StatefulSet", True)),
                         ("apps/v1", ("daemonsets", "DaemonSet", True))):
        if entry not in demo.GROUPS[group]:
            demo.GROUPS[group].append(entry)
    # Cross-kind objects for :problems: a second failing Pod under the
    # revision-2 ReplicaSet that :timeline describes (the ReplicaSet ties both
    # Pods to the stalled Deployment by controller UID), a partly ready
    # StatefulSet, a failed Job, a Pending claim, and Warning events about some
    # of them. Nine minutes ago is after that rollout and its first back-off,
    # so these objects do not move the fault :timeline reports.
    earlier = ago(9)

    def owned(item, kind, name):
        item["metadata"]["ownerReferences"] = [{
            "apiVersion": "apps/v1", "kind": kind, "name": name, "uid": "study-" + kind.lower() + "-" + name,
            "controller": True}]
        return item

    def warning_event(name, namespace, kind, target, reason, message, count):
        return {"apiVersion": "v1", "kind": "Event", "metadata": {
            "name": name, "namespace": namespace, "uid": "study-event-" + name,
            "resourceVersion": "100", "creationTimestamp": earlier},
            "involvedObject": {"kind": kind, "name": target, "namespace": namespace,
                               "uid": "study-" + kind.lower() + "-" + target},
            "reason": reason, "type": "Warning", "count": count, "firstTimestamp": earlier, "lastTimestamp": now,
            "message": message}

    def crashing(name, reason, message):
        return owned(obj("v1", "Pod", name, "apps", {"app": "checkout"},
                         {"containers": [{"name": "api", "image": "example.test/checkout:v2"}]},
                         {"phase": "Running" if reason == "CrashLoopBackOff" else "Pending", "conditions": [
                             {"type": "Ready", "status": "False", "reason": "ContainersNotReady",
                              "lastTransitionTime": earlier}],
                          "containerStatuses": [{"name": "api", "ready": False, "restartCount": 6,
                                                "state": {"waiting": {"reason": reason, "message": message}}}]}),
                     "ReplicaSet", new_set)

    api.objects.extend([
        crashing(new_set + "-p8wm", "ImagePullBackOff",
                 'Back-off pulling image "example.test/checkout:v2"'),
        obj("apps/v1", "StatefulSet", "ledger", "ops", {"app": "ledger"},
            {"replicas": 3}, {"observedGeneration": 2, "replicas": 3, "readyReplicas": 2, "updatedReplicas": 3}),
        obj("batch/v1", "Job", "nightly-export", "ops", {"app": "export"}, {"backoffLimit": 2},
            {"failed": 3, "conditions": [{"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded",
                                          "message": "Job has reached the specified backoff limit",
                                          "lastTransitionTime": earlier}]}),
        obj("v1", "PersistentVolumeClaim", "ledger-data-2", "apps", {"app": "ledger"},
            {"storageClassName": "fast"}, {"phase": "Pending"}),
        warning_event("ledger-data-2-provisioning", "apps", "PersistentVolumeClaim", "ledger-data-2", "ProvisioningFailed",
              'storageclass.storage.k8s.io "fast" not found', 9),
        warning_event("ingress-sync", "apps", "Service", "checkout-api", "FailedToUpdateEndpointSlices",
              "Error updating Endpoint Slices for Service apps/checkout-api", 2),
    ])
    for resource in (("replicasets", "ReplicaSet", True), ("statefulsets", "StatefulSet", True),
                     ("daemonsets", "DaemonSet", True)):
        if resource not in demo.GROUPS["apps/v1"]:
            demo.GROUPS["apps/v1"].append(resource)
    if not any(name == "persistentvolumeclaims" for name, _, _ in demo.GROUPS["v1"]):
        demo.GROUPS["v1"].append(("persistentvolumeclaims", "PersistentVolumeClaim", True))
    # :timeline evidence. Fourteen minutes ago the ConfigMap was edited, twelve
    # minutes ago revision 2 rolled out, and ten minutes ago its pod began to
    # crash. The order is observable; the fixture asserts no cause.
    api.objects.extend([
        timed(obj("apps/v1", "ReplicaSet", "checkout-api-6c8d7f9b5", "apps", {"app": "checkout"},
                  {"replicas": 1, "selector": {"matchLabels": {"app": "checkout"}}, "template": checkout_template(1)},
                  {"replicas": 1, "readyReplicas": 1}), ago(3 * 24 * 60), ownerReferences=deployment_owner,
              annotations={"deployment.kubernetes.io/revision": "1", "deployment.kubernetes.io/desired-replicas": "2"}),
        timed(obj("apps/v1", "ReplicaSet", new_set, "apps", {"app": "checkout"},
                  {"replicas": 2, "selector": {"matchLabels": {"app": "checkout"}}, "template": checkout_template(2)},
                  {"replicas": 2, "readyReplicas": 0}), ago(12), ownerReferences=deployment_owner,
              annotations={"deployment.kubernetes.io/revision": "2", "deployment.kubernetes.io/desired-replicas": "2"}),
        timed(obj("v1", "Pod", new_pod, "apps", {"app": "checkout", "component": "checkout"},
                  dict(checkout_template(2)["spec"], nodeName="fixture-node"),
                  {"phase": "Running", "conditions": [{"type": "Ready", "status": "False"}],
                   "containerStatuses": [{"name": "api", "ready": False, "restartCount": 6,
                                          "state": {"waiting": {"reason": "CrashLoopBackOff",
                                                                "message": "back-off restarting failed container"}},
                                          "lastState": {"terminated": {"reason": "Error", "exitCode": 1,
                                                                       "startedAt": ago(3), "finishedAt": ago(2)}}}]}),
              ago(12), ownerReferences=[{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": new_set,
                                         "uid": "study-replicaset-" + new_set, "controller": True}]),
        timed({"apiVersion": "v1", "kind": "ConfigMap", "metadata": {
            "name": "checkout-config", "namespace": "apps", "uid": "study-configmap-checkout-config",
            "resourceVersion": "100"}, "data": {"PAYMENT_URL": "synthetic-value-must-not-be-shown"}},
            ago(3 * 24 * 60), managedFields=[
                {"manager": "kubectl-create", "operation": "Update", "apiVersion": "v1", "time": ago(3 * 24 * 60)},
                {"manager": "kubectl-edit", "operation": "Update", "apiVersion": "v1", "time": ago(14)}]),
        timed({"apiVersion": "v1", "kind": "Secret", "metadata": {
            "name": "checkout-secrets", "namespace": "apps", "uid": "study-secret-checkout-secrets",
            "resourceVersion": "100"}, "type": "Opaque",
            "data": {"token-v2": "c3ludGhldGljLXNlY3JldC1tdXN0LW5vdC1iZS1zaG93bg=="}},
            ago(30 * 24 * 60), managedFields=[
                {"manager": "external-secrets", "operation": "Apply", "apiVersion": "v1", "time": ago(125)}]),
        timed({"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler", "metadata": {
            "name": "checkout-api", "namespace": "apps", "uid": "study-hpa-checkout-api", "resourceVersion": "100"},
            "spec": {"scaleTargetRef": {"apiVersion": "apps/v1", "kind": "Deployment", "name": "checkout-api"},
                     "minReplicas": 2, "maxReplicas": 6},
            "status": {"currentReplicas": 2, "desiredReplicas": 2, "lastScaleTime": ago(26 * 60), "conditions": [
                {"type": "AbleToScale", "status": "True", "reason": "ReadyForNewScale",
                 "lastTransitionTime": ago(3 * 24 * 60), "message": "recommended size matches current size"},
                {"type": "ScalingActive", "status": "False", "reason": "FailedGetResourceMetric",
                 "lastTransitionTime": ago(8),
                 "message": "the HPA was unable to compute the replica count: metrics API denied"}]}},
            ago(3 * 24 * 60)),
        event("checkout-scaled", 12, 12, "Deployment", "checkout-api", "ScalingReplicaSet", "Normal", 1,
              "Scaled up replica set " + new_set + " to 1", "deployment-controller"),
        event("checkout-started", 11, 3, "Pod", new_pod, "Started", "Normal", 7,
              "Started container api", "kubelet"),
        event("checkout-backoff", 10, 1, "Pod", new_pod, "BackOff", "Warning", 14,
              "Back-off restarting failed container api in pod " + new_pod, "kubelet"),
    ])
    for node in api.objects:
        if node["kind"] == "Node" and node["metadata"]["name"] == "fixture-node":
            conditions = node.setdefault("status", {}).setdefault("conditions", [])
            if not any(c.get("type") == "Ready" for c in conditions):
                conditions.append({"type": "Ready", "status": "True", "reason": "KubeletReady",
                                   "message": "kubelet is posting ready status"})
            for condition in conditions:
                condition.setdefault("lastTransitionTime", ago(6 * 24 * 60))
    for group, entry in (("v1", ("configmaps", "ConfigMap", True)), ("apps/v1", ("replicasets", "ReplicaSet", True)),
                         ("autoscaling/v2", ("horizontalpodautoscalers", "HorizontalPodAutoscaler", True))):
        if entry not in demo.GROUPS.setdefault(group, []):
            demo.GROUPS[group].append(entry)
    # Services are part of the existing core-v1 discovery in the test helpers.
    if not any(name == "services" for name, _, _ in demo.GROUPS["v1"]):
        demo.GROUPS["v1"].append(("services", "Service", True))
    if "services" not in api.workspace_resources:
        api.workspace_resources.add("services")
    api.RequestHandlerClass = StudyHandler


def create_kubeconfig(path, port):
    path.write_text(f"""apiVersion: v1
kind: Config
clusters:
- name: study-loopback
  cluster:
    server: http://127.0.0.1:{port}
contexts:
- name: study-dev
  context:
    cluster: study-loopback
    user: study-user
    namespace: apps
current-context: study-dev
users:
- name: study-user
  user: {{}}
""")
    path.chmod(0o600)


def isolated_environment(root, kubeconfig):
    env = {k: v for k, v in os.environ.items() if not k.startswith((
        "K9PLUS_", "K9S_", "KUBECONFIG", "KUBECACHEDIR", "XDG_", "AWS_", "AZURE_", "GOOGLE_"))
           and "TOKEN" not in k.upper() and "PASSWORD" not in k.upper() and "CREDENTIAL" not in k.upper()}
    for name in ("config", "data", "state", "cache", "logs"):
        (root / name / "k9plus").mkdir(parents=True, exist_ok=True)
    env.update({"KUBECONFIG": str(kubeconfig), "KUBECACHEDIR": str(root / "kube-cache"),
                "K9PLUS_CONFIG_DIR": str(root / "config" / "k9plus"),
                "K9PLUS_LOGS_DIR": str(root / "logs" / "k9plus"),
                "XDG_CONFIG_HOME": str(root / "config"), "XDG_DATA_HOME": str(root / "data"),
                "XDG_STATE_HOME": str(root / "state"), "XDG_CACHE_HOME": str(root / "cache"),
                "K9PLUS_NO_UPDATE_CHECK": "1"})
    return env


def named_get(base, path):
    with urlopen(base + path, timeout=2) as response:
        return json.loads(response.read())


def smoke_sources(base):
    """The declared sources answer through the service proxy and tell the
    checkout-api story: healthy before the rollout, failing after it."""
    proxy = "/api/v1/namespaces/monitoring/services/%s/proxy"
    prometheus, alertmanager, grafana = (proxy % s for s in ("prometheus:9090", "alertmanager:9093", "grafana:3000"))
    now = datetime.now(timezone.utc).timestamp()

    def instant(expression, at):
        value = named_get(base, prometheus + "/api/v1/query?query=" + quote(expression) + "&time=%.3f" % at)
        assert value["status"] == "success", value
        return value["data"]["result"]

    ratio = ('(sum(rate(http_requests_total{namespace="apps",service="checkout-api",code=~"5.."}[300s]))'
             ' / sum(rate(http_requests_total{namespace="apps",service="checkout-api"}[300s])))')
    before, after = (float(instant(ratio, at)[0]["value"][1]) for at in (now - 3600, now))
    assert before < 0.01 < 0.15 < after, (before, after)
    latency = ('histogram_quantile(0.95, sum by (le) (rate(http_request_duration_seconds_bucket'
               '{namespace="apps",service="checkout-api"}[300s])))')
    fast, slow = (float(instant(latency, at)[0]["value"][1]) for at in (now - 3600, now))
    assert fast < 0.5 < 1.0 < slow, (fast, slow)
    limits = instant('kube_pod_container_resource_limits{namespace="apps",resource="memory"}', now)
    assert {int(item["value"][1]) for item in limits} == {256 * 1024 * 1024, 512 * 1024 * 1024}, limits
    memory = named_get(base, prometheus + "/api/v1/query_range?query=" + quote(
        'container_memory_working_set_bytes{namespace="apps",pod="%s"}' % observed.NEW_POD) +
        "&start=%.3f&end=%.3f&step=15" % (now - 1800, now))["data"]["result"]
    values = [float(v) for _, v in memory[0]["values"]]
    assert len(memory) == 1 and max(values) <= 256 * 1024 * 1024 and max(values) > 240 * 1024 * 1024, max(values)
    assert float(memory[0]["values"][0][0]) >= now - observed.ROLLOUT - 30, "new pod has samples before the rollout"
    silent = instant('node_memory_MemAvailable_bytes{node="worker-b"}', now)
    assert silent == [], "worker-b must have no node-exporter sample after it went silent"
    alerts = named_get(base, alertmanager + "/api/v2/alerts")
    states = sorted((a["labels"]["alertname"], a["status"]["state"]) for a in alerts)
    assert ("CheckoutHighErrorRate", "active") in states and ("Watchdog", "active") in states, states
    assert sum(1 for a in alerts if a["status"]["silencedBy"]) == 1
    assert sum(1 for a in alerts if a["status"]["inhibitedBy"]) == 1
    scoped = named_get(base, alertmanager + "/api/v2/alerts?filter=" + quote('namespace="apps"'))
    assert len(scoped) == 3, [a["labels"]["alertname"] for a in scoped]
    assert named_get(base, grafana + "/api/health")["database"] == "ok"
    assert {d["uid"] for d in named_get(base, grafana + "/api/search?type=dash-db")} >= {"k8s-pod", "k8s-node"}
    notes = named_get(base, grafana + "/api/annotations?from=%d&to=%d&tags=deploy" % ((now - 3600) * 1000, now * 1000))
    assert len(notes) == 1 and "revision 2" in notes[0]["text"], notes
    through = named_get(base, grafana + "/api/datasources/proxy/uid/prom-main/api/v1/query?query=" + quote("up{}"))
    assert through["status"] == "success" and len(through["data"]["result"]) == 3
    denied = observed.Sources(deny=("grafana",))
    assert denied.handle("grafana", "3000", "/api/health", {})[0] == 403
    assert denied.handle("prometheus", "9090", "/api/v1/status/buildinfo", {})[0] == 200
    return ["Prometheus, Alertmanager and Grafana answer through the monitoring service proxy",
            "checkout-api 5xx ratio %.3f before the rollout, %.3f now; p95 %.2fs before, %.2fs now" % (before, after, fast, slow),
            "worker-b has no node-exporter sample after it stopped reporting",
            "one silenced and one inhibited alert; --deny-source answers HTTP 403"]


def smoke():
    api = daily.WorkspaceAPI()
    study_objects(api)
    thread = threading.Thread(target=api.serve_forever, daemon=True)
    thread.start()
    base = f"http://127.0.0.1:{api.server_port}"
    checks = []
    try:
        for path, expected_kind in (
            ("/api/v1/namespaces/apps/pods/investigation-api", "Pod"),
            ("/api/v1/namespaces/ops/pods/worker", "Pod"),
            ("/apis/apps/v1/namespaces/apps/deployments/checkout-api", "Deployment"),
            ("/api/v1/namespaces/apps/services/checkout-api", "Service"),
        ):
            value = named_get(base, path)
            assert value["kind"] == expected_kind, (path, value.get("kind"))
            checks.append("GET " + path)
        for path in ("/apis/metrics.k8s.io/v1beta1/namespaces/apps/pods",
                     "/api/v1/namespaces/ops/events"):
            try:
                named_get(base, path)
                raise AssertionError("fixture did not preserve the configured evidence gap: " + path)
            except HTTPError as error:
                assert error.code == 403, (path, error.code)
                checks.append("GET " + path + " returned synthetic HTTP 403")
        worker = named_get(base, "/api/v1/namespaces/ops/pods/worker")
        condition = next(c for c in worker["status"]["conditions"] if c["type"] == "PodScheduled")
        assert condition["status"] == "False" and condition["reason"] == "Unschedulable"
        investigation = named_get(base, "/api/v1/namespaces/apps/pods/investigation-api")
        api_status = next(s for s in investigation["status"]["containerStatuses"] if s["name"] == "api")
        assert api_status["state"]["waiting"]["reason"] == "CrashLoopBackOff"
        assert api_status["lastState"]["terminated"]["reason"] == "OOMKilled"
        deployment = named_get(base, "/apis/apps/v1/namespaces/apps/deployments/checkout-api")
        assert deployment["status"]["observedGeneration"] < deployment["metadata"]["generation"]
        assert deployment["status"]["readyReplicas"] < deployment["spec"]["replicas"]
        service = named_get(base, "/api/v1/namespaces/apps/services/checkout-api")
        pod = named_get(base, "/api/v1/namespaces/apps/pods/checkout-api-0")
        assert all(pod["metadata"]["labels"].get(key) == value
                   for key, value in service["spec"]["selector"].items())
        assert service["spec"]["type"] == "ClusterIP" and service["spec"]["clusterIP"] == "10.0.0.20"
        auth = Request(base + "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews",
                       data=b"{}", method="POST", headers={"Content-Type": "application/json"})
        with urlopen(auth, timeout=2) as response:
            review = json.loads(response.read())
            assert response.status == 200 and review["status"]["allowed"] is True
            checks.append("synthetic SelfSubjectAccessReview POST allowed")
        for method in ("POST", "PATCH", "PUT", "DELETE"):
            path = ("/apis/apps/v1/namespaces/apps/deployments" if method == "POST" else
                    "/apis/apps/v1/namespaces/apps/deployments/checkout-api")
            request = Request(base + path,
                              data=b"{}", method=method, headers={"Content-Type": "application/json"})
            try:
                urlopen(request, timeout=2)
                raise AssertionError(f"fixture accepted {method}")
            except HTTPError as error:
                assert error.code == 405, (method, error.code)
                checks.append(method + " rejected with HTTP 405")
        checks.extend(smoke_sources(base))
        return checks
    finally:
        api.shutdown()
        api.server_close()
        thread.join(timeout=2)
        assert not thread.is_alive(), "fixture server did not stop"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, help="application binary for the interactive session")
    parser.add_argument("--journal", type=Path, help="new private JSON request journal path (never overwritten)")
    parser.add_argument("--smoke", action="store_true", help="check named objects, mutation rejection and server cleanup")
    parser.add_argument("--deny-source", action="append", default=[], choices=sorted(observed.SERVICES),
                        help="answer HTTP 403 for this declared observability source (repeatable)")
    parser.add_argument("--no-icons", action="store_true", help="start with ui.noIcons, the plain ASCII presentation")
    parser.add_argument("app_args", nargs=argparse.REMAINDER, help="optional application arguments after --")
    args = parser.parse_args()
    if args.smoke:
        print(json.dumps({"checks": smoke(), "result": "passed", "scope": "synthetic loopback API only"}, indent=2))
        return 0
    if not args.binary or not args.journal:
        parser.error("interactive mode requires --binary and --journal; use --smoke for bounded fixture verification")
    binary = args.binary.resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        parser.error("--binary must name an executable file")
    journal = args.journal.expanduser().resolve()
    if journal.exists():
        parser.error("--journal already exists; choose a new path")

    api = daily.WorkspaceAPI()
    study_objects(api, args.deny_source)
    thread = threading.Thread(target=api.serve_forever, daemon=True)
    thread.start()
    child = None
    try:
        with tempfile.TemporaryDirectory(prefix="k9plus-operator-study-") as directory:
            root = Path(directory)
            kubeconfig = root / "kubeconfig"
            create_kubeconfig(kubeconfig, api.server_port)
            env = isolated_environment(root, kubeconfig)
            (root / "config" / "k9plus" / "config.yaml").write_text(
                "k9s:\n  skipLatestRevCheck: true\n  refreshRate: 2\n  ui:\n    splashless: true\n"
                + ("    noIcons: true\n" if args.no_icons else ""))
            # Declare the three synthetic sources for the study context so the
            # observability features work with no setup. Declaring contacts nothing.
            context_dir = root / "config" / "k9plus" / "clusters" / "study-loopback" / "study-dev"
            context_dir.mkdir(parents=True, exist_ok=True)
            (context_dir / "config.yaml").write_text(observed.CONTEXT_CONFIG)
            command = [str(binary), "--kubeconfig", str(kubeconfig), "--readonly"]
            if args.app_args:
                command.extend(args.app_args[1:] if args.app_args[0] == "--" else args.app_args)
            print(f"Fixture API: http://127.0.0.1:{api.server_port} (synthetic data only)", flush=True)
            print("Context: study-dev; default namespace: apps; app is in read-only mode.", flush=True)
            print("Temporary kubeconfig and app state are removed when the app exits.", flush=True)
            child = subprocess.Popen(command, env=env)
            child.wait()
        return child.returncode
    except KeyboardInterrupt:
        if child is not None and child.poll() is None:
            child.send_signal(signal.SIGINT)
            try:
                child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                child.terminate()
                child.wait(timeout=3)
        return 130
    finally:
        if child is not None and child.poll() is None:
            child.terminate()
            try:
                child.wait(timeout=3)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=3)
        api.shutdown()
        api.server_close()
        thread.join(timeout=2)
        entries = deepcopy(api.requests)
        if not journal.exists():
            journal.parent.mkdir(parents=True, exist_ok=True)
            with journal.open("x", encoding="utf-8") as stream:
                json.dump({"fixture": "operator-study-loopback", "context": "study-dev",
                           "captured_at": datetime.now(timezone.utc).isoformat(), "requests": entries},
                          stream, indent=2)
                stream.write("\n")
            journal.chmod(0o600)
            rejected = [r for r in entries if r["method"] in ("PATCH", "PUT", "DELETE") or
                        r["method"] == "POST" and "selfsubjectaccessreviews" not in r["path"]]
            print(f"Request journal written with mode 0600: {journal}; requests={len(entries)}; "
                  f"resource mutation attempts={len(rejected)}", flush=True)


if __name__ == "__main__":
    raise SystemExit(main())

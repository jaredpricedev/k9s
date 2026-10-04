# Resource pressure investigation

Select a Pod, Deployment, DaemonSet, StatefulSet, ReplicaSet or Job and run
`:pressure`, or choose **Resource pressure** in the action palette. This is a
read-only snapshot. `r` refreshes it, `g` opens related resources, and Esc returns
and cancels waiting reads. The original context and object identity stay pinned.
An object recreated with the same name requires reopening the investigation.

The report preserves configuration and evidence for each application, init,
restartable init sidecar and ephemeral container. Init containers run in a
different phase; restartable init sidecars overlap application containers. The
report does not invent a total allocation by adding these phases together.
When present, the shared Pod resource budget, runtime overhead and enacted
container resources are identified separately by their Pod API source. A shared
Pod budget is not silently assigned to individual containers.

CPU uses millicores (`1000m` is one CPU). Memory uses binary MiB and retains the
original Kubernetes quantity. Requests are scheduling allocations; limits are
configured resource bounds. Usage/request and usage/limit percentages require a
fresh observation and a nonzero configured denominator. An absent value or a
zero request/limit produces `N/A`; an observed zero remains valid.

Current usage is optional. The source is `metrics.k8s.io/v1beta1/pods`, with the
server's observation time and sample window shown. Missing, denied, malformed,
stale (older than two minutes) or identity-mismatched observations produce `N/A`.
The Pod configuration, last termination evidence and scheduling events remain
available when metrics are unavailable. No Prometheus installation or new agent
is required. Metrics-server does not provide CPU throttling counters: throttling
stays **unknown**. This report does not infer historical peaks or throttling from
CPU utilization.

`OOMKilled`, termination times, exit codes and restart counts are reported from
the Pod API. Warning and scheduling events come from the Kubernetes Events API,
matched by Pod UID and namespace. Event source and time are retained. These are
observations, not a diagnosis that a particular limit caused the termination or
that a request caused a scheduling failure. Events expire, so an empty result is
not evidence of health.

Workload inspection uses the workload's nonempty selector in its own namespace.
The Pod lookup is bounded at 100 results; at most 20 Pods receive detailed
inspection, with at most 20 retained Events per Pod. Truncation is explicit. All
reads share the inspection's ten-second deadline and stop on cancellation. No
writes, allocation changes, eviction or remediation are performed.

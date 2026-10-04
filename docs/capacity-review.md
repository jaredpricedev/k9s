<!-- Modified for k9+; see NOTICE. -->
# Capacity and autoscaling review

Select a native Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet, Job,
Namespace or Node, then run `:capacity` or choose **Capacity and autoscaling
review** from Ctrl-O. The selected resource must have a captured UID. This
review performs only reads and works in read-only mode.

The overview separates accepted Pod requests, scheduling reservation, reported
limits and current usage. Pending Pods and exhausted quota are evidence checks,
not a diagnosis. The tabs provide Pod configuration and placement, admission
constraints, HPA/VPA evidence, per-node allocatable resources and full source
coverage. `1`–`6` select a tab; Tab/Shift-Tab cycle; `r` explicitly refreshes; `/`
searches the current tab; Esc returns. Each tab retains its search and scroll
position, including through resize and skin changes.

A workload scope uses its full label selector, including match expressions.
Selector membership is distinct from controller ownership. Workload autoscalers
are matched by target kind/name and reported API group; the autoscaler reference supplies no target
UID. Pod and Namespace reviews expose namespace-wide autoscaler/admission
objects. Node review uses the namespace active when the view opened and filters
Pods by assigned node; use a Namespace or workload review for unassigned Pending
Pods. Nodes are read at cluster scope unless one Node was selected. Neither
namespace Pod totals nor the visible node page represent all cluster allocations.

The selected resource is independently fetched and checked against its captured
UID before collecting seven fixed sources: Pods, Nodes, ResourceQuota,
LimitRange, autoscaling/v2 HPA, optional autoscaling.k8s.io/v1 VPA and optional
metrics.k8s.io/v1beta1 Pod usage. Each source has a three-second deadline and a
100-object page bound; a selected Pod's metrics use a named GET with a bound of
one. Continue tokens and oversized pages are explicit partial coverage, without
unbounded pagination or discovery. Reads are independent, so a denied node API
or unavailable metrics API does not suppress readable workload/admission data.
Source read/attempt times and actual object UIDs remain in Evidence. These
reads do not form an atomic cluster snapshot.

CPU and memory retain Kubernetes quantities. Effective requirements use the
Kubernetes v1.35 client helper for sequential init phases, cumulative restartable
sidecars, accepted Pod-level resources, overhead and observed resize allocation.
This does not determine the server's scheduler feature-gate configuration.
Unbounded container limits are explicit; the reported limit sum is not an
upper bound when a container has no nonzero limit. LimitRange defaults are not
silently applied to existing Pod specs, which already reflect admission.
ResourceQuota configured hard limits and controller-reported hard/used values
remain distinct; unreported consumption is unknown.

Usage has its own server timestamp and measurement window. Missing CPU/memory,
missing containers, denied reads, old samples, mismatched UIDs and samples or
windows predating the observed Pod do not become a measured zero. UID-less
PodMetrics responses state their weaker namespace/name/time association. Only
fresh, complete Pod samples enter overview usage totals. A sample that ages
while the review is open becomes stale without replacing its timestamp.

HPA configured targets, replica counts, controller generation, reported metric
inputs and conditions are shown separately. Unreported inputs/counts are
unknown, including configured inputs missing from a partially reported status. VPA recommendations are optional API-reported target/lower/upper
quantities with their update mode; they never become automatic rightsizing,
resource writes or historical trends. History remains **not configured** until
a provider and explicit coverage window exist. This review does not infer
schedulability from aggregate CPU, alter replicas, or read Secret objects.

A failed refresh retains the prior observation with its original source/time
and an explicit failure. Replacement identity or a changed destination requires
reopening the view. Fixture and terminal checks validate these paths; they do
not establish live-cluster integration or operator-study results.

See [native validation and reproduction](capacity-review-validation-2026-10-04.md).

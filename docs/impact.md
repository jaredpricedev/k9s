# Impact: before and after a rollout

`:impact` answers one question for a selected Deployment, StatefulSet or
DaemonSet (or one of their Pods): **what was measured before the last rollout,
and what was measured after it?** It reads the declared Prometheus source
([observability-sources.md](observability-sources.md)) once, on request, and
compares two equal windows either side of the rollout time, signal by signal.

It reports order in time. It says "after", never "because of". A worse number
after a rollout is a measurement, not a diagnosis; use `t` to open `:timeline`
and see what else changed around the same time.

## Opening it

Select a Deployment, StatefulSet, DaemonSet or Pod and type `:impact`, or pick
**Impact: before and after the last rollout** in `Ctrl-O`. A Pod is resolved
to the workload that controls it (through its ReplicaSet for a Deployment). A
Pod with no controller, or one controlled by something else such as a Job, is
refused with the reason.

Nothing is read until the view is opened. The view never refreshes on a timer.

| Key | Action |
| --- | --- |
| `r` | Read again |
| `1` `2` `3` `4` `5` | Window on each side: 5m, 15m, 30m (default), 1h, 3h |
| `p` / `n` | Previous (earlier) / next (later) retained rollout |
| `t` | Open `:timeline` for the same workload |
| `Enter` | Open `:graph` for the workload, only in a build that has that command; otherwise the key is unbound and no hint is shown |
| `Up` / `Down` | Select a signal; the pane below states its numbers, rule, sample counts and exact queries |
| `Esc` | Back |

## What is read

Kubernetes, three bounded reads at most (three-second timeout each):

1. For a Pod, the Pod and, for a Deployment's Pod, its ReplicaSet.
2. The workload.
3. Its revisions: ReplicaSets with the workload's selector for a Deployment,
   ControllerRevisions for a StatefulSet or DaemonSet; at most 200, filtered
   to those the workload controls.

Prometheus, nine range queries through the shared client (eight seconds and
1 MiB each, at most three in flight, no redirects, cancellable). Leaving the
view or asking again cancels the read in flight; a late answer is discarded.
Nothing is written anywhere.

## The rollout time and the windows

The rollout time is the creation time of the revision's ReplicaSet or
ControllerRevision. The header shows which revision is compared, how many are
retained, and the exact time.

- Both windows have the same width. The default is 30 minutes.
- The after window ends at the read time, or at the next rollout when a later
  one exists. The before window never reaches back past the previous rollout.
  When either limit makes the windows narrower than asked, the header says so
  and why, for example `12m each (asked 30m; the rollout was 12m before this
  read)`.
- Less than one minute on a side is not compared.
- A revision created *before* the revision it replaced is a rollback onto an
  older ReplicaSet. Kubernetes does not record when a rollback happened, so no
  windows are placed around it; the view says so and `p` steps to an earlier
  rollout.
- Revisions pruned by the workload's history limit cannot be shown.

The header also states the source, the read time, the sample step (chosen by
the shared client so a query asks for at most 120 sample times) and the range
every rate is taken over: four steps, at least two minutes.

## What changed

One line lists what the revision's pod template changed against the one it
replaced: image tags, resource requests and limits, and environment variable
*names* (`+` added, `-` removed, `~` changed). Values and command lines are
never read into the view. This is the same comparison `:timeline` shows for a
rollout entry. The earliest retained revision has nothing to compare with.

## Signals

| Signal | Query template (fixed) | One side becomes | Direction |
| --- | --- | --- | --- |
| Request rate | `sum(rate(REQUESTS{sel}[w]))` | mean of samples | neutral |
| Error ratio | the above, and the same with `CODE=~"ERRORS"` | summed errors / summed requests | higher is worse |
| Latency p50, p95, p99 | `sum by (le) (rate(BUCKETS{sel}[w]))` | quantile of the summed bucket rates | higher is worse |
| Restarts | `sum(kube_pod_container_status_restarts_total{pods})` | sum of the counter's upward steps | higher is worse |
| Memory peak | `max(container_memory_working_set_bytes{containers})` | maximum | neutral |
| Memory/limit | `max(max by (pod,container)(working set) / max by (pod,container)(kube_pod_container_resource_limits{resource="memory"}))` | maximum | higher is worse |
| CPU usage | `sum(rate(container_cpu_usage_seconds_total{containers}[w]))` | mean | neutral |
| CPU throttled | throttled periods rate / periods rate (`container_cpu_cfs_*`) | mean | higher is worse |
| Ready (min) | `max(kube_deployment_status_replicas_available)`, `kube_statefulset_status_replicas_ready` or `kube_daemonset_status_number_ready` | minimum | lower is worse |

`pods` is `namespace="NS", pod=~"PATTERN"`, where the pattern is
`NAME-[a-z0-9]+-[a-z0-9]+` for a Deployment, `NAME-[0-9]+` for a StatefulSet
and `NAME-[a-z0-9]+` for a DaemonSet. `containers` adds `container!=""` and
`container!="POD"`. Pods are matched by name, not by owner, because metrics
carry the pod name and not the owner chain; a different workload whose pods
happen to follow the same pattern would be included.

Quantiles are interpolated the way Prometheus does it, from the bucket rates
summed over the whole window, so busy minutes weigh more than quiet ones. The
error ratio is likewise summed errors over summed requests, not an average of
per-step ratios. When request series exist and no error series does, errors
count as zero (a counter for an outcome that has not happened does not exist)
and the detail pane says so.

After-samples whose rate range still reaches back across the rollout would
blend both sides. They are left out of the numbers (the detail pane counts
them) but still drawn in the trend.

The trend column shows the before half, a rollout mark, and the after half on
one shared scale from zero. A blank cell had no sample. With `noIcons` the
trend uses the ASCII ramp `_.:-=+*#` and `|`.

### Metric names for HTTP signals

The request, error and latency queries use a small default set that can be
overridden per context, next to the source declarations:

```yaml
k9s:
  observability:
    red:
      requests: http_requests_total                          # default
      codeLabel: code                                        # default
      errorCodes: "5.."                                      # default, a regular expression
      durationBuckets: http_request_duration_seconds_bucket  # default, seconds, with le
      namespaceLabel: namespace                              # default
      podLabel: pod                                          # default
      workloadLabel: service                                 # default empty
```

With `workloadLabel` empty, HTTP series are selected by `podLabel` and the pod
name pattern above. Set it (for example to `service`, `deployment`, `job` or
`app`) when your series carry the workload name in a label; the selector then
is `namespaceLabel="NS", workloadLabel="NAME"`. Names are validated as
Prometheus metric and label names; an unusable mapping is reported and nothing
is queried. The mapping only fills the fixed templates above: there is no way
to supply query text. The container and kube-state-metrics names are fixed.

## Verdicts

A signal is compared only when **both** windows hold at least **4** usable
samples. Then it has changed when the difference reaches **both** its absolute
floor **and 10 % of the before value**:

| Signal | Floor |
| --- | --- |
| Request rate | 0.1 req/s |
| Error ratio | 0.1 percentage points |
| Latency | 5 ms |
| Restarts | 1 |
| Memory peak | 1 Mi |
| Memory/limit | 5 percentage points |
| CPU usage | 10 millicores |
| CPU throttled | 1 percentage point |
| Ready (min) | 1 |

A changed signal is `worse` or `better` when it has a direction and `higher`
or `lower` when it is neutral; otherwise it is `unchanged`. The rule is shown
next to the selected signal. Worse rows carry a `!` as well as the word, so
the meaning does not depend on colour.

Signals that were not compared say why, and show `-` rather than a number:

| Word | Meaning |
| --- | --- |
| `not enough samples` | Series exist, but a window has under 4 usable samples, or nothing was counted in it (for example no requests, so no ratio). A side that was read still shows its value. |
| `not available` | The source answered and no series matched. Typical for an app without HTTP metrics, or CPU throttling when no CPU limit is set. |
| `not read` | The read was denied, timed out, failed or exceeded a limit. |

A missing signal is never a zero and never counts as "unchanged".

## The summary line

`After rev 2 (04:20): error ratio 0.2% -> 14.1%, p99 180ms -> 2.4s, memory at
limit (limit 512Mi -> 256Mi).`

It lists the signals that are worse by the rule above, in a fixed order, with
one latency figure (the highest percentile that moved). "memory at limit"
means the peak working set reached 90 % of the limit; when the revision also
changed the limit, both values follow in brackets, taken from the two pod
templates. When nothing is worse it says so, and how many signals were
compared. When the request rate rose or fell by a factor of two or more it
adds that error ratio and latency are not like-for-like.

## Limits

- Order is not cause. Other changes (a ConfigMap edit, a node problem, a
  traffic shift, a dependency) can fall in the same window.
- The rollout time is when the revision object was created, not when its
  pods became ready or when the old ones stopped serving. The first minutes
  of the after window include the rollout itself.
- A paused rollout, a canary that shares the workload name, or a HorizontalPod
  Autoscaler changing replica counts all change the numbers without a new
  revision.
- Windows are compared as wholes; there is no significance test. With few
  samples or bursty traffic a threshold can be crossed by chance.
- Restarts are the upward steps of the summed restart counter. A restart and
  the removal of a pod with restarts in the same step can cancel out.
- Memory/limit needs kube-state-metrics; containers without a limit have no
  ratio. CPU throttling series exist only for containers with a CPU limit.
- More than 32 histogram buckets exceed the shared series limit; latency is
  then reported `not read` rather than computed from a partial histogram.
- Prometheus retention bounds how far back `p` is useful.
- A 403 from the API server's service proxy and a 403 from Prometheus itself
  both read `denied`.
- `Enter` passes the workload to `:graph`, not the selected signal.

## Synthetic fixture

`scripts/operator-study-fixture.py` declares `red.workloadLabel: service` for
its context, so the checkout-api story reads: request rate about 40/s on both
sides, error ratio about 0.2 % before and 22 % after, p99 0.5 s before and 4 s
after, six restarts, memory peak at 99 % of the new 256Mi limit, ready replicas
2 then 1, and CPU throttling `not available` (no CPU limit is set).

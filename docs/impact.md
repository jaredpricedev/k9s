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
| `n` / `Shift-N` | Next (later) / previous (earlier) retained rollout, the same pair that steps through episodes in `:slow` and signals in `:graph` |
| `t` | Open `:timeline` for the same workload |
| `Enter` | Open `:graph` for the workload over the narrowest graph window that reaches back to the start of the before window, scrolled to the chart of the selected signal (memory signals and restarts: MEMORY; CPU signals: CPU). For a signal `:graph` does not draw (request rate, errors, latency, readiness) a message says so and the graph opens at the top |
| `Shift-D` | Grafana hand-off for the workload, with the range from the start of the before window to the end of the after window ([grafana-handoff.md](grafana-handoff.md)) |
| `Up` / `Down` | Select a signal: on the calm page a row, with all detail any signal of the table |
| `s` | The rule, the sample counts and the exact queries of the selected signal, and the source, step, rate range and both windows of the comparison; press again to hide them |
| `a` | All detail: the table of every signal with before, after, change, percent and verdict, and the pane of the selected one. Press again for the calm page |
| `Esc` | Back |

## The page

The view is laid out the calm way ([presentation.md](presentation.md#calm-layout-the-standard-for-incident-views)):

```text
Impact  Deployment checkout-api · rev 2 (latest of 2)                                                  read 04:14 · 9 of 9 queries

  After rev 2 (03:58): error ratio 0.2% → 22.3%, p99 500ms → 4s, restarts 0 → 6, memory at limit (limit 512Mi → 256Mi), ready
  replicas 2 → 1.
  rev 2 changed: image v1 → v2; limits.memory 512Mi → 256Mi; env ~PAYMENT_TOKEN +FEATURE_FLAGS
  Order in time, not proof of cause.

     rev 2 rolled out 03:58:22Z · 15m each side                                          ▼
                                                                                         ┊
  WORSE AFTER   before → after                                                           ┊
> Error ratio   0.2% → 22.3%    +22.1pp  x112   ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁┊▅▅▅▅▆███████████████████████████████████
  Latency p50   45.5ms → 117ms  +71.3ms  +157%  ▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄┊▄▄▅▅▆███████████████████████████████████
  Latency p95   241ms → 1.98s   +1.74s  +723%   ▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂┊▅▅▆▆▇███████████████████████████████████
  Latency p99   500ms → 4s      +3.5s  +701%    ▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂┊▆▆▆▇▇███████████████████████████████████
  Restarts      0 → 6           +6  new         ·········································┊···▲···▲·····▲··▲···▲····▲··············
  Memory/limit  36.7% → 96.1%   +59.3pp  +162%  ▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄┊▆▆██▅▆▆██▅▅▆██▅▅▆██▃▅▆███▅▆███▅▆▆██▅▆▆██
  Ready (min)   2 → 1           -1  -50%        █████████████████████████████████████████┊█▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅
                                                                                         ┊
  HIGHER OR LOWER AFTER                                                                  ┊
  Memory peak   188Mi → 246Mi   +57.9Mi  +31%   ▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆┊▆▆██▅▆▆██▅▅▆██▅▅▆██▃▅▆███▅▆███▅▆▆██▅▆▆██
  CPU usage     60m → 344m      +284m  +474%    ▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂┊██████▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇
                                                ──────┬────────────┬────────────┬────────━───┬────────────┬────────────┬──────────
  UTC                                               03:45        03:50        03:55        04:00        04:05        04:10

  UNCHANGED  Request rate
  NOT AVAILABLE  CPU throttled

  next  Enter graph this workload    a all 11 signals    s query and rule    t timeline

  ↑↓ signal · 1-5 window · n/N later/earlier rollout · D Grafana · r re-read · Esc back
```

- **The answer** is [the summary line](#the-summary-line), bold, and what
  the revision changed on a line of its own directly under it. A warning
  that makes the page unsafe to read as it stands (the revision list was
  cut, the context changed) comes before both.
- **One row per signal that moved**: its name, before → after (`->` with `noIcons`), the change
  in the signal's unit and in percent, and a strip over both windows.
- **The rollout is one column through every row**, at the split of the two
  windows: `▼` over the strips, a guide `┊` down through every line to the
  axis, where it is heavy, and the band shading behind it. What is left of
  the guide is before the rollout and what is right of it is after. The
  guide stands in place of the one column whose step holds samples of both
  sides; a restart in that step keeps its `▲`, and a column that was not
  read keeps its `×`.
- **A strip is the value from zero**, so a signal that rose is low before
  the guide and high after it, and a signal that fell is high before and
  low after. A column is in the row's colour where its sample is further
  from the before value than the signal's rule allows, in the direction the
  signal moved, and dim where it is not. Restarts are counted, not
  levelled: each is a `▲` where the counter rose.
- **Groups**: `WORSE AFTER`, `BETTER AFTER`, `HIGHER OR LOWER AFTER` (the
  neutral signals) are rows; `UNCHANGED` is one dim line of names; `NOT
  READ` (with the reason), `NOT AVAILABLE` and `TOO FEW SAMPLES` are one
  line each and are never left out. A missing signal is never a row with a
  zero.
- The source, the step, the rate range, the exact windows and the rule,
  samples and queries of a signal are under `s`; the table is under `a`.

What a smaller screen gives up, in order: in an 80x24 terminal every signal
that is worse keeps its row and strip, the neutral signals fold to one line
(`HIGHER OR LOWER AFTER  Memory peak 188Mi → 254Mi · CPU usage 60m →
345m`) and the second key line goes; `a` is on the first. At 60 columns the
change column goes, then the last rows of `WORSE AFTER` are left out and
counted (`not shown (a shows all): 3 of 7 rows of WORSE AFTER`); the
one-line groups stay. The group the cursor is in opens again when the cursor
moves into it. With `s` open on a short screen the page keeps the row under
the cursor and the block states the rule, the samples and the queries only.

A query under `s` is there to be read and run again, so one that is wider
than the line is written on as many lines as it needs, broken after a comma
where it can be. Only where that would cost the page its strips or its
`next` line (60 columns in a 24-row terminal) is each query one line, cut
where the line ends and marked with `…`; `a` on a taller screen shows them
whole.

## What is read

Kubernetes, three bounded reads at most (three-second timeout each):

1. For a Pod, the Pod and, for a Deployment's Pod, its ReplicaSet.
2. The workload.
3. Its revisions: ReplicaSets with the workload's selector for a Deployment,
   ControllerRevisions for a StatefulSet or DaemonSet; at most 200, filtered
   to those the workload controls. When the server holds more than that, the
   header says in a warning line that only the first 200 were read and that a
   later rollout may be missing, because "latest" is then only the latest
   revision that was read.

Prometheus, nine range queries through the shared client (eight seconds and
1 MiB each, at most three in flight, no redirects, cancellable). Leaving the
view or asking again cancels the read in flight; a late answer is discarded.
Nothing is written anywhere.

## The rollout time and the windows

The rollout time is the creation time of the revision's ReplicaSet or
ControllerRevision. The title shows which revision is compared and how many
are retained; the marker row over the strips shows the exact time and the
width compared on each side.

- Both windows have the same width. The default is 30 minutes.
- The after window ends at the read time, or at the next rollout when a later
  one exists. The before window never reaches back past the previous rollout.
  When either limit makes the windows narrower than asked, `s` and the table
  of all detail say so and why, for example `12m each (asked 30m; the rollout
  was 12m before this read)`; the marker row says `(asked 30m)` where it has
  room.
- Less than one minute on a side is not compared.
- A revision created *before* the revision it replaced is a rollback onto an
  older ReplicaSet. Kubernetes does not record when a rollback happened, so no
  windows are placed around it; the view says so and `Shift-N` steps to an earlier
  rollout.
- Revisions pruned by the workload's history limit cannot be shown.

The title states the read time and how many of the nine queries were
answered. `s` states the source, the sample step (chosen by the shared client
so a query asks for at most 120 sample times) and the range every rate is
taken over: four steps, at least two minutes.

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

On the calm page the strip of a row is the value of every sample on one
scale from zero to the row's largest sample, over both windows. A sample
further from the before value than the rule allows, in the direction the
signal moved, is in the row's colour; every other sample is dim. A column
without a sample is `×`. In the table of all detail (`a`) the trend column
shows the before half, a rollout mark, and the after half on the same kind
of scale; a blank cell had no sample. With `noIcons` both use the ASCII ramp
`_.:-=+*#`; the rollout is `v` over the strips and `|` through the rows and
in the table.

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
for the selected signal under `s`. On the calm page the group label says the
verdict; in the table worse rows carry a `!` as well as the word. The
meaning does not depend on colour in either.

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

The page writes each `->` of it as `→`, as every calm page writes a change,
and as `->` again with `noIcons`; the table under `a` and the report keep
`->`.

It lists the signals that are worse by the rule above, in a fixed order, with
one latency figure (the highest percentile that moved). "memory at limit"
means the peak working set reached 90 % of the limit; when the revision also
changed the limit, both values follow in brackets, taken from the two pod
templates. When nothing is worse it says so, how many signals were compared
and how many were not, so a quiet sentence is never read as health when
signals are missing. When the request rate rose or fell by a factor of two or more it
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
- Prometheus retention bounds how far back `Shift-N` is useful.
- A 403 from the API server's service proxy and a 403 from Prometheus itself
  both read `denied`.
- `:graph` draws memory, CPU, network and restarts only, so `Enter` on an
  HTTP or readiness signal cannot show that signal as a chart. Rates use the
  same range rule in both views (four steps, at least two minutes).

## Synthetic fixture

`scripts/operator-study-fixture.py` declares `red.workloadLabel: service` for
its context, so the checkout-api story reads: request rate about 40/s on both
sides, error ratio about 0.2 % before and 22 % after, p99 0.5 s before and 4 s
after, six restarts, memory peak at 99 % of the new 256Mi limit, ready replicas
2 then 1, and CPU throttling `not available` (no CPU limit is set).

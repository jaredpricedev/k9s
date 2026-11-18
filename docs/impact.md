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
| `s` | The rule, the sample counts and the exact queries of the selected signal, the full diff of the revision, and the source, step, rate range and both windows of the comparison; press again to hide them |
| `a` | All detail: the table of every signal with before, after, change, percent and verdict, and the pane of the selected one. Press again for the calm page |
| `Esc` | Back |

## The page

The view is laid out the calm way ([presentation.md](presentation.md#calm-layout-the-standard-for-incident-views)):

```text
Impact  Deployment checkout-api · rev 2 (latest of 2)                                                  read 10:35 · 9 of 9 queries

  7 signals got worse after rev 2 rolled out at 10:22: errors, latency, restarts, memory and ready replicas.
  Rev 2 changed the image, the memory limit (512Mi → 256Mi) and 2 env vars.                     Order in time, not proof of cause.

     rev 2 rolled out 10:22:54Z · 12m each side                                          ▼
                                                                                         ┊
  WORSE AFTER   before → after                                                           ┊
> Error ratio     0.2% → 22.3%  +22.1pp  x112   ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁┊▃▄▅▅▅▅▆█████████████████████████████████
  Latency p50   45.5ms → 116ms  +70.7ms  +155%  ▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄┊▄▄▄▅▅▅▅█████████████████████████████████
  Latency p95    241ms → 1.98s  +1.74s   +723%  ▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂┊▄▄▅▆▆▆▇█████████████████████████████████
  Latency p99    500ms → 4s     +3.5s    +700%  ▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂┊▅▅▆▆▆▇▇█████████████████████████████████
  Restarts           0 → 6      +6       new    ·········································┊·····▲·····▲·····▲····▲·····▲·····▲·····
  Memory/limit   36.7% → 99.2%  +62.5pp  +170%  ▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▃▃▃▃▃▃▃▃▃▃▃┊▅▆▇▇██▄▅▆▇██▄▄▅▇▇██▄▄▆▇▇██▄▅▆▇▇█▄▄▅▆▇██▃
  Ready (min)        2 → 1      -1       -50%   █████████████████████████████████████████┊██▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅
                                                                                         ┊
  HIGHER OR LOWER AFTER                                                                  ┊
  Memory peak    188Mi → 254Mi  +66Mi    +35%   ▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆▆┊▆▆▇▇██▄▅▆▇██▄▄▅▇▇██▄▄▆▇▇██▄▅▆▇▇█▄▄▅▆▇██▃
  CPU usage        60m → 344m   +284m    +474%  ▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂┊████████▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇
                                                ──────────────┬────────────────┬─────────━──────┬───────────────┬────────────────┬
  UTC                                                       10:15            10:20            10:25           10:30            10:35

  UNCHANGED  Request rate
  NOT AVAILABLE  CPU throttled

  next  Enter graph the workload    a all 11 signals    s query and rule    t timeline

  ↑↓ signal · 1-5 window · n/N later/earlier rollout · D Grafana · r re-read · Esc back
```

- **The answer** is two sentences, as a person would say them
  ([the answer](#the-answer)): how many signals got worse after the rollout
  and of which kinds, bold, then what the revision changed. It repeats no
  before or after value: the rows directly under it carry every one. The
  caveat is dim, at the right of the last line when it fits. A warning that makes the
  page unsafe to read as it stands (the revision list was cut, the context
  changed) comes before both.
- **One row per signal that moved**: its name, before → after (`->` with
  `noIcons`), the change in the signal's unit and its ratio, and a strip
  over both windows. One kind of number is read down one column: the arrow
  stands in one column on every row of the page, under the arrow of
  `before → after` over it, and the change and the ratio each start in one.
  Where before and after are too wide to be set in two columns they are
  written as one text and nothing is cut for the sake of the column.
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
- The source, the step, the rate range, the exact windows, the full diff
  of the revision and the rule, samples and queries of a signal are under
  `s`; the table, with the sentence that lists the values
  ([the summary line](#the-summary-line)), is under `a`.
- **The keys**: the `next` line names what `Enter` graphs for the row under
  the cursor (`graph restarts`, `graph memory/limit`, `graph memory peak`,
  `graph CPU`; `graph the workload` for a signal `:graph` has no chart
  of), then `a`, `s` and `t`. They are on that line because a short page
  gives the second key line up. A line too narrow for all four says
  `Enter graph` without what, which the marked row says, so that `a` and
  `s` are named at 60 columns too; a key the line still has no room for is
  named on the second line where the page has one (`a show every signal`,
  `s queries`, `t timeline`), in the place every calm page has for it, and
  there the keys of the rollouts are `n/N rollout` when the longer words do
  not fit.

### In an 80x24 terminal

```text
Impact  Deployment checkout-api · rev 2 (latest of 2)                10:35 · 9/9
  7 signals got worse after rev 2 rolled out at 10:22.
  Rev 2 changed the image, the memory limit (512Mi → 256Mi) and 2 env vars.
  Order in time, not proof of cause.

     rev 2 rolled out 10:22:54Z · 12m each side                 ▼
  WORSE AFTER   before → after                                  ┊
> Error ratio     0.2% → 22.3%  +22.1pp  x112   ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁┊▅▅█████████████
  Latency p50   45.5ms → 116ms  +70.7ms  +155%  ▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄┊▅▅█████████████
  Latency p95    241ms → 1.98s  +1.74s   +723%  ▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂┊▆▆█████████████
  Latency p99    500ms → 4s     +3.5s    +700%  ▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂┊▆▇█████████████
  Restarts           0 → 6      +6       new    ················┊·▲·▲··▲·▲·▲·▲··
  Memory/limit   36.7% → 99.2%  +62.5pp  +170%  ▄▄▄▄▄▄▄▄▄▄▄▄▃▃▃▃┊▇█▆█▅▇█▆██▇█▅▇█
  Ready (min)        2 → 1      -1       -50%   ████████████████┊█▅▅▅▅▅▅▅▅▅▅▅▅▅▅
                                                ─────┬──────┬───━─┬──────┬─────┬
  UTC                                              10:15  10:20 10:25  10:30
  HIGHER OR LOWER AFTER  Memory peak 188Mi → 254Mi · CPU usage 60m → 344m
  UNCHANGED  Request rate
  NOT AVAILABLE  CPU throttled
  next  Enter graph the workload  a all 11 signals  s query and rule  t timeline
```

This is the page in the 20 rows the application leaves the view when it
draws its line of crumbs; without that line the view has 21 and one more
blank row stands over `next`.

What a smaller screen gives up, in order:

1. **The neutral rows fold first.** `HIGHER OR LOWER AFTER` becomes one
   line that keeps both values of every signal (`HIGHER OR LOWER AFTER
   Memory peak 188Mi → 254Mi · CPU usage 60m → 344m`) before the page gives
   any blank row up: the answer does not rest on those rows, and the rows
   that are worse read better set apart than beside two more. The group
   opens again when the cursor moves into it: its rows are what the keys
   then act on. In 20 rows the page pays for that with the blank row under
   the answer and the last two rows of `WORSE AFTER`, which it counts
   (`not shown at this size (a shows all): 2 of 7 rows of WORSE AFTER`),
   and takes them back when the cursor leaves the group; in 21 rows it
   pays with its blank rows alone. No column moves either way: the names
   are one column as wide as the widest of all rows, so the arrows, the
   strips, the rollout guide and the axis stand where they stood whichever
   rows are drawn.
2. Then the page follows [the order every calm page
   follows](presentation.md#what-a-smaller-screen-gives-up): the blank rows
   around the answer and the keys, the blank rows before the groups, the
   second key line (`a` is on the first), and last the blank rows that set
   the answer and the keys apart. With one row to spare the page keeps the
   one under the answer and gives up the one over `next`.
3. In an 80x24 terminal every signal that is worse keeps its row and its
   strip, and nothing is left out, while the cursor is on one of those
   rows. At 60 columns the change column goes as well, and `s` or `a` has
   it; the folded line of the neutral signals then has room for the first
   of them and counts the others (`Memory peak 188Mi → 254Mi +1 more`),
   and the `next` line names `Enter graph`, `a` and `s`.
4. A screen shorter than that leaves the last rows of `WORSE AFTER` out and
   counts them (`not shown (a shows all): 2 of 7 rows of WORSE AFTER`); the
   one-line groups stay, and the line of what was not read is never left
   out.

With `s` open on a short screen the page keeps the row under the cursor, and
the block states the rule, the samples, the queries and the diff; the source
and the windows are added from 28 rows.

Every line under `s` is there to be read to its end, and a query to be run
again, so a line that is wider than the screen is written on as many lines
as it needs: a query broken after a comma where it can be, a sentence
between its words, each under the text and not under its label. Where the
page has no rows for all of it without losing its strips or its `next`
line, it gives the lines up in this order, and marks every cut with `…`:

1. The blank row beside the block.
2. The rule, the samples, and on a tall screen what a strip draws, the
   source and the windows are one line each.
3. The diff becomes one line. `s` is the key of the query and the rule, so
   the queries stay whole; the answer still says what kinds of thing the
   revision changed.
4. Where whole queries do not fit either, each query is one line and the
   diff is whole again if it then fits.
5. One line of each.

The rows of the page give way before the block does: with `s` open a short
page keeps the row under the cursor and counts the others (`not shown (a
shows all): 6 of 7 rows of WORSE AFTER, …`).

In the 20 rows of an 80x24 or a 60x24 terminal with the line of crumbs the
two queries of the error ratio are whole and the diff of the example is one
line, cut after `~PAYMENT_TOKEN` at 80 columns and after `env` at 60; in 21
rows the diff is whole too, and from 23 rows so are the rule and the
samples. A signal whose one query takes three lines (memory peak at 80
columns) has one line of it in 20 rows, and the diff is then whole. A longer
diff or longer queries need more rows in the same order. A taller or wider
terminal shows what a cut left out; no key does at that size, because the
table under `a` has the diff on one line of its header and its pane of the
selected signal on one line too.

## The answer

```text
  7 signals got worse after rev 2 rolled out at 10:22: errors, latency, restarts, memory and ready replicas.
  Rev 2 changed the image, the memory limit (512Mi → 256Mi) and 2 env vars.                     Order in time, not proof of cause.
```

The first sentence counts the verdicts of the comparison and names their
kinds: `errors`, `latency` (the three percentiles are one kind), `restarts`,
`memory` (the share of the limit in use), `CPU throttling`, `ready
replicas`. It adds no rule of its own and names no cause: "after" is order
in time. The only numbers in it are the count, the revision and the time of
the rollout (with the day when the read was on another one: `rolled out on
Nov17 at 04:20`).

| The comparison | The first sentence |
| --- | --- |
| Signals of several kinds are worse | `7 signals got worse after rev 2 rolled out at 04:20: errors, latency, restarts, memory and ready replicas.` |
| Signals of one kind are worse | `Latency got worse after rev 2 rolled out at 04:20: p50, p95 and p99.` One percentile alone is named as its row is (`Latency p99 got worse after …`): one percentile is not "latency" |
| None is worse | `No compared signal got worse after rev 2 rolled out at 04:20: 3 better, 2 higher or lower, 5 unchanged, 1 not compared.` Every signal is counted, so a quiet sentence is never read as health when signals are missing; signals that are only higher or lower are counted as that, never as worse or better |
| Nothing could be compared | `No signal could be compared around rev 2, rolled out at 04:20: 2 not available, 9 with too few samples before or after.` |
| Nothing was read | `Nothing was read around rev 2, rolled out at 04:20: denied (HTTP 403).` in the warning colour, with the caveat `Not read is not zero and not healthy.` |
| No rollout time (a rollback), no controller | `No comparison: …` with the reason, and the caveat `No comparison is not a sign that nothing changed.` |

The second sentence says what kinds of thing the revision changed and how
many of each: `the image` or `2 images`, `the memory limit` with its two
values, `the CPU limit` and the other resource settings, `2 env vars`, `the
envFrom sources`, `the command`, `the containers (1 added)`. It is counted
from [the list of changes](#what-changed), so it never says less than the
list; an entry of a kind it has no word for is counted as `1 other setting`.
The list itself, with the image tags and the names of the environment
variables, is under `s`.

A request rate that rose or fell by a factor of two or more limits what the
comparison says, and is a sentence of its own between the two: `Request rate
40/s → 120/s (+200%): error ratio and latency are not like-for-like.`

How long the answer is:

- **Each sentence is one line** wherever a form of it fits one, at any
  width. The fullest form that fits is written. The first sentence leaves
  the kinds out when they do not fit beside it (`7 signals got worse after
  rev 2 rolled out at 10:22.`): the rows name them. The second leaves the
  two values of the memory limit out: `s` has them. The sentence about the
  request rate keeps the size of the change and leaves the two rates out
  (`Request rate +200%: errors and latency are not like-for-like.`): its
  row or its folded line has them. What was not compared, and why nothing
  was read, is never left out; a sentence that fits no line in any form is
  wrapped whole.
- **The caveat** is at the right of the last line when it fits there, and a
  line of its own when not.
- **132 columns: two lines**, three with the sentence about the request
  rate. **80 columns: three lines**, the caveat being the third. **60
  columns: three lines**, without the kinds and without the two values of
  the memory limit. The sentence about the request rate is one line more
  at 80 columns and two at 60. A warning in front (the revision list was
  cut, the context changed) is written whole at any width: one line more
  at 132 columns, two at 80 and at 60. So the answer is at most three lines
  at 132 columns unless a warning stands in front of it.

The page writes each `->` as `→`, as every calm page writes a change, and as
`->` again with `noIcons`.

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

The revision's pod template is compared with the one it replaced: image
tags, resource requests and limits, and environment variable *names* (`+`
added, `-` removed, `~` changed). Values and command lines are never read
into the view. This is the same comparison `:timeline` shows for a rollout
entry. The earliest retained revision has nothing to compare with, and the
page says so.

The answer says it in a sentence (`Rev 2 changed the image, the memory limit
(512Mi → 256Mi) and 2 env vars.`). The list itself is under `s`, written
whole on as many lines as it needs wherever the page has the rows for it
beside the queries ([in an 80x24 terminal](#in-an-80x24-terminal) it is the
first of the two to be cut, and the cut is marked):

```text
  changed  image v1 → v2; limits.memory 512Mi → 256Mi; env ~PAYMENT_TOKEN +FEATURE_FLAGS
```

The table under `a` has the same list on one line of its header, cut where
that line ends, as it had before.

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

This is the sentence that lists the values. It was the answer of the calm
page until the rows carried every one of them; it is in the header of the
table under `a`, with `->`, and the calm page has [the answer](#the-answer)
instead.

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

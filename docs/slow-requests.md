# Slow requests: where on the request path did it hang

`:slow` (also `:requests`) answers one question for a Service: **when a page
sometimes just waits, which hop on the request path was raised, is one Pod
the odd one, and when did it happen?** It is for the failure that status
views do not show: nothing crash-loops, nothing is refused, clients give up.

It reads the configured path once from the Kubernetes API, then one bounded
batch from the declared Prometheus source
([observability-sources.md](observability-sources.md)), using the
request-path declaration and profiles in [request-path.md](request-path.md).
It reads only when you open it, change the window or press `r`. It changes
nothing in the cluster and sends no probe.

It reports what overlaps in time. It says "overlaps", "before" and "outside",
never "because". Two things that are raised in the same minutes overlap;
that is order in time, not a cause.

## Opening it

Select a Service, an Ingress, a Deployment, a StatefulSet, a DaemonSet or a
Pod and press `Shift-W`, type `:slow` or `:requests`, or pick **Slow
requests: which hop** in `Ctrl-O`. A request path belongs to a
Service, so the selection is resolved to one:

| Selection | Service that is read |
| --- | --- |
| Service | that Service |
| Ingress | the first backend Service that has a `requestPath.services` entry, else the first backend; the others are named |
| Deployment, StatefulSet, DaemonSet | the Service in the namespace whose selector matches the Pod template labels (one list of at most 200 Services); with several, the first with a declaration, else the first by name, and the others are named |
| Pod | the same, by the Pod's own labels; the view opens on that Pod's row |

A workload or Pod that no Service selects, and an Ingress without a backend
Service, are refused with that reason. The palette entry is unavailable, with
the reason, for other kinds and when the context declares no Prometheus
source. The typed command still opens the view and shows every hop as not
read.

## Keys

| Key | Action |
| --- | --- |
| `Up` / `Down` (`k` / `j`) | Select a hop, or a Pod of the app hop |
| `Enter` | On the app hop: list or fold its Pods. On a Pod: open `:graph` for it. On any other hop: its queries, samples and rules |
| `s` | Queries, samples and rules of the selected hop in place of the lanes; `PgDn` / `PgUp` page; `s` again returns |
| `Left` / `Right` | Show the cursor on the lanes and move it one column; the title states its time and each lane shows its value there |
| `n` / `Shift-N` | Move the cursor to the next / previous episode |
| `1` `2` `3` `4` | Window: 15m, 1h (default), 6h, 24h. The batch is sent again; the path is not read again |
| `r` | Read the path and the batch again |
| `p` | Probe the Pods now, through the `:probe` command when this build has it; otherwise a message says it does not |
| `h` | The existing native Hubble view for the backend Pods ([hubble.md](hubble.md)) |
| `m` | `:graph` for the selected Pod, or the workload behind the Service |
| `Shift-D` | Grafana hand-off for the selected Pod or the workload, with the window that was read ([grafana-handoff.md](grafana-handoff.md)) |
| `t` | `:timeline` for the selected Pod or the workload |
| `Esc` | Put the cursor away; then back |

`p`, `m` and `t` are looked up in the action registry behind `Ctrl-O` when
the key is pressed, so a command this build does not have, or one that is
unavailable for the selection, answers with the reason instead of doing
nothing. The `next:` line lists only the steps that would run.

## What is read

Kubernetes, once per open and per `r` (three-second timeout per read unless
stated):

1. The selected Ingress, workload or Pod, and one list of Services, only
   when the selection is not a Service.
2. The configured path, with the collector of `:network-review`
   ([network-path-review.md](network-path-review.md)): the Service (its UID
   is checked), its Pods, its EndpointSlices, the Ingresses and HTTPRoutes of
   the namespace and its NetworkPolicies, within that collector's limits.
3. For each declared dependency, one list of its EndpointSlices, to name its
   Pods (at most 16 per dependency).
4. The workload that controls the Pods and its retained revisions, as
   `:impact` reads them, for the rollouts lane.

No Secret and no ConfigMap is read. Nothing is written.

Prometheus, one batch per read:

- one range query per signal of every hop, at most 48 per batch, four in
  flight at a time, the whole batch within 45 seconds;
- each query is one GET with the limits of the declared source's client
  (8 seconds, 1 MiB, 32 series, 4096 samples, 120 sample times);
- the step is the one `:graph` uses for the window: 15s for 15m, 30s for 1h,
  5m for 6h, 15m for 24h; a rate is taken over four steps and at least two
  minutes;
- the window ends on a whole step, and on a whole minute when it is longer
  than 15m, so that a lane column and the cursor begin and end at the
  minutes they are labelled with.

Alertmanager, one read, only when the context declares one: the alerts whose
`namespace` label is the Service's namespace.

## The hops

One row per hop, in the order a request meets them.

| Row | Read from | Signals |
| --- | --- | --- |
| client → ingress | the ingress profiles (default `ingress-nginx`), pinned to the Ingress and the Service | request rate, HTTP 499 rate (the client closed the request while waiting) and its share of all requests, 5xx share, p99 request duration, p99 upstream response duration |
| ingress → service | the Kubernetes API, plus `kube_pod_status_ready` when kube-state-metrics is scraped | endpoints ready now; Pods that were created or changed Ready inside the window; when a Pod was not Ready |
| service → pods | the app profiles (default `red`, `container-cpu`), per Pod, plus `kube_pod_container_status_restarts_total` and `container_memory_working_set_bytes` over `kube_pod_container_resource_limits` | p99 latency, Puma queue and free threads with the `gitlab-webservice` profile, CPU throttled share, restarts, memory against the limit |
| pods → *dependency* | the profile of each dependency declared under `requestPath.services.<namespace/name>.dependencies` | that profile's signals (`postgres-exporter`, `redis-exporter`, `grpc-server`, or your own) |
| dns | the DNS profiles (default `coredns`, cluster-wide) and the `dns-errors` signal of the network profile | p99 lookup time, SERVFAIL and NXDOMAIN share, share of DNS errors answered to these Pods |
| network | the network profiles (default `cilium-hubble`), per destination Pod | drops by reason, TCP resets per new connection, flows |

Columns: `HOP`, `WHAT` (the host, the Service or the profile), `LAST 1h` (a
thin trend of the hop's main signal, scaled so that the usual level sits
low and only a burst reaches the top), `WORST` (the key number: the worst
sample of the main signal in the window), `STATE` and `EVIDENCE`. Evidence
that does not fit loses whole items from the end and says so with an
ellipsis; a number is never cut.

Dependencies are never guessed: without a declaration the row reads
`not read` with the reason. When the screen is short and no dependency is
raised, they share one row.

## Status words

There are four, and none of them means healthy.

| Word | Meaning |
| --- | --- |
| `quiet` | every signal that was read stayed under its rule in the window |
| `elevated` | at least one signal crossed its rule |
| `OUTLIER` | one Pod differs from its peers (app hop only; written in capitals so that it stands out without color) |
| `not read` | nothing was read for the hop, with the reason: no mapping, no series matched, the source refused or timed out, no Pod or Ingress is known |

`quiet` is not health. A rule compares a signal with its own usual level in
this window, so a hop can be quiet and slow all day if it is equally slow
all day and under the level that always counts.

### The rule of a signal

A sample is **raised** when it crosses the rule of its signal. The usual
level is the median of the same series over the window.

| Signal | Raised when the sample is | Always raised at |
| --- | --- | --- |
| a duration (`seconds`: p99, latency) | at least 3 times the usual level and at least 50ms above it | 5s |
| a share (`ratio`: 5xx, errors, SERVFAIL, resets per connection) | at least 3 times the usual level and at least 1 point above it | 5% |
| `throttled` (share of CPU periods) | at least 3 times the usual level and at least 25 points above it | 50% |
| `memory` (working set over limit) | - | 90% |
| a rate (`perSecond`: 499, drops) | at least 3 times the usual level and at least 0.1/s above it | - |
| a count (queued connections, clients) | at least 3 times the usual level and at least 1 above it | - |
| a signal that is bad when low (free threads, `up`, Ready) | a third of the usual level or less and at least 1 below it | 0 |
| `restarts` | larger than the sample before | - |
| a signal with `worse: none` (request rate, cores, threads configured) | never: it is shown for context | - |

The floors exist so that noise around a tiny level stays quiet: a hop that
adds less than 50ms cannot explain a page that waits. The rule of every
signal is printed beside its numbers under `s`.

Each raised sample stands for the step that ends at it: a query evaluated
at a time looks back from it (a gauge at its last scrape, a counter since
the sample before). A stretch of a gauge is therefore as long as its raised
samples are many, the newest sample of the window is on the strip, and a
sentence states a stretch by the sample that ends its first step.

A rate, a share of two rates and a quantile of a rate look back further:
over the rate window (2m at 1h). A burst that has ended is still in the
rate until it has left that window, so the samples of a three-minute stall
are raised for about five minutes. Drawn as they are, give-ups at the
ingress would seem to go on for two minutes after the queue on the Pod had
cleared. The stretch of such a signal is therefore stated without that
time:

- While a burst enters the window the rate climbs in proportion to the
  part of it that is inside, and it falls the same way while the burst
  leaves: two ramps as long as the window *W*, or as long as the burst
  when the burst is shorter and never fills the window.
- The first and the last raised sample each hold a part of the peak, which
  is how far up its ramp each sits. The first then says how long the burst
  had been going on, the last how much of it was still in the window. That
  is read with ramps as long as the window and as long as the burst; the
  wrong reading is always the longer one, so the shorter is taken.
- When those two samples do not sit on such ramps (the series jumps to its
  peak in one step), or together hold more than 0.8 of them, the stretch is
  solved from the number of raised samples instead. The rule is crossed
  once a part *f* of a ramp is climbed (the distance from the usual level
  to the rule, over the distance to the peak) and until less than *f* is
  left, so the raised samples of a burst of length *D* span *D + (1 - 2f)W*
  when it is longer than the window and *W + (1 - 2f)D* when it is shorter.
  At most 0.4 of a ramp is taken off at each end, and a burst shorter than
  the window is solved this way only up to *f* = 0.3.
- A quantile does not climb in proportion. It is taken to be slow while any
  part of its window is, and is always solved from the number of samples.
- The stretch is stated to a whole number of steps, at least one, and from
  the step in which a gauge of the same burst would begin.
- A stretch that reaches the first or the last sample keeps that end: the
  burst may have begun before the window, or may not be over.
- A gauge, a counter, a signal that is bad when low and a rate window no
  longer than one step are left as they are.

For a burst longer than the rate window this gives the stretch a gauge of
the same burst would have, wherever the samples fall. A burst no longer
than the window never fills it, so its peak does not say how large it was;
its stretch is right to within one step at either end. The samples
themselves are not changed: the trend, the worst number and the value at
the cursor are what the source answered, and the detail of a signal (`s`)
says when its stretches were read from a rate.

A signal whose extra matcher matched no series (a counter has no
`status="499"` series before the first 499) is read as zero when the same
metric without the matcher was read in the same profile, and says so. Without
that total it stays `not read`.

### One Pod against its peers

On the app hop every signal with a bad direction is compared across the Pods
with `chart.Outliers` (the median and the median absolute deviation; see
[request-path.md](request-path.md)). A Pod differs when:

- its worst sample deviates from its peers' worst samples (modified z-score
  of at least 3.5, at least twice their median, further than the signal's
  floor) **and** that Pod crossed the signal's own rule; or
- its usual level deviates from its peers' usual levels in the same way, in
  which case the row says `usually`.

Fewer than three Pods with samples are not compared, and the row says so.
The row then reads `1 of 3 pods: …-hx4mt p99 48s vs peers 350ms`, and
`Enter` lists the Pods, each with its own trend on one shared scale.

## When it happened

The bottom half is a strip of lanes on one time axis. A filled cell means
that the lane's rule was crossed in that column; a dot that it was not; `×`
(`x` without icons) that there is no data, which is never drawn as quiet.

- One lane per hop, labelled with the signals that were raised and, when all
  marks belong to one Pod, that Pod.
- The app hop has one lane per raised signal (at most four) when the height
  allows, because slow answers, a queue and CPU throttling say different
  things; otherwise one lane.
- Dependencies without a mark share one lane.
- `restarts` (the restart counter, and the last restart the API reports),
  `rollouts, new pods` (creation times of the workload's retained revisions
  and of Pods created inside the window) and, with a declared Alertmanager,
  `alerts firing`.
- Without kube-state-metrics the endpoints lane marks only the last Ready
  change the API still knows, and the time before it is unknown.

One column is a round stretch of time (1m for 1h at 132 columns, 2m at 80
and 60), stated in the title or in the axis gutter. The edges of a stretch
are moved to the nearest column edge, so that the cells a lane fills add up
to the time written beside it; a stretch shorter than half a column keeps
one cell. To the right of a lane is the time it was over its rule, or, with
the cursor, its worst value in the cursor's column. When the lanes do not
all fit, the marked ones stay and one line names the rest. When not even
the marked ones fit, the lane the episodes are counted from stays, then the
lanes that overlap every episode, then those that overlap some, and the
line counts the marked lanes that are left out. The lanes never take a row
from the summary, which keeps whole sentences.

## What lines up

An **episode** is a stretch of the first raised signal on the path (the
ingress's 499 when it is raised), with stretches closer than two steps
joined. The headline counts the episodes and states when each began and how
long they lasted. A lane that folds several signals of a hop marks every
stretch in which any of them was raised, but the episodes are those of the
one signal the headline names: a stretch in which the ingress was slow and
no client gave up is on the lane and is not counted as a give-up.

The summary is computed from interval overlap only:

- which lanes overlap every episode, grouped by where ("slow p99, queueing
  and CPU throttling on pod …-hx4mt");
- which overlap some, with the episodes named ("2 of 3 (07:28, 07:47)
  overlap not Ready on pod …-hx4mt");
- which overlap none;
- what was raised **outside** every episode, with its time, so that an
  unrelated burst is not mistaken for part of the hang; a dated fact shortly
  before an episode (within 15 minutes) is said to be before it;
- what was not read;
- the 499 at its worst as a share of all requests, and whether 5xx rose
  with it: give-ups without 5xx are requests that waited, not requests
  refused;
- "Overlap is order in time, not cause."

When no signal crossed its rule, the summary says the window was quiet for
the hops that were read, lists the hops that were not read, and repeats that
quiet is not healthy. When the screen is small, whole sentences are left out
from the least important; what overlaps every episode and the caveat stay.

## Sizes and presentation

- 132x36: every hop and the Pods of an outlying app hop, the title, one lane
  per raised signal, the summary and the next steps.
- 80x24: the hop table without the `WHAT` column and with quiet dependencies
  on one row, the lanes with marks, and the summary.
- 60 columns: hop, key number and state word; the trend column is left out
  first. A lane label that does not fit keeps what its marks mean and the
  Pod (`not ready …-hx4mt`, `p99 +3 …-hx4mt`) before the name of the hop.
- The page of queries and rules (`s`) wraps at every width: no number, rule
  or query is cut.
- With `noIcons` every character is ASCII except the `·` separator: `->`
  for the arrow, `#` for a mark, `x` for no data, `...` for an elision.
- State is always a word. Color repeats it and is taken from the semantic
  palette.

## Limits

- The path is the Service's namespace: an Ingress in another namespace and a
  Gateway API route are not read as an ingress hop, and say so.
- With several Ingresses the ingress hop is read for all of them together.
- A profile's label names are assumptions about a common scrape setup; a
  signal that matches nothing is `not read`, not zero. `:sources` shows the
  declaration; [request-path.md](request-path.md) shows how to change a
  profile.
- More than 32 Pods give an answer that is cut by the series limit, and Pod
  names beyond about 1000 bytes of matcher are left out of the query; the
  signal's detail (`s`) says so and counts them.
- At 6h and 24h a sample stands for 5 or 15 minutes and a rate for 20
  minutes or an hour: a stall of a few minutes merges with the next one or
  disappears. The summary says so; use 1h or 15m around the time of
  interest.
- The ingress-nginx duration histogram ends at 10s by default, so a longer
  wait reads as 10s.
- The stretch of a rate is an estimate. A burst no longer than the rate
  window is right to within one step at either end. A quantile whose slow
  requests are a thin share of the window's traffic (a stalled Pod answers
  few requests) crosses its rule later and leaves it sooner than assumed,
  and can read a step or two short. A share of two rates is read as a rate
  although it does not climb exactly in proportion. A source that answers
  something other than a mean over the window for these queries (a
  recording rule with another range) is read by the number of its raised
  samples, less the window.
- The CoreDNS numbers are cluster-wide: they describe what CoreDNS answered
  to everyone. Only the Hubble `dns-errors` signal is about these Pods.
- The rules are fixed and cannot be configured. They are stated on screen so
  that a status word can be checked against its numbers.
- An episode is counted from the first raised signal on the path. When the
  ingress is not read, that is the next hop that was raised.
- The Kubernetes API keeps only the last Ready change and the last restart
  of a container; history comes from kube-state-metrics.
- Nothing here proves a cause, and a quiet window is not proof of health.

## Trying it

`scripts/operator-study-fixture.py` serves the "GitLab intermittent hang"
scenario described in [request-path.md](request-path.md). Open the Service
list of namespace `gitlab`, select `gitlab-webservice-default` and press
`Shift-W`: three episodes of client give-ups at the ingress, one Pod of
three as the outlier on p99, queue and CPU throttling, quiet dependencies
and DNS, and one burst of dropped packets towards another Pod that the
summary places outside every episode. `--deny-source prometheus` shows the
not-read state. The fixture does not list Ingresses in its table view, so
the Ingress row is exercised by tests only.

Code: `internal/slowpath` resolves the path (`Resolve`), plans the batch
(`BuildPlan`), sends it (`Collect`) and evaluates it (`Evaluate`: rules,
peers, lanes, episodes, summary); `internal/view/slow.go` and
`slow_render.go` are the view. `internal/slowpath/slowtest` is the synthetic
story the tests use.

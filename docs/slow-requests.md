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

It reports what overlaps in time. It says "lines up with", "overlaps",
"before" and "outside", never "because". Two things that are raised in the
same minutes overlap; that is order in time, not a cause.

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

## The page

The view opens on one page, laid out the calm way
([presentation.md](presentation.md#calm-layout-the-standard-for-incident-views)):
the answer first, then every lane of the read as one row with its time strip
on one shared axis. This is the hang of the test story in the area a 132x36
terminal gives the view (`internal/view/testdata/slow/hang-132x32.txt`):

```text
Slow requests  Service gitlab/gitlab-webservice-default · last 1h                                    read 08:20 · 35 of 35 queries

  3 stalls: clients gave up waiting at 07:28, 07:47 and 08:13 UTC, for 2 to 4 minutes each.
  All 3 line up with slow p99, queueing, no free threads and CPU throttling on pod …-hx4mt.
  2 of 3 also line up with not Ready on pod …-hx4mt.                                          Overlap in time, not proof of cause.

                                                            stalls        ▼▼▼▼▼               ▼▼▼▼▼▼                      ▼▼▼

  LINES UP WITH THE STALLS
  ingress      client give-ups (HTTP 499) +2  6.29%   of requests  ▁▁▁▁▁▁▁█████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁██████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁▁▁

> pod …-hx4mt  slow p99                       48s     peers 350ms  ▁▁▁▁▁▁▁█████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁██████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁▁▁
               queueing                       30      peers 0      ▁▁▁▁▁▁▁█████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁██████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁▁▁
               no free threads                0       peers 5      ▁▁▁▁▁▁▁█████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁██████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁▁▁
               CPU throttling                 86%     peers 1.2%   ▁▁▁▁▁▁▁█████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁██████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁▁▁
               not Ready                      twice   up to 3m     ▁▁▁▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁

  DIFFERS, BUT NOT DURING A STALL
  network      packet drops (POLICY_DENIED)   0.37/s  to …-2bq7k   ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁██▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁

  QUIET
  pods …-2bq7k, …-8wzpd  p99 350ms                                 ·······························································
  3 dependencies         no rule crossed                           ·······························································
  dns                    p99 6ms                                   ·······························································
  restarts · rollouts    none in the last 1h                       ·······························································
                                                                   ┬──────━━━┯━─────────┬─────━━━━┯━─────────┬─────────┬──━━━────┬
  UTC                                                            07:20     07:30      07:40     07:50      08:00     08:10     08:20

  next  Shift-B probe the pods now    m compare the pods    Enter open …-hx4mt    Shift-D open in Grafana

  ↑↓ row · a show every signal · s queries · ←→ time · 1-4 window · n next stall · t timeline · h Hubble · r re-read · Esc back
```

Behind the columns under each `▼` runs a band, from the marker row down to
the row above the axis: what is over its rule inside a band lines up with a
stall, and what is over its rule between two bands does not.

### The answer

The answer is computed from the episodes and from interval overlap
(`slowpath.Report.Answer`); no sentence says "because".

- The first sentence, in bold, states the episodes: how many, when each
  began (UTC) and how long they lasted. They are called **stalls** when the
  signal they are counted from is the ingress's HTTP 499, which is a client
  that closed the request while it waited, and **episodes** for any other
  signal (`2 episodes of not Ready on pod …-hx4mt at 07:29 and 07:48 UTC`).
- The second states what lines up with every one of them, grouped by where
  (`All 3 line up with slow p99, queueing, no free threads and CPU throttling
  on pod …-hx4mt.`).
- Then what lines up with some (`2 of 3 also line up with not Ready on pod
  …-hx4mt.`); lanes that overlap different episodes are never joined in one
  count. When nothing else does: `Nothing else lines up.`, with `that was
  read` when a hop was not read.
- A Pod whose usual level differs from its peers' is stated whether or not
  there is an episode (`Pod …-hx4mt usually differs from its peers: p99 2s
  (peers 305ms).`).
- At 6h and 24h the answer ends with what such a window cannot show.
- The caveat (`Overlap in time, not proof of cause.`) is dim, at the right
  of the last line when it fits there.
- Without an episode: `No signal that was read crossed its rule in the last
  1h.`, then how many of the queries were answered, and `Nothing found is
  not proof of health.`
- When Prometheus answered nothing: `Prometheus was not read (denied: HTTP
  403), so no hop has latency evidence.` in the warning colour, and `Not
  read is not healthy.`

When the first sentence does not fit on one line, the times are left to the
marker row and the axis. When the second does not, it counts the signals the
rows under it name (`All 3 line up with 4 signals on pod …-hx4mt.`). A screen
without room for strips keeps the times in the sentence.

A read that failed after an earlier one succeeded, and a destination that
changed under the view, are one line in the warning colour under the title;
the earlier read stays on the page. While a read runs the title says
`reading…`, and the page stays as it is.

### The rows

One row per lane. The lanes are those of [When it happened](#when-it-happened):
one per hop, one per raised signal for the Pods of the Service, and the dated
facts. A row is the subject, what its marks mean, the one number that matters,
what that number is held against, and the strip. The subject is the hop, or
`pod …-hx4mt` when every mark of the lane belongs to one Pod of the Service;
it is written once and its rows stand beneath it.

| Row | Number | Held against |
| --- | --- | --- |
| a raised signal of a hop | its worst sample; for a rate that is part of a total, its share of that total at worst (`6.29%`) | `of requests`; the Pod its worst series is about (`to …-2bq7k`); else its own usual level (`usual 4%`) |
| a raised signal of one Pod | that Pod's worst sample | `peers 350ms` when the Pod differs from its peers on it, else its own usual level |
| a raised signal of several Pods | the worst sample | how many of the Pods crossed it (`2 of 3 pods`) |
| not Ready | how often (`twice`) | the longest stretch (`up to 3m`) |
| dated facts: restarts, rollouts and new Pods, firing alerts, a Ready change | how often, or for how long an alert fired | when first (`at 08:00`, `from 07:46`) |
| a Pod whose usual level differs from its peers' | that level | `peers 305ms` |

`+2` after what a row measures says the hop has two more raised signals on
the same lane; `s` lists them.

Which group a row is in is the overlap of its lane's marks with the episodes
(`slowpath.Report.Findings`, which counts with `OverlapCount` as the summary
sentences do). The view chooses nothing.

| Group | Rows |
| --- | --- |
| `LINES UP WITH THE STALLS` (`… THE EPISODES`) | the lane the episodes are counted from, then every lane whose marks overlap at least one episode, the ones that overlap most first |
| `DIFFERS, BUT NOT DURING A STALL` (`… AN EPISODE`) | a lane that is marked in the window and overlaps no episode; a Pod whose usual level differs from its peers' |
| `QUIET` | what was read and has no mark, one dim line each. The Pods that crossed no rule while another did share a line (`pods …-2bq7k, …-8wzpd  p99 350ms`), dependencies without a mark share one, and the dated facts of which there were none share one |
| `NOT READ` | every lane without data, with the reason; `dependencies  none declared`; and a hop that was read in part (`dns  1 of 5 signals: no series matched …`), which is quiet only for what was read. The group is left out when it is empty |

Without an episode nothing lines up. The first two groups are then
`DIFFERS FROM ITS PEERS` (a Pod whose usual level differs), `OVER ITS RULE`
(a signal that is marked although no episode was counted from it) and
`IN THE WINDOW` (dated facts).

### Strips, bands and the axis

| Glyph | `noIcons` | Meaning |
| --- | --- | --- |
| `█` | `#` | the lane's rule held in this column: the stretches the overlap is computed from, a rate without its window (see [The rule of a signal](#the-rule-of-a-signal)) |
| `▁` | `_` | sampled, and the rule did not hold |
| `▲` | `^` | a dated fact |
| `·` | `.` | read, and nothing happened |
| `×` | `x` | no data. It is never drawn as quiet |
| `▼` | `v` | an episode, on the marker row; the axis is heavy under it (`━`, `=`) |

The row the episodes are counted from draws the stretches of the one signal
it names, so its marks are the bands. One strip column is the window divided
by the width of the strips (about 57s for 1h at 132 columns, 2m13s at 80),
and a mark fills every column it touches.

One colour per subject on the whole page, from the semantic palette: the Pod
that differs from its peers in the failure colour, any other subject that
lines up in the warning colour, a subject that is raised without lining up
in the progress colour. Quiet rows are muted and dim. The band is the skin's
`band` token; without colour the marker row and the axis carry the episodes.

### The two cursors

The row cursor (`>`) moves over the rows that are on the screen. It opens on
a row of the Pod the view was opened for, else on the first row of a Pod that
differs from its peers, else on the first row. `s`, `Enter`, `t` and
`Shift-D` act on its row.

`Left` and `Right` show the time cursor and move it one strip column; `n`
and `Shift-N` put it on the column in which the first raised sample of the
next or previous episode ends. The title states the time of the column and
the value of the selected row there (`cursor 07:27:37–07:28:34 UTC · slow p99
48s`), and each row states its own in the column of what its number is held
against (`┆ 48s`). The value is the worst of the samples that stand for the
column; a sample stands for the step that ends at it. `Esc` puts the cursor
away.

Neither cursor moves a row or a column: the page is planned first, and the
cursors are drawn on the plan.

## Keys

| Key | Action |
| --- | --- |
| `Up` / `Down` (`k` / `j`) | Move the row cursor |
| `Enter` | On a row of a Pod: open `:graph` for that Pod, usage against its limits, for the window read here. On any other row: its queries, samples and rules. On the page of queries: return |
| `s` | Queries, samples and rules behind the selected row, the signal of the row first; for a row of dated facts, the facts with their times. `PgDn` / `PgUp` page; `s`, `Enter` or `Esc` returns |
| `a` | All detail: the hop table, the lanes strip and the summary sentences the view had before the calm page ([below](#all-detail-a)). `a` again returns. It reads nothing |
| `Left` / `Right` | Show the time cursor and move it one column, ten with `Shift` |
| `Home` / `End` | Cursor to the first / last column of the window |
| `n` / `Shift-N` | Move the cursor to the next / previous episode |
| `1` `2` `3` `4` | Window: 15m, 1h (default), 6h, 24h. The batch is sent again; the path is not read again |
| `r` | Read the path and the batch again |
| `Shift-B` | Open `:probe` for the Service ([probe.md](probe.md)). Opening it sends nothing; a request leaves only after `r` and a confirmation there |
| `m` | `:graph` with one chart per Pod of the Service, on the signal that set a Pod apart here (for example `p99`) and for the window read here ([graphs.md](graphs.md#which-pod-is-different-p)) |
| `h` | The existing native Hubble view for the backend Pods ([hubble.md](hubble.md)) |
| `Shift-D` | Grafana hand-off for the selected Pod or the workload, with the window that was read ([grafana-handoff.md](grafana-handoff.md)) |
| `t` | `:timeline` for the selected Pod or the workload |
| `b` | Chart style of the trends on the all-detail page: eighth blocks (the default) or a thin braille line, as in `:graph`, the dashboards and `:query`; the choice holds for the session. The strips of the calm page have one form |
| `Esc` | Put the time cursor away; then close the queries; then back |

These keys are the ones the sibling views use. `Shift-B` is the probe and
`Shift-W` is this view, on a row of the resource list and inside `:probe`;
`m`, `t` and `Shift-D` are the graph, the timeline and Grafana as everywhere
else; the digits are the windows of `:graph`, a drawn dashboard and
`:query`; the cursor moves as it does there; `n` and `Shift-N` step forward
and back, as they step through signals in `:graph` and rollouts in
`:impact`; `a` is all detail in every view laid out the calm way. `p` is not
bound here: it is the per-Pod page of `:graph`.

A key answers with the reason when its view is unavailable for the selection
(no Prometheus source for `m`, for example) instead of doing nothing. The
page has two lines of keys. The `next` line names the likely steps in words,
and only those that would run: `Shift-B probe the pods now`, `m compare the
pods`, what `Enter` does on the selected row (`open …-hx4mt`, `its queries`)
and `Shift-D open in Grafana`; a narrow line drops steps from its end. The
dim line under it names the rest, `a` and `s` first and the way out last,
and drops from before the way out.

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

One hop per stretch of the path, in the order a request meets them.

| Row | Read from | Signals |
| --- | --- | --- |
| client → ingress | the ingress profiles (default `ingress-nginx`), pinned to the Ingress and the Service | request rate, HTTP 499 rate (the client closed the request while waiting) and its share of all requests, 5xx share, p99 request duration, p99 upstream response duration |
| ingress → service | the Kubernetes API, plus `kube_pod_status_ready` when kube-state-metrics is scraped | endpoints ready now; Pods that were created or changed Ready inside the window; when a Pod was not Ready |
| service → pods | the app profiles (default `red`, `container-cpu`), per Pod, plus `kube_pod_container_status_restarts_total` and `container_memory_working_set_bytes` over `kube_pod_container_resource_limits` | p99 latency, Puma queue and free threads with the `gitlab-webservice` profile, CPU throttled share, restarts, memory against the limit |
| pods → *dependency* | the profile of each dependency declared under `requestPath.services.<namespace/name>.dependencies` | that profile's signals (`postgres-exporter`, `redis-exporter`, `grpc-server`, or your own) |
| dns | the DNS profiles (default `coredns`, cluster-wide) and the `dns-errors` signal of the network profile | p99 lookup time, SERVFAIL and NXDOMAIN share, share of DNS errors answered to these Pods |
| network | the network profiles (default `cilium-hubble`), per destination Pod | drops by reason, TCP resets per new connection, flows |

## All detail (`a`)

`a` shows what the view showed before the calm page, and `a` again returns:
every hop as a row of a table, the lanes as a strip of their own under it,
and what lines up as sentences. Nothing is read for it. This section,
[When it happened](#when-it-happened) and [What lines up](#what-lines-up)
describe that page; the calm page draws the same lanes and states the same
overlap.

`Up` and `Down` select a hop, or a Pod of the app hop. `Enter` on the app
hop lists or folds its Pods, on a Pod opens `:graph` for it, and on any other
hop shows its queries in place of the lanes, as `s` does.

Columns: `HOP`, `WHAT` (the host, the Service or the profile), `LAST 1h` (a
trend of the hop's main signal in eighth blocks, one cell high, with `×` for
a cell without a sample, scaled so that the usual level sits low and only a
burst reaches the top), `WORST` (the key number: the worst
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

On the all-detail page the bottom half is a strip of lanes on one time axis;
on the calm page each lane is a row. A filled cell means
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

On the all-detail page one column is a round stretch of time (1m for 1h at
132 columns, 2m at 80 and 60), stated in the title or in the axis gutter. The edges of a stretch
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

The answer and the groups of the calm page, and the summary of the
all-detail page, are computed from interval overlap only. The summary states:

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

The application keeps two rows at the top of the terminal and two at the
bottom, so the view is given 132x32, 80x20 and 60x20 in terminals of 132x36,
80x24 and 60x24. The calm page gives room up in the order of
[presentation.md](presentation.md#what-a-smaller-screen-gives-up), and only
as far as the size needs (`TestSlowCalmPageSaysWhatEachSizeGivesUp`):

| Terminal | Strips | What the page gives up, in this order |
| --- | --- | --- |
| 132x36 | about 63 columns | nothing. With a `NOT READ` group, the blank rows around the answer and the keys |
| 80x24 | 27 columns | the times in the first sentence and the names of the signals in the second (the marker row and the rows have them); the blank rows; `QUIET` and `NOT READ` to one line each; `DIFFERS, BUT NOT DURING A STALL` to one line; the column of what a number is held against; the hints at the end of the second key line |
| 60x24 | 20 columns | the same, and what a row measures is shortened and marked (`client give-ups…`) |
| fewer rows than the rows that line up | | the last of those rows, counted in a line that names the key that shows them (`not shown at this size (a shows all): 7 of 8 rows of LINES UP WITH THE STALLS`) |
| under about 41 columns | none | the strips: the rows are written as text and the times return to the answer |

At 80x24 and 60x24 every row that lines up keeps its number and its strip,
and both key lines stay: the page keeps the line that names `a` and `s` in
place of its last blank rows.

```text
Slow requests  Service gitlab/gitlab-webservice-default · last 1h  08:20 · 35/35
  3 stalls: clients gave up waiting, for 2 to 4 minutes each.
  All 3 line up with 4 signals on pod …-hx4mt.
  2 of 3 also line up with not Ready on pod …-hx4mt.
  Overlap in time, not proof of cause.
                                              stalls    ▼▼      ▼▼▼         ▼▼
  LINES UP WITH THE STALLS
  ingress      client give-ups (HTTP 499) +2  6.29%  ▁▁▁██▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁██▁▁
> pod …-hx4mt  slow p99                       48s    ▁▁▁██▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁██▁▁
               queueing                       30     ▁▁▁██▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁██▁▁
               no free threads                0      ▁▁▁██▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁██▁▁
               CPU throttling                 86%    ▁▁▁██▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁██▁▁
               not Ready                      twice  ▁▁▁▁█▁▁▁▁▁▁▁██▁▁▁▁▁▁▁▁▁▁▁▁▁
                                                     ───━┯──────━━━────┬────━━──
  UTC                                                  07:30         08:00
  DIFFERS, BUT NOT DURING A STALL  network packet drops (POLICY_DENIED) 0.37/s …
  QUIET  pods …-2bq7k, …-8wzpd · 3 dependencies · dns · restarts · rollouts
  next  Shift-B probe the pods now  m compare the pods  Enter open …-hx4mt
  ↑↓ row · a show every signal · s queries · ←→ time · 1-4 window · Esc back
```

- The all-detail page: at 132x36 every hop and the Pods of an outlying app
  hop, the title, one lane per raised signal, the summary and the next
  steps; at 80x24 the hop table without the `WHAT` column and with quiet
  dependencies on one row, the lanes with marks, and the summary; at 60
  columns hop, key number and state word, with the trend column left out
  first. A lane label that does not fit keeps what its marks mean and the
  Pod (`not ready …-hx4mt`, `p99 +3 …-hx4mt`) before the name of the hop.
- The page of queries and rules (`s`) wraps at every width: no number, rule
  or query is cut.
- With `noIcons` the calm page is ASCII only (`|` for the separator, `#`
  for a mark, `v` for an episode, `x` for no data, `...` for an elision);
  the all-detail page keeps the `·` separator.
- State is always a word or a glyph. Color repeats it and is taken from the
  semantic palette.

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
- The calm page names one raised signal per hop on its row and counts the
  others (`+2`); `s` and `a` list them. The Pods of the Service have a row
  per raised signal, four at most.
- A strip column is rarely a whole minute, and a mark fills every column it
  touches: a dated fact half a minute before a stall can stand in the first
  column of its band. The answer and the groups are computed from the times,
  never from the columns.
- The value at the time cursor is what the source answered. For a rate it
  is still raised in the columns after a band while the burst leaves the
  rate window.
- The row cursor moves over the rows that are on the screen. A group that
  is folded to one line is reached on a taller screen or with `a`.
- A reason for not reading is cut at the strips on the calm page; `s` on
  the row states it in full.
- Nothing here proves a cause, and a quiet window is not proof of health.

## Trying it

`scripts/operator-study-fixture.py` serves the "GitLab intermittent hang"
scenario described in [request-path.md](request-path.md). Open the Service
list of namespace `gitlab`, select `gitlab-webservice-default` and press
`Shift-W`: three stalls, the rows of one Pod of three over their rules
inside every band (p99, queue, free threads and CPU throttling), quiet
peers, dependencies and DNS, and one burst of dropped packets towards
another Pod between two bands, under `DIFFERS, BUT NOT DURING A STALL`. `a`
shows the hop table and the lanes. `--deny-source prometheus` shows the
not-read state. `:ingresses gitlab` lists the Ingress, and `Shift-W` on its
row opens the same view for its backend Service. From there `m` opens the
three Pods side by side on `p99` and `Shift-B` opens the probe.

Code: `internal/slowpath` resolves the path (`Resolve`), plans the batch
(`BuildPlan`), sends it (`Collect`) and evaluates it (`Evaluate`: rules,
peers, lanes, episodes, summary) and states the read for the calm page
(`Findings`, `Answer`); `internal/view/slow.go` is the view, `slow_calm.go`
its calm page, built with `internal/calm`, and `slow_render.go` the
all-detail page. `internal/slowpath/slowtest` is the synthetic
story the tests use.

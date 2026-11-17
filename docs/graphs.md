# Graphs (`:graph`)

`:graph` draws time-series charts in the terminal for one selected object,
from the Prometheus source declared for the context
([observability-sources.md](observability-sources.md)). It is how usage can be
seen against a limit when metrics-server is denied or absent.

It opens on demand: `m` on a Pod, Deployment, StatefulSet, DaemonSet,
ReplicaSet, Service or Node list, `:graph` (or `:graph 15m|1h|6h|24h`) with
such an object selected, or "Graph: usage vs limits over time" in the Ctrl-O
palette. For a workload or a Service, `p` on the page (or `:graph pods`, or
"Per-Pod graphs: which Pod differs" in the palette) switches to one small
chart per Pod; see [Which Pod is different](#which-pod-is-different-p).
`m` is the same key in `:problems`, `:alerts` and `:timeline`, where it graphs
the object of the selected row (in `:timeline`, the focused entry's object or
the timeline's own workload, over the timeline's window). In `:impact`,
`Enter` opens the graph over a window that holds both compared ranges,
scrolled to the chart of the selected signal: Memory peak and Memory/limit
open on MEMORY, CPU usage and CPU throttled on CPU. Request rate, error
ratio, latency and readiness have no chart here, and `:impact` says so
instead of implying one. Where no Prometheus source is declared, `m` answers
with that reason and reads nothing, and the "next:" line of a list never
names `:graph`.

`Shift-D` on the graph hands the same object to Grafana with the graph's own
window ([grafana-handoff.md](grafana-handoff.md)). `Shift-W` opens `:slow`
([slow-requests.md](slow-requests.md)) and `Shift-B` opens `:probe`
([probe.md](probe.md)) for the graphed object, the keys that open them from a
row; for a Node, which has neither, the key says so. In `:slow`, `m` opens
the per-Pod page below.

## What is never done

- Nothing is read while browsing or on a refresh tick. A page is read when it
  opens, when the window changes (`1`–`4`) and on `r`. There is no polling.
- No query text is typed or taken from the cluster. Every query is a fixed
  template built with the typed builder; names from the cluster are matcher
  values and are always escaped.
- Nothing mutates the cluster. Object times for the axis marks are bounded
  reads through the Kubernetes API with your credentials.
- A hole is never drawn as zero, and nothing on the page says why something
  happened. Marks on the time axis state order, not cause.

## The page

```
Graph · Pod apps/checkout-api-7d9f8c6b5d-x2x9q · last 1h to 05:19Z · step 30s
read 05:19:29Z · 10 of 10 queries answered · 2 no data
MEMORY  a working set 253Mi  ─ limit 256Mi  ┄ request 128Mi
   256Mi ┤                                        ───⡜⠉⠹⣠⠋⠉⢣⢠⠋⠙⡄⡜⠉⠹⣠⠋⠉⢣a
   128Mi ┤                                        ┄⡜┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄
       0 ┤                                                             ┆
restarts │                                            ▲  ▲   ▲ ▲  ▲   ▲┆
         └─┬─────────┬────────┬────────┬────────┬─────R───F┬───────X──^
                   04:25    04:30    04:35    04:40      05:10
R rollout rev 2 05:07 · F first restart 05:09 · X last exit Error (1) 05:17 · order, not cause
Cursor 05:19:00Z (window end) · restarts 6, +6 in window
```

- The first line names the object, the window, the window end and the step.
  The second says when the read began and how the queries went.
- Each chart has a legend line. Every drawn series has a glyph that is also
  in the plot: a line is one continuous stroke that ends in its letter (`a`,
  `b`, …), and when a chart has several lines the letter also repeats along
  the line; a limit, allocatable amount or size is a
  `─` level; a request is a `┄` level. Colour repeats this and is never the
  only difference.
- The value after each name is the sample at the cursor. `←`/`→`
  move the cursor one step of the sample grid, with Shift ten steps, and
  Home/End jump to the window edges. Nothing is interpolated: `–` means there
  is no sample at the cursor (with `last 05:03` or `first 05:07` when the
  series stopped earlier or began later), and `no data` means the query was
  answered and matched nothing in the whole window.
- The Y axis starts at zero and is labelled at the top, the middle and zero,
  in Kubernetes units (Mi/Gi, millicores below one core) or bytes per second.
  The top is a round number whose half is round too: a count ends on a whole
  number with a whole half (2, 4, 6, 10, 20, never a tick at 0.5 or 2.5), and
  a duration longer than a minute ends on whole minutes or hours.
- A row that carries a limit, request, allocatable or size line is labelled
  with that level's own value (`1.5 ┤───`), so a line never sits on a row
  that names another number. Data is always drawn over a level line, never
  under it. In a chart only a few rows high a request and a limit can fall
  into the same text row; then only the limit is drawn there, and the legend
  still gives both values.
- A legend takes at most two lines. Entries that do not fit are counted
  (`+1 more`), never dropped silently; `s` lists every series.
- All charts share the time axis at the bottom, labelled with UTC clock
  times. `↑`/`↓` show the charts that do not fit; the second line says how
  many are shown. At 80x24 two charts fit; at 60 columns the page keeps one
  or two charts and the readout, and shortens words rather than facts.
- `s` shows exactly what was asked: the window with dates, the step, when the
  read began, the source, every PromQL text with its state, series and sample
  counts, and for each series where it begins late, ends early or has gaps.
- With `ui.noIcons` the page is drawn with plain characters: `*`, `+`, `o`,
  `x` for lines at one level per row, `=` and `.` for levels, `^` for
  restarts. The braille drawing has two dots per column and four per row.

## Charts and the metrics they need

A Pod or workload page needs cAdvisor (kubelet) and kube-state-metrics series
with `namespace` and `pod` labels; a Node page needs node-exporter and
kube-state-metrics series with a `node` label. A metric that is not scraped,
or is labelled differently, shows as `no data`, never as zero.

Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet:

| Chart | Series | Metric |
| --- | --- | --- |
| MEMORY | working set, the peak within each step | `container_memory_working_set_bytes` |
| | limit, request | `kube_pod_container_resource_limits`, `kube_pod_container_resource_requests` with `resource="memory"` |
| CPU cores | usage, a rate over four steps (at least 2m) | `container_cpu_usage_seconds_total` |
| | limit, request | the same two with `resource="cpu"` |
| | throttled share of periods, read at the cursor only | `container_cpu_cfs_throttled_periods_total` / `container_cpu_cfs_periods_total` |
| NETWORK | receive and transmit rate | `container_network_receive_bytes_total`, `container_network_transmit_bytes_total` |
| restarts | a mark where the counter rose between two neighbouring samples | `kube_pod_container_status_restarts_total` |

Container series exclude the pod cgroup (`container=""`) and the pause
container, and are summed per Pod. Limits and requests are summed over the
containers that declare one, so a Pod in which only some containers have a
limit shows the sum of those.

Node:

| Chart | Series | Metric |
| --- | --- | --- |
| CPU cores | busy (all modes but idle, iowait, steal) against allocatable | `node_cpu_seconds_total`, `kube_node_status_allocatable{resource="cpu"}` |
| MEMORY | total minus available against allocatable | `node_memory_MemTotal_bytes`, `node_memory_MemAvailable_bytes`, `kube_node_status_allocatable{resource="memory"}` |
| DISK / | size minus available on the root filesystem against its size | `node_filesystem_size_bytes`, `node_filesystem_avail_bytes` with `mountpoint="/"` |
| PODS | Pods bound to the Node against allocatable | `kube_pod_info`, `kube_node_status_allocatable{resource="pods"}` |

### Workloads

A workload's Pods are found with one instant query of `kube_pod_info` over
the window (`created_by_kind`, `created_by_name`), so Pods that a rollout has
already replaced are still drawn. A Deployment is matched through ReplicaSets
named `<deployment>-<hash>`. If that query names no Pod, the page says so and
asks nothing else; kube-state-metrics is required for a workload graph. The
same query keeps each Pod's `node`, `created_by_kind` and `created_by_name`
labels, which place the Pod in a group when the series are regrouped.

### Services

A Service is graphed through the Pods behind it. They are resolved from the
cluster, with your credentials, by the bounded and UID-checked collector of
`:network-review` ([network-path-review.md](network-path-review.md)): the
Pods the Service's selector matches now and the Pods its EndpointSlices name.
A Service that was deleted and recreated under the same name is not followed.
The charts are the workload charts for those Pods; there are no rollout marks,
because a Service has no revisions. A Pod the Service selected earlier in the
window and no longer selects is not drawn, and the `s` page says so. When the
Service cannot be read or selects no Pod, the page says which and asks
Prometheus nothing. One more instant query of `kube_pod_info` gives the Node
and owner of each Pod; without it the charts are still drawn.

Up to four Pods are drawn as one line each. More are folded into the minimum,
average and maximum across Pods, and the second line says how many Pods were
folded. Limits and requests of a workload are drawn as the lowest value among
its Pods (`min limit`, `min request`): after a rollout that lowers a limit,
the level steps down where the first Pod with the new limit appears. At most
30 Pod names (900 characters) go into one matcher; Pods beyond that are
counted on the page as NOT READ.

## Which Pod is different (`p`)

With several Pods the overlaid lines run on top of each other. `p` draws one
small chart per Pod instead, all on one Y scale and one time axis, so the Pod
that behaves differently is the chart that looks different.

```
Graph · Deployment gitlab/gitlab-webservice-default · last 1h to 09:41Z · step 30s
read 09:41:29Z · 13 of 13 queries answered · 3 pods
throttled*  cpu*  memory  restarts  [p99*]  5xx  queued*  free-threads*  busy-threads*  network
* hx4mt differs in 6 of 10 signals: throttled, cpu, p99, queued, free-threads, busy-threads · a difference, not a cause
P99 by pod · worst first by peak · Rails request duration, 99th percentile, gitlab-webservice profile
     hx4mt · worker-f    OUTLIER · peak 47.9s   8wzpd · worker-e    peak 346ms   2bq7k · worker-d    peak 346ms
50s ┤       ⡆           ⢰⢲                  ┆  │                             ┆  │                             ┆
25s ┤      ⡇⠘⡆          ⡇ ⡇              ⡇⡇ ┆  │                             ┆  │                             ┆
  0 ┤⣀⣀⣀⣀⣀⡼  ⢸⣀⣀⣀⣀⣀⣀⣀⣀⣀⡇   ⣇⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⡼  ⣇⣀  │⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀  │⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀

EVERY SIGNAL · one row per signal, each on its own scale · the number is what ranks the Pod · * differs from its peers
                hx4mt · worker-f                 8wzpd · worker-e                 2bq7k · worker-d
  throttled*    ⣀⣀⣀⣸⠙⢲⣀⣀⣀⣀⣀⣰⠚⠙⢲⣀⣀⣀⣀⣀⣀⣰⠒⢲⣀ 90%*      ⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀ 1.26%     ⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀ 1.26%
> p99*          ⣀⣀⣀⣸⠙⢲⣀⣀⣀⣀⣀⣠⠼⠙⢲⣀⣀⣀⣀⣀⣀⣰⠲⢤⣀ 47.9s*    ⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀ 346ms     ⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀ 346ms
Cursor 09:41:00Z (window end) · hx4mt 346ms · 8wzpd 346ms · 2bq7k 346ms
```

- The third line names every signal. The selected one is in brackets; a star
  marks a signal in which a Pod differs from its peers; a signal that could
  not be compared carries the reason (`5xx (no data)`, `throttled (denied)`).
  `n` and `Shift-N` step through the signals; from the overlaid charts `n`
  opens this page. A page opens on the first signal in which a Pod differs,
  else on the first that has samples. Where the names do not all fit, the
  ones on either side are counted (`+4 earlier`, `+5 more`).
- The fourth line says which Pod differs and in which signals. It states a
  difference between peers over the same window; it never says why. That
  caveat is kept when the line is shortened.
- Charts are sorted worst first: by the peak, by the lowest value for a
  signal that is worse when low (free threads), by the rise of a counter
  (restarts), and for memory by the peak as a share of the limit when every
  Pod has one. Each title has the short Pod name, its Node, the word
  `OUTLIER` where it applies and the ranking number; with room also the last
  value. All titles of a page take the same form. A Pod that was asked for
  and has no samples is still drawn, as `no samples`, last.
- A limit is drawn as a `─` level in the CPU and memory charts and is part
  of the shared scale.
- `←`/`→` move one cursor through every chart at once. The readout under the
  charts lists every Pod's value at that time (`–` where a Pod has no sample
  there), and counts the Pods it has no room for.
- Pods that do not fit are counted in the second line (`pods 1-4 of 12`) and
  `↑`/`↓` page through them. The scale is that of all Pods on every page.
- Where the page is tall enough, a table under the charts shows every signal
  at once: one row per signal, one thin trend per Pod on that row's own
  scale, and the number that ranks the Pod, starred where the Pod differs.
  The Pods keep the order of the charts above.
- At 80x24 two by two charts fit. Below 72 columns the page is a ranked list
  instead: one row per Pod with its Node, a thin trend on the shared scale,
  the ranking number, the value at the cursor and `OUTLIER`. Under it, where
  there is room, every signal is one row with its worst Pod, that Pod's
  number and the median of all Pods; signals without a row are counted.
  Narrower still, the Node column goes first and then the value at the
  cursor (the header then no longer names it); a name is shortened last.
- The page fits from 40x12 up, the smallest size the view draws at: the key
  line is always the last line, and a status that does not fit two lines
  keeps the count of what is not shown.
- `p` returns to the overlaid lines. `s` lists the per-Pod queries too.
- `:slow` opens this page with `m`: for the Pods behind its Service, on the
  signal that set a Pod apart there (for example `p99`) and for the window it
  read, so the two views show the same Pods, the same reading and the same
  minutes. The signal is named as `profile/signal`; when this page reads that
  reading under its own name (`throttled`, `cpu`, `memory`, `restarts`) the
  name alone finds it, and an unknown one opens on the first signal.
- On a page whose read did not succeed there is nothing to compare; `p`, `n`
  and `g` say so and point at `r`.

### When a Pod is called an OUTLIER

Each Pod is reduced to one number per signal over the window (the number it
is ranked by). A Pod is an `OUTLIER` when that number is far from the median
of all Pods: a modified z-score of at least 3.5 on the median absolute
deviation, at least twice the median (or at most half of it), and further
from the median than a noise floor (5 percentage points for a ratio, 0.1 s,
0.1 cores, 64Mi, 10 kB/s, 1 for a count). Throttling, restarts and profile
signals that are worse when high only mark a Pod above its peers; signals
that are worse when low only below; CPU, memory and network either way.
Fewer than three Pods are never compared, and the page says so. A Pod that
is not marked is not thereby healthy, and a mark is not a cause.

### Signals and the metrics they need

| Signal | What is read per Pod | Metric |
| --- | --- | --- |
| `throttled` | share of CPU periods in which a container was held at its limit | `container_cpu_cfs_throttled_periods_total` / `container_cpu_cfs_periods_total` |
| `cpu` | cores used, against the Pod's limit | `container_cpu_usage_seconds_total`, `kube_pod_container_resource_limits{resource="cpu"}` |
| `memory` | working set, against the Pod's limit | `container_memory_working_set_bytes`, `kube_pod_container_resource_limits{resource="memory"}` |
| `restarts` | the restart counter | `kube_pod_container_status_restarts_total` |
| profile signals | latency, error share, queue and thread pool, as the request-path profiles of the app hop declare them | see [request-path.md](request-path.md) |
| `network` | receive plus transmit bytes per second | `container_network_receive_bytes_total`, `container_network_transmit_bytes_total` |

The profile signals come from the request-path declaration of the Service in
front of the Pods (`requestPath.services.<namespace/name>.app`, else the
defaults `red` and `container-cpu`). For a Service that is the Service
itself. For a workload it is a Service of the namespace whose selector
matches the workload's Pod template, found with one Get of the workload and
one list of at most 200 Services; a Service that has a declaration is
preferred, else the first by name, and the `s` page says which was used and
why. This is the lookup `:slow` makes for a workload, so both views read the
same declaration for the same Pods. Of a profile, the signals that name a worse side and can be read per Pod
are compared (for `gitlab-webservice`: `p99`, `5xx`, `queued`,
`free-threads`, `busy-threads`), at most six; the two `container-cpu`
signals are the page's own `throttled` and `cpu`. A signal whose metric is
not scraped shows as `no data` in the strip and is not drawn as zeros.

## Regroup (`g`)

`g` folds the same per-Pod series into groups, and `Shift-G` steps back:
by pod, by container, by revision, by node, by zone. Each group is one small
chart on the shared scale: the line is the median of the group's Pods, the
two thin lines are their minimum and maximum, and the title gives the group,
its size, the Pod in it that is an `OUTLIER` and the number the group is
ranked by. That number is its worst Pod's (the peak, the lowest value or the
largest rise); with room the title adds the latest value of the median and
says that it is the median (`largest +6 in window · median now 3`). The word
belongs to a Pod, not to the group: a short title of a group of several Pods
counts them (`1 OUTLIER`). Groups are sorted worst first. So "only the Pods
on one Node", "only revision 2" or "only one container" is one chart that
looks different.

| Grouping | A Pod's group comes from |
| --- | --- |
| container | the `container` label of the series; one more read per signal that has it (`throttled`, `cpu`, `memory`, `restarts`), made the first time this grouping is shown. A container is compared with the same container of the other Pods. A profile signal or the network is measured per Pod and says so instead of drawing. |
| revision | `created_by_name` of `kube_pod_info` when the owner is a ReplicaSet (the pod-template hash). For a StatefulSet or DaemonSet, `label_controller_revision_hash` of `kube_pod_labels`, which kube-state-metrics exposes only when that label is allow-listed. The title is `rev N hash` when the object history that the axis marks read names the revision. |
| node | the `node` label of `kube_pod_info` |
| zone | `label_topology_kubernetes_io_zone` of `kube_node_labels` for the Pod's Node, one instant query |

The line under the readout names the grouping, where it came from, the size
of every group and the Pods whose group could not be determined, with the
reason (`zone not known for 8wzpd (kube_node_labels names no zone for their
Nodes)`). Those Pods are drawn as a group of their own named "not known";
no Pod is left out. A Pod name that appears on two Nodes within the window
(a StatefulSet Pod recreated elsewhere) is placed on neither. The cursor
readout gives the median of each group at the cursor; in the ranked list
that column is headed `MEDIAN` and the time.

## Axis marks

Marks are moments read from the objects themselves, plus one from the data:

- `R rollout rev N`: the creation time of the ReplicaSet that owns the Pod;
  or, for a Deployment, StatefulSet or DaemonSet, of its newest retained
  revision. That is the bounded revision read `:impact` makes
  ([impact.md](impact.md): one Get and one list of at most 200 ReplicaSets or
  ControllerRevisions, matched by controller UID), so both views name the
  same rollout moment. When the list was cut by its limit the page says a
  later rollout may be missing.
- `X last exit <reason> (<code>)`: the most recent
  `lastState.terminated.finishedAt` among the Pod's containers.
- `F first restart`: the first rise of the restart counter inside the window.

A mark outside the window is listed with "(before window)" and not placed.
The line ends with "order, not cause"; that caveat is kept when space is
short. Nodes have no object mark.

## States

| What the page shows | Meaning |
| --- | --- |
| charts with values | the queries were answered and had samples |
| `no data`, "no samples in this window (not a zero)" | answered, matched nothing: missing data, not health |
| a line that stops, `– last 05:03` | the series has no later sample; it is not zero |
| `denied`, `timeout`, `absent`, `unavailable` in a legend | that one query failed; the others are still drawn |
| a page of text instead of charts | every query, or the Pod lookup, failed alike, or no Prometheus source is declared; it says which and points to `:sources` |

Leaving the view or pressing a window key cancels the read in flight; a reply
that arrives after the view closed or the destination changed is discarded.
While a new window is being read the old charts are not shown under the new
window's name.

## Limits

- Windows are 15m, 1h, 6h and 24h, with steps of 15s, 30s, 5m and 15m. The
  window end is aligned to the step, and to the minute for the 6h and 24h
  windows, so it is never more than one minute before the read.
- The restart count "+N in window" is the rise between the first and last
  sample of each Pod's counter. A rise across a hole is counted but has no
  mark, because its time is not known; with a single sample the page says the
  rise is not known rather than "+0".
- A Pod of a workload that was asked for and returned no usage samples is
  listed in the legend as "no data"; it is not left out.
- One page is ten GETs for a Pod, eleven for a workload drawn per Pod,
  fifteen for a folded workload and eight for a Node, four at a time, each bounded as described in
  observability-sources.md (8 s, 1 MiB, 32 series, 4096 samples), and all
  within 30 s. A Service adds one instant query and the configuration reads
  of the network path collector.
- The per-Pod comparison is read when `p`, `n` or `g` is first pressed for a
  window, and again on `r` or a window key; never while browsing. While the
  charts are being read again these keys wait for that read instead of
  cancelling it, and the comparison is then read for the charts that arrive.
  Leaving the page cancels a comparison read that is in flight; coming back
  asks it once more. It is one
  range query per signal (five, plus at most six from profiles), two for the
  limits and at most two instant label lookups, minus the series the charts
  already hold for the same window. Regrouping by container adds four. The
  same per-query limits apply: more than 32 series (Pods, or Pods times
  containers) or 4096 samples cut an answer, and the page then says
  `cut at the series or sample limit` in the second line and `CUT` beside the
  signal. At most 30 Pod names go into a matcher, as on the charts.
- The Pods of a Service are the ones it selects now; the network path
  collector reads at most 16 EndpointSlice target Pods that the selector did
  not already return.
- The comparison ranks a Pod by one number over the whole window. A Pod that
  was different for a short part of a long window can be hidden by that; a
  shorter window (`1`) shows it. Free threads of 7, 4 and 0 mark nobody: with
  three Pods the rule needs a clear gap.
- The band of a group is computed here from the per-Pod series, so it covers
  only the Pods that were read. The limit drawn in a group is the lowest
  limit among its Pods. Per-container limits are not drawn.
- The thin trend of the table of every signal has four levels per row; it
  shows the shape, the number beside it is exact.
- A memory value is the peak within its step (at least one minute); a rate is
  taken over four steps and at least two minutes, so short spikes in CPU or
  network are averaged.
- Lines are not joined across more than one and a half steps.
- Node charts require a `node` label on node-exporter series. Setups that
  identify nodes only by `instance` show `no data` for CPU, memory and disk.
- Per-interface network, custom metrics and raw PromQL are not offered here.
  Per-container series are offered only as the container grouping of the
  comparison.
- The charts were exercised against the synthetic fixture and unit tests, not
  against a production Prometheus.

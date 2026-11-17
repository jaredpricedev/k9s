# Graphs (`:graph`)

`:graph` draws time-series charts in the terminal for one selected object,
from the Prometheus source declared for the context
([observability-sources.md](observability-sources.md)). It is how usage can be
seen against a limit when metrics-server is denied or absent.

It opens on demand: `m` on a Pod, Deployment, StatefulSet, DaemonSet,
ReplicaSet or Node list, `:graph` (or `:graph 15m|1h|6h|24h`) with such an
object selected, or "Graph: usage vs limits over time" in the Ctrl-O palette.
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
window ([grafana-handoff.md](grafana-handoff.md)).

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
  in the plot: a line ends in its letter (`a`, `b`, …) and neighbouring Pod
  lines alternate solid and dotted; a limit, allocatable amount or size is a
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
asks nothing else; kube-state-metrics is required for a workload graph.

Up to four Pods are drawn as one line each. More are folded into the minimum,
average and maximum across Pods, and the second line says how many Pods were
folded. Limits and requests of a workload are drawn as the lowest value among
its Pods (`min limit`, `min request`): after a rollout that lowers a limit,
the level steps down where the first Pod with the new limit appears. At most
30 Pod names (900 characters) go into one matcher; Pods beyond that are
counted on the page as NOT READ.

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
  within 30 s.
- A memory value is the peak within its step (at least one minute); a rate is
  taken over four steps and at least two minutes, so short spikes in CPU or
  network are averaged.
- Lines are not joined across more than one and a half steps.
- Node charts require a `node` label on node-exporter series. Setups that
  identify nodes only by `instance` show `no data` for CPU, memory and disk.
- Per-container lines, per-interface network, custom metrics and raw PromQL
  are not offered.
- The charts were exercised against the synthetic fixture and unit tests, not
  against a production Prometheus.

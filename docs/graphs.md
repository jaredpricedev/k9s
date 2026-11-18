# Graphs (`:graph`)

`:graph` draws time-series charts in the terminal for one selected object,
from the Prometheus source declared for the context
([observability-sources.md](observability-sources.md)). It is how usage can be
seen against a limit when metrics-server is denied or absent.

It opens on demand: `m` on a Pod, Deployment, StatefulSet, DaemonSet,
ReplicaSet, Service or Node list, `:graph` (or `:graph 15m|1h|6h|24h`) with
such an object selected, or "Graph: usage vs limits over time" in the Ctrl-O
palette. For a workload or a Service, `p` on the page (or `:graph pods`, or
"Per-Pod graphs: which Pod differs" in the palette) compares its Pods; see
[Which Pod is different](#which-pod-is-different-p).
`m` is the same key in `:problems`, `:alerts` and `:timeline`, where it graphs
the object of the selected row (in `:timeline`, the focused entry's object or
the timeline's own workload, over the timeline's window). In `:impact`,
`Enter` opens the graph over a window that holds both compared ranges,
with the chart of the selected signal at full size: Memory peak and
Memory/limit open on MEMORY, CPU usage and CPU throttled on CPU. Request rate, error
ratio, latency and readiness have no chart here, and `:impact` says so
instead of implying one. Where no Prometheus source is declared, `m` answers
with that reason and reads nothing, and the "next:" line of a list never
names `:graph`.

`Shift-D` on the graph hands the same object to Grafana with the graph's own
window ([grafana-handoff.md](grafana-handoff.md)). `Shift-W` opens `:slow`
([slow-requests.md](slow-requests.md)) and `Shift-B` opens `:probe`
([probe.md](probe.md)) for the graphed object, the keys that open them from a
row; for a Node, which has neither, the key says so. `t` opens `:timeline`
for the same object. In `:slow`, `m` opens the per-Pod page below.

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

The page that opens says first what the series show, and then shows what
that rests on: one chart at full size and every other signal as one row
under it, on the same time axis. It is the calm layout of the incident views
([presentation.md](presentation.md#calm-layout-the-standard-for-incident-views)).
`a` shows every chart stacked, as the page was before
([Every chart](#every-chart-a)); `a` again returns.

```
Graph  Pod checkout-api-7d9f8c6b5d-x2x9q · last 15m                                                  read 04:13 · 10 of 10 queries

  Memory reached its 256Mi limit 7 times. A container restarted after 5 of them.                Order in time, not proof of cause.

  MEMORY  254Mi now  at limit  ─ limit 256Mi  ┄ request 128Mi
     256Mi ┤─────────────▄▆▇▇▇▇▇▇▇▇────▄▆▇▇▇▇▇▇▇▇────▄▆▇▇▇▇▇▇▇▇─────▄▇▇▇▇▇▇▇▇▇────▄▆▇▇▇▇▇▇▇▇────▄▆▇▇▇▇▇▇▇▇────▄▆▇▇▇▇▇▇▇
           │  ┊        ▃▇██████████▅ ▃▇██████████▅  ▃██████████▅  ▃▇██████████  ▃▇██████████▅ ▃▇██████████▅  ▃█████████
           │  ┊      ▂▇█████████████▂█████████████▂▇████████████▂▇████████████▂▇█████████████▂█████████████▂▇██████████
           │  ┊     ▂██████████████████████████████████████████████████████████████████████████████████████████████████
     128Mi ┤┄┄┄┄┄┄▁▅┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄
           │  ┊  ▄█████████████████████████████████████████████████████████████████████████████████████████████████████
           │   ▄███████████████████████████████████████████████████████████████████████████████████████████████████████
           │  ▇████████████████████████████████████████████████████████████████████████████████████████████████████████
           │  █████████████████████████████████████████████████████████████████████████████████████████████████████████
         0 ┤  █████████████████████████████████████████████████████████████████████████████████████████████████████████
              ┊                                                                      ┊
  ALSO        ┊                                                                      ┊
  restarts  ××┊··············▲············▲··········▲···········▲···········▲·······┊··▲······························  6
  CPU       ×××▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇  340m
  network   ×××▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇  9kB/s  tx
            ┬─R───────────┬─────────────┬─────────────┬──────────────┬─────────────┬─X───────────┬─────────────┬───────
  UTC     03:58         04:00         04:02         04:04          04:06         04:08         04:10         04:12

  NO DATA  CPU limit · CPU throttled

  R rollout rev 2 at 03:58
  X last exit Error (1) at 04:08

  next  n next chart (CPU)    a all charts    t what changed    D open in Grafana

  ←→ time · 1-4 window · s queries · b dots · r re-read · Esc back
```

- **The title** names the view, the object and the window, and at the right
  when the read began and how many queries were answered. A read that is not
  complete is in the warning colour and keeps its count at every width. A
  workload or Service adds the number of its Pods. The namespace is written
  before the name only when it is not the one the session is in (`Pod
  gitlab/…` from a session in `apps`), as on every page laid out the calm
  way. A name that does not fit is shortened in the middle, so the
  generated end of a Pod's name stays.
- **The answer** is one or two sentences, the first bold, and a caveat that
  says what they do not claim. They are read off the chart the rule below
  chooses, and stay the same when another chart is put at full size:

  | The samples show | The page says |
  | --- | --- |
  | a line within 5% of its limit, or above it, in one or more stretches of neighbouring samples | `Memory reached its 256Mi limit 6 times.` |
  | memory of one object at its limit, and the restart counter rose between the first sample of a stretch and three steps after its last | `A container restarted after each.` (or `after 5 of them`, or `3 restarts in the last 1h, none right after.`), with `Order in time, not proof of cause.` |
  | CPU throttled in 25% of its periods or more | `CPU was throttled in up to 88% of its periods. Usage peaked at 1.5 cores against a 1.5 cores limit.` |
  | nothing at a limit | `Memory is 60Mi now, 23.4% of its 256Mi limit. 6 restarts in the last 1h.` with `A reading, not a statement of health.` |
  | the last recorded exit of a container was OOMKilled | `The last exit was OOMKilled, at 04:58.` in place of the count of restarts |
  | a chart of several Pods | the line is named: `Memory of x2x9q reached the 256Mi min limit 6 times.` The restarts of a workload are counted, not given to a Pod |
  | no sample | `Memory has no samples in the last 15m. The query matched nothing: that is missing data, not a zero.` with `Not read is not healthy.` |

  When the read failed, the reason is the answer, in the warning colour, and
  the page has no chart ([States](#states)).
- **One chart is at full size.** It is chosen by rule: memory, when it was
  at its limit in the window or the last recorded exit was `OOMKilled`; else
  CPU, when it was throttled in 25% of its periods or more (the level `:slow`
  uses) or was at its limit; else the first chart that was at its limit (a
  Node's disk, its Pods); else memory. `s` says which of these it was. The
  line over the chart is its legend: the value now, the words `at limit`
  where that is so, and the levels drawn (`─ limit 256Mi`, `┄ request
  128Mi`). A chart of several Pods names each line by its letter and keeps
  them solid lines; entries without room are counted (`+1 more`). A single
  line that is not a filled area (dots, noIcons) is drawn with its letter,
  and the legend then names it (`a 254Mi now`). The chart
  is drawn as described under [Every chart](#every-chart-a): a filled area
  for one series, the warning colour where it is at its limit.
- **`ALSO`** holds every other chart as one row: its name, a strip on the
  axis of the chart above, and the value now. The strip is that chart's
  line on the scale it has at full size; with several lines it is the
  highest of them at each time, and the name of that line follows the
  value. `at limit` follows it where that is so. `×` is a time without a
  sample (before the Pod existed), never a level. `restarts` marks every
  rise of the restart counter with `▲` and gives the rise in the window.
- **`n`** (or `Tab`, or `↓`) makes the next row the chart at full size, and
  `Shift-N`, `Shift-Tab` or `↑` the previous; the line of next steps names
  which one is next. A caller that arrives about one chart (`:impact`) finds
  it at full size.
- **Moments** read from the object ([Axis marks](#axis-marks)) are thin
  guides (`┊`) through the whole block, with their letter on the axis and
  one line each under it. A guide starts under the legend line, which is
  not part of the plot. The rollout line adds what the limit of the chart
  was before it when the samples on both sides show that it changed
  (`memory min limit 512Mi before it, 256Mi now`). The first restart has no
  guide here: it is the first `▲` of the restarts row.
- **`NO DATA`** names every query that was answered and matched nothing (a
  limit that is not set, a metric that is not scraped); **`NOT READ`**, in
  the warning colour, every one that was refused or timed out, with its
  state, and Pods beyond the matcher limit. Neither line is ever given up
  for room. **`OUTSIDE THE WINDOW`** names a moment that lies before or
  after the window.
- **`←`/`→`** move the cursor one step of the sample grid, with Shift ten
  steps; Home and End jump to the window edges. At the window end no cursor
  is drawn and every number is the latest sample. Once it is moved, the
  cursor is a `┆` through the block and a `^` on the axis, the legend reads
  `206Mi at 04:55:30`, and the number of every row is its sample at that
  time. Nothing is interpolated. The cursor moves and nothing else does:
  the columns after the strips are held as wide as the widest number and
  note any sample of the window writes there (`9kB/s`, `x2x9q at min
  limit`), so no strip, mark or axis shifts.
- **Two lines of keys**: `next` names the likely steps in words (`p compare
  the pods` for a workload, the next chart, `a all charts`, `t what
  changed` for `:timeline`, `D open in Grafana` when Grafana is declared);
  the dim line names the rest in the order of every calm page (the cursor,
  `1-4 window`, `s queries`, the chart style, `r re-read`) and ends with
  the way out. The menu at the top names `n` after what it steps through:
  `Next chart` here, `Next signal` on the page that compares the Pods.

### What a smaller screen gives up

In the order the calm layout gives things up
([presentation.md](presentation.md#what-a-smaller-screen-gives-up)), and
only as far as the size needs:

1. The strips and the chart shorten with the width; the read status
   shortens (`04:13 · 10/10`), then the name of the object, in the middle.
2. The blank rows go, and the chart gives rows up, down to three, before
   the rows under it do: at 80x24 the page is

```
Graph  Pod checkout-api-7d9f8c6b5d-x2x9q · last 15m                04:13 · 10/10
  Memory reached its 256Mi limit 7 times. A container restarted after 5 of them.
  Order in time, not proof of cause.

  MEMORY  254Mi now  at limit  ─ limit 256Mi  ┄ request 128Mi
     256Mi ┤─────▁▄▇████▁▄▇█████▁▄▇█████▄▇█████▁▄▇█████▁▄▇████▁▄▇████
     128Mi ┤┄┄▂▄▇┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄
         0 ┤ ▇███████████████████████████████████████████████████████
  ALSO       ┊                                     ┊
  restarts  ×┊·······▲······▲····▲······▲······▲···┊▲················  6
  CPU       ×▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇  340m
  network   ×▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇  9kB/s  tx
            ┬R─────┬───────┬──────┬───────┬───────┬X─────┬───────┬───
  UTC     03:58  04:00   04:02  04:04   04:06   04:08  04:10   04:12
  NO DATA  CPU limit · CPU throttled
  R rollout rev 2 at 03:58
  X last exit Error (1) at 04:08

  next  n next chart (CPU)  a all charts  t what changed  D open in Grafana
  ←→ time · 1-4 window · s queries · b dots · r re-read · Esc back
```

3. Then `ALSO` folds to one line that keeps every number (`ALSO  restarts
   6 · CPU 340m · network 9kB/s tx`): at 60 columns the answer takes three
   lines, and that is what an 80x24 terminal narrowed to 60 shows. The
   legend counts the levels it has no room for.
4. Then the second key line goes, and last the guides of the moments: they
   become one line (`MOMENTS`). What is no longer on the screen is named
   in one line with the key that shows it (`not shown at this size (a shows
   all): MOMENTS, ALSO`), and the page ends as the chart alone on its axis
   under the answer. `NO DATA` and `NOT READ` stay.

## Every chart (`a`)

`a` is the page with all the detail: every chart stacked on one time axis,
each with its own legend line, the restarts strip, the marks and the cursor
readout. Its first line ends with `a short page` where there is room.

```
Graph · Pod apps/checkout-api-7d9f8c6b5d-x2x9q · last 1h to 14:35:30Z · step 30s
read 14:35:43Z · 10 of 10 queries answered · 2 no data · 2 of 3 charts (↑/↓)
MEMORY  █ working set 254Mi at limit  ─ limit 256Mi  ┄ request 128Mi
   256Mi ┤                                                       ──▄█─██▄█▄█─███
         │                                                         ██▄██████▄███
   128Mi ┤                                                       ┄▃┄┄┄┄┄┄┄┄┄┄┄┄┄
         │                                                        ██████████████
       0 ┤                                                        ██████████████
CPU cores  █ usage 340m  ─ limit no data  ┄ request 100m  throttled no data
    400m ┤                                                        ▃▃▃▃▃▃▃▃▃▃▃▃▃▃
    200m ┤                                                        ██████████████
    100m ┤                                                       ┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄
       0 ┤                                                        ██████████████
restarts │                                                          ▲ ▲ ▲ ▲ ▲ ▲┆
         └─────┬──────────┬───────────┬───────────┬──────────┬───R──F────┬───X─^
             13:40      13:50       14:00       14:10      14:20       14:30
R rollout rev 2 14:23 · F first restart 14:25 · X last exit Error (1) 14:33
order, not cause
Cursor 14:35:30Z (window end) · restarts 6, +6 in window
←/→ cursor · 1 15m 2 1h 3 6h 4 24h · r re-read · s queries · b dots · Esc back
```

- The first line names the object, the window, the window end and the step.
  The second says when the read began and how the queries went.
- A chart with one series (limits and requests are levels, not series) is a
  **filled area**: each column is filled from zero up to its value in eighth
  blocks (`▁▂▃▄▅▆▇█`), so a chart of H rows has 8×H levels. The top edge of
  the area is the value. A column without a sample is empty; a sample of
  zero is still one eighth high, so a hole is never mistaken for zero.
- A chart with several series draws each as a **solid stepped line** of
  box-drawing characters (`─ │ ╭ ╮ ╰ ╯`): level along a row, a vertical run
  where it changes rows, however far, and a break where samples are missing.
  One row is one level, so such a line is coarser than an area; the legend
  has the exact value. A line ends in its letter (`a`, `b`, …) and the
  letter repeats along its level stretches, so lines are told apart without
  colour. Where two lines run level in the same cells the first keeps them
  and both letters are there; a turn of the second is still drawn.
- `b` switches every chart and trend to **braille dots** and back (the
  footer names it as `b dots` / `b solid`). Dots have two columns and four
  rows per cell: finer, and harder to read. The choice holds for the
  session and is the same in `:graph`, `:slow`, drawn dashboards and
  `:query`.
- Each chart has a legend line. Every drawn series has a glyph that is also
  in the plot: `█` for a filled area, its letter for a line; a limit,
  allocatable amount or size is a `─` level; a request is a `┄` level.
  Colour repeats this and is never the only difference.
- Where an area comes within 5% of its limit (or above it) its columns are
  drawn in the warning colour, its top is drawn in the limit's row in place
  of the line, and the legend says `at limit` after the value at the cursor
  (`at min limit`, `at allocatable`, `at size` for those levels). The words
  are there in every style and for lines too.
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
  that names another number. The top of an area and every line are always
  drawn over a level line, never under it; a level that lies under the top
  of an area is drawn over the area's body, so it stays visible (the `┄`
  row above). In a chart only a few rows high a request and a limit can fall
  into the same text row; then only the limit is drawn there, and the legend
  still gives both values.
- Ticks, levels, lines and areas share one scale: each row is an equal
  share of the axis, the bottom row starts at zero and the top row ends at
  the top. A level exactly on the edge between two rows is drawn in the row
  nearer the middle, which keeps round levels off the rows that carry the
  scale's own labels.
- A legend takes at most two lines. Entries that do not fit are counted
  (`+1 more`), never dropped silently; `s` lists every series.
- All charts share the time axis at the bottom, labelled with UTC clock
  times. `↑`/`↓` (and `n`, `Tab`) show the charts that do not fit; the
  second line says how many are shown. At 80x24 two charts fit; at 60
  columns the page keeps one or two charts and the readout, and shortens
  words rather than facts.
- `s` shows exactly what was asked: the window with dates, the step, when the
  read began, the source, every PromQL text with its state, series and sample
  counts, and for each series where it begins late, ends early or has gaps.
- With `ui.noIcons` the page is drawn with plain characters: `*`, `+`, `o`,
  `x` for lines at one level per row, `=` and `.` for levels, `^` for
  restarts. Nothing is filled there and `b` changes nothing; it says so.

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

With several Pods the overlaid lines run on top of each other. `p` compares
the Pods instead: the page says which Pod differs from its peers and in how
many signals, and shows each of those signals as one row per Pod on a
shared time axis. `a` shows one small chart per Pod and the table of every
signal, as the page was before ([A chart per Pod](#a-chart-per-pod-a)).

```
Compare pods  Service gitlab-webservice-default · 3 pods · last 1h                                   read 04:10 · 13 of 13 queries

  Pod hx4mt differs from the other two in 6 of 10 signals. No other Pod differs.                 A difference, not proof of cause.

            stalls            ▼▼▼▼▼▼                       ▼▼▼▼▼▼▼                                 ▼▼▼▼

  QUEUED · connections waiting for a free Puma thread                                                            peak
  hx4mt  worker-f  ▁▁▁▁▁▁▁▁▁▁▁▃▅████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▃▅█████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▃▅██▁▁▁▁▁▁▁▁  40        differs
  2bq7k  worker-d  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  0
  8wzpd  worker-e  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  0

  FREE-THREADS · Puma threads free to take a request                                                             low
  hx4mt  worker-f  ▁▁▁▁▁▁▁▁▁▁▁██████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁████▁▁▁▁▁▁▁▁  0         differs
  2bq7k  worker-d  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  4
  8wzpd  worker-e  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  4

  P99 · Rails request duration, 99th percentile                                                                  peak
> hx4mt  worker-f  ▁▁▁▁▁▁▁▁▁▁▁▁▃▅███▄▄▂▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▃▅████▇▄▄▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▃▅▇▇▄▂▁▁▁▁▁  47.9s     differs
  2bq7k  worker-d  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  346ms
  8wzpd  worker-e  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  346ms
                   ───────────━━━┯━━────────────┬──────────━━━━━┯━─────────────┬──────────────┬────━━━━───────┬
  UTC                          03:20          03:30           03:40          03:50          04:00           04:10

  ALSO DIFFERS  throttled (hx4mt 90%, peers 1.26%) · cpu (hx4mt 1.57, peers 423m) · busy-threads (hx4mt 8, peers 4)
  ALIKE ON ALL 3  memory · restarts · 5xx · network

  next  Enter open hx4mt    g group by container / revision / node / zone    B probe the pods now

  ↑↓ signal · ←→ time · 1-4 window · a show every signal · s queries · p overlaid lines · r re-read · Esc back
```

- **The answer** counts: `Pod hx4mt differs from the other two in 6 of 10
  signals. No other Pod differs.`, with `A difference, not proof of cause.`
  Signals that had no reading are counted in the same sentence (`3 of 10
  signals had no reading`). "The other two" is said only when each of them
  has samples in every signal the Pod differs in; where one has none the
  sentence says `differs from its peers`, and the rows show which Pod was
  not sampled. Several Pods are listed with their counts.
  When no Pod differs the page says how much it read (`No Pod differs from
  its peers in the 7 signals read. All 13 queries answered.`) and that
  nothing found is not proof of health; with fewer than three Pods it says
  `2 Pods: too few to call one different. 3 are needed.` and calls none
  different.
- **One group per signal in which a Pod differs**, by the rule under [When
  a Pod is called an OUTLIER](#when-a-pod-is-called-an-outlier). The signal
  whose Pod is furthest from the median of its peers comes first, measured
  as a share of the larger of the two values so that signals in different
  units can be ordered. A group has as its heading the signal (for a
  profile signal with what the profile says it measures), and over the
  numbers the word for what they are: `peak`, `low`, `rise`, `of limit`.
- **One row per Pod**: its short name, its Node, its strip, the number that
  ranks it, and the word `differs` on the Pod that does. That Pod is in the
  failure colour everywhere on the page: its name, its number and its
  strip. Its peers are dim, in the order of their names, so a peer is on
  the same row in every group.
- **The strip** of the Pod that differs is a level in its colour at the
  samples at which it is apart from the others: outside their range, at
  least the signal's noise floor from their median, and at least twice
  that median or at most half of it. One other Pod with a sample is enough
  for that, because which Pod is the odd one was decided over the window.
  Every other sample, and every sample of a peer, is the baseline `▁`. `×`
  is a time without a sample. For a signal that is worse when low the level
  is the shortfall, so a dip stands up. A counter (restarts) is drawn as
  its rises (`▲`).
- **The bands** (`▼` on the marker row, the shading behind the rows, the
  heavy part of the axis) are the samples at which the marked row (`>`) is
  apart from the others, under the Pod's name (`hx4mt differs`). What the
  other signals do in those columns is what lines up with it. Opened from
  `:slow` with `m`, the bands are the episodes that view found, under the
  name `episodes`, for as long as the window shown lies inside the one
  `:slow` read; after a window key that reaches further the page falls
  back on the marked row. They state when, never why.
- **`↑`/`↓`** (or `n`/`Shift-N`, `Tab`/`Shift-Tab`) mark the next signal,
  in the order of the page: the ones in which a Pod differs, then the
  others. A signal in which no Pod differs can be marked too: it is then
  shown as the last group, its rows quiet and without bands. `Enter` opens
  the charts of the Pod in the marked row, for the same window.
- **The other signals are one line each**: `ALSO DIFFERS` for those that
  have no room as a group, each with the Pod, its number and the median of
  its peers; `ALIKE ON ALL 3` for those in which no Pod is an `OUTLIER`
  (which is not "identical": see the rule). A Pod without a sample in a
  signal was not compared in it, so the line then reads `ALIKE` and counts
  the Pods that were (`cpu (3 of 4 pods sampled)`). `NOT COMPARED` for those with
  too few Pods that have samples; `NOT READ` with the state of each that
  gave no reading. The last two are never given up for room.
- **More Pods than five** show the Pods that differ (three at most), the
  others three or fewer at a time by name, and one row that counts the rest
  (`+8 more  like their peers`). `PgUp`/`PgDn` turn the page of the peers;
  the key line says which page it is.
- **`←`/`→`** move the cursor as on the charts. Once it is moved, the number
  of every row is its sample under the cursor and the word over the numbers
  is the time (`04:30:00`). The number column is always as wide as that
  time and the widest sample of the signal, so the strips, the bands and
  the axis stay where they are when the cursor moves. On a narrow page the
  label of a group gives way to the word over its numbers (`QUEUED` without
  what it measures) rather than the other way round.
- **How many groups**: as many as the size holds without giving anything
  up, blank rows included; at 132x36 that is three signals of three Pods.
  A second group is worth the blank rows around the answer and the keys;
  no group is worth a row that is left out. At 80x24 one or two groups are
  shown and the rest are in `ALSO DIFFERS`:

```
Compare pods  Service gitlab-w…service-default · 3 pods · last 1h  04:10 · 13/13
  Pod hx4mt differs from the other two in 6 of 10 signals. No other Pod differs.
  A difference, not proof of cause.

            stalls      ▼▼▼          ▼▼▼▼              ▼▼▼
  FREE-THREADS · Puma threads free to take a request           low
  hx4mt  worker-f  ▁▁▁▁▁███▁▁▁▁▁▁▁▁▁▁████▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁  0         differs
  2bq7k  worker-d  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  4
  8wzpd  worker-e  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  4
  P99 · Rails request duration, 99th percentile                peak
> hx4mt  worker-f  ▁▁▁▁▁▄██▄▁▁▁▁▁▁▁▁▁▃███▄▁▁▁▁▁▁▁▁▁▁▁▁▁▁▅▇▄▁▁  47.9s     differs
  2bq7k  worker-d  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  346ms
  8wzpd  worker-e  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  346ms
                   ───┬─━━━─────┬────━━━━──┬─────────┬─━━━───
  UTC               03:15     03:30      03:45     04:00
  ALSO DIFFERS  queued (hx4mt 40, peers 0) +3 more
  ALIKE ON ALL 3  memory · restarts · 5xx · network

  next  Enter open hx4mt  g group by container / revision / node / zone
  ↑↓ signal · ←→ time · 1-4 window · a show every signal · s queries · Esc back
```

  At 60 columns the strips shorten and the key lines name fewer keys;
  nothing else changes. Below 12 columns of strip the rows are written as
  text, and what is not shown is named with the key that shows it.
- `p` returns to the charts. `s` lists the per-Pod queries too, and says
  how this page is derived from them.
- `:slow` opens this page with `m`: for the Pods behind its Service, with
  the signal that set a Pod apart there marked (for example `p99`), for the
  window it read and with its episodes as the bands, so the two views show
  the same Pods, the same reading and the same minutes. The signal is named
  as `profile/signal`; when this page reads that reading under its own name
  (`throttled`, `cpu`, `memory`, `restarts`) the name alone finds it, and
  an unknown one changes nothing.
- On a page whose read did not succeed there is nothing to compare; `p`
  and `g` say so and point at `r`, and so does `n` for the chart.

### A chart per Pod (`a`)

`a` is the page with all the detail: one small chart per Pod for one signal,
all on one Y scale and one time axis, and where the page is tall enough the
table of every signal. Its first line ends with `a short page` where there
is room. `n` and `Shift-N` step through the signals in the order they are
read in, and `↑`/`↓` page through the Pods.

```
Graph · Service gitlab/gitlab-webservice-default · last 1h to 14:38Z · step 30s
read 14:38:11Z · 13 of 13 queries answered · 3 pods
throttled*  cpu*  memory  restarts  [p99*]  5xx  queued*  free-threads*  busy-threads*  network
* hx4mt differs in 6 of 10 signals: throttled, cpu, p99, queued, free-threads, busy-threads · a difference, not a cause
P99 by pod · worst first by peak · Rails request duration, 99th percentile, gitlab-webservice profile
     hx4mt · worker-f    OUTLIER · peak 47.9s   8wzpd · worker-e              peak 346ms   2bq7k · worker-d              peak 346ms
50s ┤      ▅▅           ▅▅                  ┆  │                                       ┆  │                                       ┆
    │      ██           ██                  ┆  │                                       ┆  │                                       ┆
    │      ██           ██               ▂  ┆  │                                       ┆  │                                       ┆
    │      ██           ██               █  ┆  │                                       ┆  │                                       ┆
25s ┤      ██          ▁██▂              █▁ ┆  │                                       ┆  │                                       ┆
    │      ██▃         ████              ██ ┆  │                                       ┆  │                                       ┆
    │     ████         ████             ███ ┆  │                                       ┆  │                                       ┆
    │     ████         ████             ███ ┆  │                                       ┆  │                                       ┆
  0 ┤▁▁▁▁▁████▁▁▁▁▁▁▁▁▁████▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁  │▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  │▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁
    └────┬─────────┬─────────┬─────────┬────^  └────┬─────────┬─────────┬─────────┬────^  └────┬─────────┬─────────┬─────────┬────^
       13:45     14:00     14:15     14:30        13:45     14:00     14:15     14:30        13:45     14:00     14:15     14:30

EVERY SIGNAL · one row per signal, each on its own scale · the number is what ranks the Pod · * differs from its peers
                hx4mt · worker-f                      8wzpd · worker-e                      2bq7k · worker-d
  throttled*    ▁▁▁▃▇▅▁▁▁▁▁▁▆▇▅▁▁▁▁▁▁▁▁▂▆▃▁ 90%*      ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ 1.26%     ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ 1.26%
  cpu*          ▃▃▃▄▆▅▃▃▃▃▃▃▆▇▅▃▃▃▃▃▃▃▃▃▆▄▃ 1.57*     ▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂ 423m      ▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂▂ 423m
  memory        ▆▆▆▆▇▇▇▇▇▇▇▇▆▆▆▆▆▆▆▇▇▇▇▇▇▇▇ 67.1%     ▇▇▆▆▆▆▆▆▇▇▇▇▇▇▇▇▆▆▆▆▆▆▆▇▇▇▇ 67.1%     ▇▇▇▇▇▇▆▆▆▆▆▆▇▇▇▇▇▇▇▇▆▆▆▆▆▆▆ 67.1%
  restarts      ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ +0        ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ +0        ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ +0
> p99*          ▁▁▁▃█▄▁▁▁▁▁▁▅█▄▁▁▁▁▁▁▁▁▁▆▃▁ 47.9s*    ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ 346ms     ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ 346ms
  5xx           ▁▁▁▁▃▂▁▁▁▁▁▁▂▃▂▁▁▁▁▁▁▁▁▁▂▁▁ 0.27%     ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ 0.03%     ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ 0.03%
  queued*       ▁▁▁▆█▁▁▁▁▁▁▁██▁▁▁▁▁▁▁▁▁▄█▁▁ 40*       ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ 0         ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ 0
  free-threads* ▅▅▅▁▁▅▅▅▅▅▆▅▁▁▅▅▅▆▅▅▅▅▅▁▁▅▅ 0*        ▅▇▅▅▅▅▅▅▇▅▅▇▅▅▆▅▅▅▆▅▅▇▅▅▅▅▅ 4         ▅▅▇▅▅▇▅▅▅▅▅▅▇▅▅▆▅▅▅▆▅▅▇▅▅▆▅ 4
  busy-threads* ▄▅▅██▅▅▄▅▅▃▅██▄▅▅▃▅▅▄▅▅██▅▅ 8*        ▅▂▅▅▄▄▅▄▂▅▅▂▅▅▃▄▅▄▃▅▅▂▅▅▄▄▅ 4         ▅▅▂▅▅▂▄▅▄▄▅▅▂▅▅▃▄▅▄▃▅▅▂▅▅▃▄ 4
  network       █▇▇█▄▇▇███▇▇▇▂▇▇███▇▇██▇▅▇█ 392kB/s   ██▇▇██▇▇▇██▇▇██▇▇▇██▇▇███▇▇ 392kB/s   ▇██▇▇██▇▇▇██▇▇███▇▇██▇▇███▇ 392kB/s
Cursor 14:38:00Z (window end) · hx4mt 346ms · 8wzpd 346ms · 2bq7k 346ms
←/→ cursor · n/N signal · g/G regroup · p overlaid lines · ↑/↓ page · 1-4 window · r re-read · s queries · b dots · Esc back
```

- Every small chart is a filled area, like a chart with one series on the
  overlaid page: a Pod near zero is a baseline one eighth high, a Pod
  without samples has no fill and says `no samples`. A trend in a row is
  eighth blocks one cell high, with `×` for a cell without a sample. `b`
  switches both to braille dots and back.

- The third line names every signal. The selected one is in brackets; a star
  marks a signal in which a Pod differs from its peers; a signal that could
  not be compared carries the reason (`5xx (no data)`, `throttled (denied)`).
  `n` and `Shift-N` step through the signals. The page opens on the signal
  the short page marked. Where the names do not all fit, the
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
  of the shared scale. A Pod whose area comes within 5% of its limit is
  drawn in the warning colour there, and the readout under the charts says
  `at limit` after its value.
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
by pod, by container, by revision, by node, by zone.

On the short page a group is one row where a Pod was one: its name, its
size (`2 pods`), its strip, a number, and where a Pod of it differs that
Pod's name and the word `differs`. The strip is the member of the group
that is furthest out at each time (its highest, or for a signal that is
worse when low its lowest), drawn in the Pod's colour at the samples at
which a Pod of the group that differs is apart from the other Pods. The
number is that Pod's own when one Pod of the group differs, else the
group's worst. The title says how the Pods are grouped (`· by revision`),
the answer stays the one about the Pods, and `Enter` opens the Pod of the
marked group. So "only the Pods on one Node" or "only revision 2" is one row
that looks different:

```
Compare pods  Deployment checkout-api · 4 pods · by revision · last 1h                               read 03:00 · 15 of 15 queries

  Pod x2x9q differs from its peers in 5 of 7 signals. No other Pod differs. 1 of 8 signals had no reading.
  A difference, not proof of cause.

              x2x9q differs                                                              ▼▼▼▼▼ ▼ ▼▼▼

  RESTARTS                                                                                              rise
> rev 2 7d9f8c6b5d  2 pods  ×××××××××××××××××××××××××××××××××××××××××××××××××××××××××××··▲·▲·▲·▲·▲·▲··  +6          x2x9q  differs
  rev 1 6c8d7f9b5   2 pods  ··········································································  +0

  ERRORS · share of requests answered with an error status                                              peak
  rev 2 7d9f8c6b5d  2 pods  ×××××××××××××××××××××××××××××××××××××××××××××××××××××××××××███████████████  75%         x2x9q  differs
  rev 1 6c8d7f9b5   2 pods  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  2.03%

  P50 · request duration, median
  rev 2 7d9f8c6b5d  2 pods  ×××××××××××××××××××××××××××××××××××××××××××××××××××××××××××███████████████  556ms       x2x9q  differs
  rev 1 6c8d7f9b5   2 pods  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  80ms

  MEMORY                                                                                                of limit
  rev 2 7d9f8c6b5d  2 pods  ××××××××××××××××××××××××××××××××××××××××××××××××××××××××××▁▇██████████████  99.2%       x2x9q  differs
  rev 1 6c8d7f9b5   2 pods  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  38.1%
                            ───────────┬────────────┬───────────┬───────────┬────────────┯━━━━─━─━━━─┬
  UTC                                02:10        02:20       02:30       02:40        02:50       03:00

  ALSO DIFFERS  network (x2x9q 11.6kB/s, peers 25.2kB/s)
  ALIKE  cpu (3 of 4 pods sampled) · p99 (3 of 4 pods sampled)
  NOT READ  throttled (no data)

  next  Enter open x2x9q    g group by node / zone / pod / container    B probe the pods now

  ↑↓ signal · ←→ time · 1-4 window · a show every signal · s queries · p overlaid lines · r re-read · Esc back
```

By container, the rows are the containers of every Pod (`hx4mt/webservice`)
and a container is compared with the same container of the other Pods; a
signal that is measured per Pod is named in the line `PER POD ONLY`. A group
whose label could not be determined is a row named "not known". `a` shows
the page below.

With `a`, each group is one small
chart on the shared scale: the filled area is the median of the group's Pods,
their maximum is a line above it and their minimum one dash per column
inside it (in braille dots, `b`: a line and two thin lines), and the title
gives the group,
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

On the short page `R` and `X` are guides through the chart and the rows
under it, with one line each (`R rollout rev 2 at 21:58`); the first restart
is the first `▲` of the restarts row and has no letter. A mark outside the
window is named in the line `OUTSIDE THE WINDOW` and not placed. The caveat
is the one beside the answer.

On the page with every chart (`a`) the three marks are named in one line. A
mark outside the window is listed with "(before window)" and not placed.
The line ends with "order, not cause"; that caveat is kept when space is
short. Nodes have no object mark.

## States

| What the page shows | Meaning |
| --- | --- |
| charts with values | the queries were answered and had samples |
| `no data`, "no samples in this window (not a zero)", the line `NO DATA`, a row of `×` | answered, matched nothing: missing data, not health |
| the line `NOT READ`, a read status in the warning colour (`9 of 10 queries`) | a query was refused or not answered; the line names it with its state |
| an answer in the warning colour, `Not read is not healthy.` | the chart the answer is read off has no sample, or nothing could be read |
| a line that stops, `– last 05:03` | the series has no later sample; it is not zero |
| `denied`, `timeout`, `absent`, `unavailable` in a legend | that one query failed; the others are still drawn |
| a page of text instead of charts | every query, or the Pod lookup, failed alike, or no Prometheus source is declared; the answer says which and points to `:sources`, and the keys named are `r` and `s` |

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
- The per-Pod comparison is read when `p` or `g` is first pressed for a
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
- The answer of the short page is derived by fixed rules from the samples of
  one chart. It counts and orders; it does not diagnose. "Reached its limit"
  needs a sample within 5% of the limit: a spike between two samples of a
  long window is not counted, and a memory value is the peak within its
  step, so a stretch can outlast the moment by up to that step. "Restarted
  after" pairs each stretch with one rise of the counter between its first
  sample and three steps after its last; it does not know which container
  restarted or why.
- A row under `ALSO` is one chart folded to one row of eight levels: it
  shows the shape and whether a level is near the top of its own scale, not
  a value. The number beside it is exact.
- On the short page of the comparison a Pod's row is drawn against the
  other Pods sample by sample with one other Pod being enough, so with two
  Pods left at some time the row can stand up where the rule over the
  window would not have been able to tell the two apart. A Pod that does
  not differ over the window is always the baseline, whatever a single
  sample of it did; `a` draws every Pod's own series.
- The order of the signals on that page compares differences across units
  by their share of the larger value. It is an order to read in, not a
  ranking of severity.
- The bands taken from `:slow` are that view's episodes at the time it was
  read; they are not read again here, and are dropped when the window shown
  reaches outside the one `:slow` read.
- At 80x24 the chart at full size has three rows when the object has both
  marks and a line of missing data; `a` or a taller terminal shows more.
- The charts were exercised against the synthetic fixture and unit tests, not
  against a production Prometheus.

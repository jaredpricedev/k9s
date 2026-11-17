# Dashboards in the terminal and the query bar (`v` in `:dashboards`, `:query`)

Two on-demand pages draw Prometheus data as charts in the terminal, with the
same renderer:

- `v` on a row of `:dashboards` draws that Grafana dashboard: its definition
  is read once from the declared Grafana source, and the expressions of its
  panels are run against the declared Prometheus source.
- `:query` charts an expression you type, a query saved under a name, or,
  without an argument, every named query on one page.

Both need a Prometheus source declared for the context
([observability-sources.md](observability-sources.md)); the dashboard page
also needs Grafana. The list of dashboards, links and the hand-off to a
browser are described in [grafana-handoff.md](grafana-handoff.md).

## What is never done

- Nothing is read while browsing or on a refresh tick. A page is read when it
  opens, when the window changes (`1`–`4`) and on `r`. There is no polling.
- A dashboard's definition is read once per page (`GET
  /api/dashboards/uid/<uid>`, one deadline, 1 MiB) and kept; a window change
  and `r` read only the panels again. `r` reads the definition again only
  when the first read did not return one.
- Every query is a read-only range query through the declared Prometheus
  source, with the limits of every other read: an eight-second deadline, 32
  series and 4096 samples per query, at most 120 points per series. Queries
  do not go through Grafana, and no other datasource is contacted.
- Nothing mutates the cluster or Grafana, and nothing is written to the
  configuration. Saving a query means adding it to the context file yourself.
- A panel is never dropped silently and an answer without series is never
  drawn as a flat line. A threshold word is the dashboard author's rule, not
  a finding of this tool.

## Drawing a dashboard

Select a Pod, workload, Node or namespace, open `:dashboards`, choose a
`declared` or `search` row and press `v`. An `explore` row is a link, not a
dashboard, and says so.

```
Dashboard · GitLab / Webservice overview · last 1h to 10:38:30Z · step 30s
read 10:38:38Z · 9/11 panels drawn · 11/11 answered · 1-6/11 ↑↓
for Deployment gitlab/gitlab-webservice-default · namespace=gitlab (selection) · pod=~.* (All)
── Requests ────────────────────────────────────────────────────────────────────────────────────────
> Requests per second                           Slowest pod p99
  36.2/s                                        346ms  green
  ⠉⠉⠉⠉⠉⠙⠚⠉⠉⠉⠉⠉⠉⠉⠉⠉⠉⠉⠙⠒⠚⠉⠉⠉⠉⠉⠉⠉⠉⠉⠉⠉⠉⠉⠉⠉⠙⠚⠉⠉⠉⠉    ⣀⣀⣀⣀⣰⠚⠙⢲⣀⣀⣀⣀⣀⣀⣀⣀⣀⣠⠼⠉⠙⢲⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣠⠴⠲⢤⣀⣀⣀
  min 24.7/s  max 37/s                          min 346ms  max 47.9s (red)
  Puma threads in use                           CPU throttled, worst pod
  37.5%  green                                  3.81%  green
  ━━━━━━━━━━━━━───────────────────── of 100%    ━───────────────────────────────── of 100%
  ⠲⠤⠴⠚⠉⠉⠹⠴⠒⠒⠲⠤⠴⠒⠒⠒⠲⠴⠚⠉⠙⠲⠤⠴⠒⠒⠲⠤⠴⠒⠒⠲⠤⠴⠒⠚⠉⠹⠴⠒⠒⠒    ⣀⣀⣀⣀⣰⠚⠉⠹⢤⣀⣀⣀⣀⣀⣀⣀⣀⣰⠚⠉⠉⠹⢤⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣠⠼⠙⢲⣀⣀⣀
  Request duration p99 by pod                        Requests by pod  a …2bq7k 13.3/s
  a …2bq7k 346ms  b …8wzpd 346ms                     b …8wzpd 12.3/s  c …hx4mt 10.7/s
  c …hx4mt 346ms peak 47.9s
  50s ┤      ⣶⡀           ⢰⢲⡀                       15/s ┤⣀⢀⣀⣀⣀⣀⣀⣀⢀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⡀⣀⣀⣀⣀⣀⣀⡀⣀⣀⣀⣀⣀⣀⡀  ⣀⣀a
      │     ⢠⠇⡇           ⡼ ⡇               ⣰            │⢾⣻⣖⣫⢿⠉⠓⢮⣿⢶⣫⠽⢯⣙⡶a⢶⣫⣷⠋b⠦⣟⣷⣫c⠯⣝⡦⣟⡷⣾⠽⠻⣍⡧⣝⡶⣾⡽cb
  25s ┤     ⢸ ⡇           ⡇ ⡇               ⡟⡆     7.5/s ┤    ⠈⡇ ⢠⠇         ⢸  ⢠⠇             ⢹ ⢸
      │     ⡼ ⢳          ⢸⠁ ⢳              ⢠⠇⢳           │     ⢹ ⡼          ⢸  ⡼              ⠈⡇⡏
      │    ⢀⡇ ⠸⡄         ⣸  ⠸⡄             ⣸ ⢸           │     ⢸ ⡇          ⠈⡇ ⡇               ⣷⠃
    0 ┤⣀⣀⣀⣀⣸⣀⣀⣀⣇⣀⣀⣀⣀⣀⣀a⣀⣀⣇⣀b⣀⣇⣀⣀c⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣇⣀⣸cba      0 ┤     ⠈⠛⠁           ⠙⠚⠁
      └────┬──────────┬─────────┬──────────┬─────        └────┬──────────┬─────────┬──────────┬─────
         09:45      10:00     10:15      10:30              09:45      10:00     10:15      10:30
── Puma and CPU ────────────────────────────────────────────────────────────────────────────────────
Panel 1 of 11 · stat · A sum(rate(http_requests_total{namespace="gitlab", pod=~".*"}[2m]))
←/→ ↑/↓ panel · Enter zoom · 1-4 window · r re-read · s queries · Shift-D Grafana · Esc
```

- The first line names the dashboard, the window, the window end and the
  step. The second says when the read began, how many panels were drawn, how
  the queries went and which panels are on the page. On a page of 24 rows or
  more a third line names the selection and the variables the expressions
  use.
- Panels flow in the dashboard's own order. Panels that share a dashboard row
  stay side by side as far as the width allows: a line chart is at least 48
  cells wide, a value tile at least 24. A row that does not fit is split into
  even lines. Row titles are section lines. The page scrolls by whole panels;
  room left under the last whole line is given to the next chart, squeezed,
  rather than left blank.
- `>` marks the selected panel. The line above the keys says what it asks:
  its type and the expression that was sent, or why it is not drawn.
- All charts on a page share one label gutter, so plots that stand above each
  other start in the same column.

### Supported panel types

| Grafana type | Drawn as |
| --- | --- |
| `timeseries`, `graph` | A line chart over the window. Each series is one continuous line that ends in its letter; with several lines the letter repeats along the line. The legend names each line by the panel's `legendFormat` (`{{label}}` is replaced) or, without one, by the labels that differ, and gives the line's last sample. |
| `stat`, `singlestat` | The value by the panel's reducer (`lastNotNull`, `last`, `first`, `mean`, `max`, `min`, `sum`), the threshold word, a thin sparkline of the window and the window's minimum and maximum. |
| `gauge` | The value, the threshold word, a thin bar against the panel's `min` and `max`, and the sparkline. |
| `bargauge` | One row per series: name, bar and value. A `stat` or `gauge` whose query returns several series is drawn the same way. |
| `row` | A section line. Panels inside a collapsed row are drawn like any other. |

Anything else (`table`, `logs`, `heatmap`, `piechart`, `text`, …) is listed
as `NOT DRAWN` with its type.

What is honoured from the definition (Grafana 12, schema version 41, and
older shapes):

- `fieldConfig.defaults.unit` (for a `graph`, the left axis format): seconds
  (`s`, `ms`, `µs`, `ns`, `m`, `h`, `d`), `percent`, `percentunit`, bytes
  (`bytes`, `decbytes`, `kbytes`, `mbytes`, `gbytes`), `Bps`, rates (`reqps`,
  `ops`, `rps`, `wps`, `iops`, `cps`, `eps`, `pps`), `short`, `none` and
  `cores`. Any other unit is drawn as plain numbers and the panel title says
  `unit <id> (plain numbers)`, so a number is not read in the wrong unit.
- `fieldConfig.defaults.min` and `max`: the range of a gauge or bar, and the
  top of a chart's axis while the data stays under it.
- `fieldConfig.defaults.thresholds`: the step a value falls into is named by
  the colour the author gave it (`green`, `orange`, `red`, …) with the value
  it starts at, for example `48s  red, from 10s`. A colour without a name is
  `step 2 of 3`. Percentage thresholds are evaluated only when the panel has
  both `min` and `max`. When the window's maximum falls into another step
  than the value, the tile says so: `min 346ms  max 47.9s (red)`.
- `legendFormat`, `hide`, and the panel's and each target's `datasource`,
  whether it is an object (`{type, uid}`), a variable (`${datasource}`) or a
  bare name. A panel on mixed datasources draws its Prometheus queries and
  names the ones it leaves out. When the definition does not state the
  datasource type at all, the expression is sent to Prometheus and the panel
  detail says that this was assumed.

In a legend of the grid, `peak` is added to a line whose largest sample in the
window is more than twice its last one (`c …hx4mt 346ms peak 47.9s`), and a
line that stopped before the window end says when (`– last 04:20`). Both are
samples of that line; neither says why. Names that share a long start up to a
dash are shortened to what tells them apart, marked `…`; the zoomed panel and
the `s` page show what was asked.

### Variables

Variables are filled before a query is sent, and the page states what each
one stands for.

1. A variable named in the dashboard's declaration
   (`namespaceVar`, `podVar`, `workloadVar`, `nodeVar` under
   `k9s.observability.grafana.dashboards`) takes the selection's value. For a
   dashboard that is not declared, the variables named `namespace`, `pod`,
   `workload` and `node` do; those are the names a pin snippet proposes.
2. Any other variable keeps the value saved in the dashboard: a `constant`,
   a `custom` or `textbox` value, an `interval` (an automatic interval is the
   step), and the saved choice of a query-backed variable.
3. `All` stands for the variable's `allValue`, or for the listed values of a
   `custom` variable. `All` of a query-backed variable without an `allValue`
   cannot be resolved, because its values come from a query that is not run
   here. A panel that uses such a variable is `NOT DRAWN` with that reason;
   so is one that uses a variable without a saved value, an undefined
   variable or an unsupported variable type.
4. `$__interval` is the step. `$__rate_interval` is four steps and at least
   two minutes. Grafana derives it from the datasource's scrape interval,
   which is not read here; this value is at least as wide for scrape
   intervals up to 30 seconds. `$__range`, `$__range_s`, `$__range_ms` and
   `$__interval_ms` follow the window and step.

`$name`, `${name}`, `${name:regex}`, `${name:pipe}`, `${name:raw}`,
`${name:text}`, `${name:csv}` and `[[name]]` are understood. A value is
escaped for the place it lands in: inside a quoted string it cannot close the
string, and as the operand of `=~` or `!~` a name from the cluster is quoted
as a regular expression. A pattern (an `allValue`) is only accepted by a
`=~` or `!~` matcher. An expression that still holds a literal `$` after
substitution (a regex anchor, a `$1` in `label_replace`) is not sent.

### What is not drawn

A panel that draws nothing takes one line in its place, selectable like any
panel: `Pods and nodes · NOT DRAWN: panel type "table" is not drawn here`.
The reasons are:

- the panel type is not one of the drawn types, or it is a text panel or a
  library panel whose definition is not part of the dashboard JSON;
- its datasource is not Prometheus;
- a variable is not resolved (see above);
- the expression failed: Prometheus rejected it, and its own error text is
  shown, without control characters and bounded;
- no series matched in the window (missing data, not a zero);
- the read was denied, timed out or failed;
- a limit of one read: at most 24 panels and 40 queries are sent per read, a
  panel keeps at most 6 queries and draws at most 12 lines, and a definition
  keeps at most 80 panels and 40 variables. The status line counts what a
  limit cut and `s` lists it.

`s` lists every panel with the expression that was sent and what came back,
every variable, and everything that was not drawn with its reason.

When the definition cannot be read (denied, absent, timeout, over 1 MiB, not
a dashboard), or no Prometheus source can be asked, or every query failed
alike, the page says which source failed and how, draws nothing, and points
to `:sources`.

### Keys

| Key | Action |
| --- | --- |
| `←` `→`, `Tab` `Shift-Tab` | Previous or next panel |
| `↑` `↓` | The panel above or below |
| `Home` `End` | First or last panel |
| `Enter` | Zoom the selected panel; `Esc` returns to the grid |
| `1` `2` `3` `4` | Window: 15m, 1h, 6h, 24h (reads the panels once) |
| `r` | Read the panels again |
| `s` | Queries, variables and what was not drawn |
| `Shift-D` | Open the same dashboard in Grafana with the window drawn here and the variables the selection filled |
| `Esc` | Back |

In a zoomed panel `←` `→` move the cursor along the sample grid (`Shift`
moves ten points, `Home` and `End` go to the window's ends) and `↑` `↓` change
the panel. The legend then holds the samples at the cursor, never an
interpolation. Below the plot are the expression of each query as it was
sent, its state, series and sample counts, the panel's notes and the author's
description. A stat or gauge is zoomed as the series behind it.

## The query bar (`:query`)

| Typed | Result |
| --- | --- |
| `:query` | Every named query as one chart each, for the selection |
| `:query <name>` | That named query alone, with the cursor |
| `:query <expression>` | The expression alone; anything that is not a known name is taken as PromQL |

Names complete in the prompt (`:query thr` then `Tab`). In a query page `e`
puts the expression back into the prompt to change it; a `:query` typed while
a query page is open replaces what the page draws and keeps its selection.
`Shift-D` opens Grafana Explore with the expression as it was sent, when the
Grafana declaration has `publicURL` and `datasource`. The command is also
"Query: chart a PromQL expression or a named query" in the `Ctrl-O` palette.

Variables in an expression:

| Variable | Stands for |
| --- | --- |
| `$namespace` | The selection's namespace |
| `$pod` | The selected Pod. With a workload selected: the pattern `<workload>-.+`, a match on the name, not a read of which Pods the workload owns. Otherwise `.+`. A pattern is only accepted in a `=~` or `!~` matcher |
| `$workload` | The selected Deployment, StatefulSet, DaemonSet, ReplicaSet, Job or CronJob |
| `$node` | The selected Node, or the node of the selected Pod when the list already holds it |
| `$__rate_interval`, `$__interval`, `$__range` | As for dashboards |

A variable without a value is refused before anything is sent, and the page
says which one. The page states what each variable stood for.

Errors are shown as Prometheus worded them (`NOT DRAWN: expression failed:
bad_data: 1:8: parse error …`), cleaned of control characters and bounded to
400 characters; nothing else of a failed answer is kept. An answer without
series says `no series matched`; when the typed text is one bare word, the
page also lists the named queries.

### Named queries

Shipped, for looking into a request that hangs:

| Name | Chart | Expression uses |
| --- | --- | --- |
| `throttled` | Share of CPU periods in which each Pod was throttled at its limit | `container_cpu_cfs_throttled_periods_total` / `container_cpu_cfs_periods_total` by `pod` (cAdvisor) |
| `ingress-499` | Share of requests the client gave up on, per Ingress of the namespace | `nginx_ingress_controller_requests{status="499"}` by `ingress` (ingress-nginx, with its `namespace` label) |
| `puma-queue` | Connections waiting for a free Puma thread, per Pod | `puma_queued_connections` by `pod` (GitLab webservice) |
| `dns-p99` | 99th percentile of DNS request duration, cluster-wide | `coredns_dns_request_duration_seconds_bucket` |
| `hubble-drops` | Dropped packets per second by reason, cluster-wide | `hubble_drop_total` by `reason` (Cilium Hubble metrics) |

The metric and label names are the defaults of those exporters. Nothing
checks that they exist: a query that matches nothing says so.

Your own, per context, in the context configuration file:

```yaml
k9s:
  observability:
    queries:
      slow-sql:                      # lower-case letters, digits, dashes; at most 32 queries
        title: Longest transaction
        expr: 'max by (datname) (pg_stat_activity_max_tx_duration{namespace="$namespace"})'
        unit: seconds                # seconds, ratio, perSecond, count, bytes or cores; optional
        legend: '{{datname}}'        # optional
```

A saved query with the name of a shipped one replaces it. A declaration that
cannot be used is reported when `:query` is run and nothing is asked. The `s`
page of a query shows the YAML that would save the expression on the page;
nothing writes it.

## Sizes and plain characters

At 80x24 a dashboard page holds one or two charts, or four value tiles and a
chart; at 60 columns one chart, or two value tiles side by side, with the
readout. A zoomed panel keeps the cursor line, the expression and the keys at
60 columns. With `ui.noIcons` the page uses plain characters: `-- title --`
for a section, `=` and `-` for a bar, the chart package's plain line glyphs,
`...` for a shortened name and `Left/Right`, `Up/Down` in the key line.

## Limits

- Queries go to the declared Prometheus source, not to the datasource the
  dashboard names. If the two differ, the numbers differ from Grafana's.
- `$__rate_interval` is not Grafana's value (see above), so a rate can be
  smoother here than in Grafana.
- Transformations, overrides, value mappings, per-series units, stacking,
  fill, right-hand axes, annotations, repeats (a repeated panel is drawn
  once) and panel links are not applied. Reducers other than those listed
  fall back to the last sample and say so.
- A query-backed variable is never refreshed: its saved value is used.
  Chained variables are therefore not re-evaluated for the selection.
- Thresholds are named, not interpreted; a dashboard without thresholds gets
  no word.
- The fixed step ladder applies (15s for 15m, 30s for 1h, 5m for 6h, 15m for
  24h); a panel's own `interval` or `maxDataPoints` is not used.
- An expression with a literal `$` after substitution is not sent.
- Only expressions of timeseries, graph, stat, gauge and bargauge panels are
  read; logs, traces and SQL are not.

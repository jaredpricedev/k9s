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

The page opens in the calm layout the incident views share
([presentation.md](presentation.md#calm-layout-the-standard-for-incident-views)):
what was read first, one panel at full size, and every other panel as one row
on the same time axis.

```
Dashboard  GitLab / Webservice overview · last 1h                    read 03:16 · 11 of 11 queries
  9 of 11 panels read. 2 cannot be drawn here.                           A reading, not a finding.

  REQUEST DURATION P99 BY POD  c …hx4mt 346ms peak 47.9s  a …2bq7k 346ms  b …8wzpd 346ms
                       50s ┤      ╭╮           ╭╮
                           │      ││           ││               │
                       25s ┤      ││           │╰╮              ├╮
                           │     ╭╯╰╮         ╭╯ │             ╭╯│
                           │     │  │         │  │             │ │
                         0 ┤─────╯──╰──────a──╯─b╰───c─────────╯─cba
  REQUESTS
  Requests per second       ██████▆▇███████████▆▆███████████████▇███  35.1/s
  Slowest pod p99           ▁▁▁▁▁▄██▃▁▁▁▁▁▁▁▁▁▄██▄▁▁▁▁▁▁▁▁▁▁▁▁▁▄▇▄▁▁  346ms green       max red
  Puma threads in use       ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  37.5% green
  CPU throttled, worst pod  ▁▁▁▁▁▄██▃▁▁▁▁▁▁▁▁▁▆██▅▁▁▁▁▁▁▁▁▁▁▁▁▁▄▇▅▁▁  3.93% green       max red
  Requests by pod           ▇▇▇████▇▇▇███▇▇▇▇███▇▇▇▇███▇▇▇████▇▇▇███  …2bq7k 12.8/s
  PUMA AND CPU
  Puma queued connections   ▁▁▁▁▁▆██▁▁▁▁▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁▁▁▁▁▁▆█▁▁▁  …hx4mt queued 0   peak 40
  CPU usage against limit   ▃▃▃▃▃▅██▅▃▃▃▃▃▃▃▃▃▆██▆▃▃▃▃▃▃▃▃▃▃▃▃▃▅█▆▃▃  …hx4mt 472m       peak 1.52
  CPU throttled periods     ▁▁▁▁▁▄██▃▁▁▁▁▁▁▁▁▁▆██▅▁▁▁▁▁▁▁▁▁▁▁▁▁▄▇▅▁▁  …hx4mt 3.93%      peak 88.7%
                            ─────────┬─────────┬─────────┬─────────┬
  UTC                              02:30     02:45     03:00     03:15
  NOT DRAWN  Pods and nodes (table) · Webservice log lines with timeout (logs)

  next  n next chart (Requests by pod)    Enter zoom    a all panels    D open in Grafana
  ↑↓ chart · ←→ time · 1-4 window · s queries · b dots · r re-read · Esc back
```

- **Title line.** The dashboard, the window, when it was read and how many
  of its queries were answered. The count is in the warning colour when not
  every query answered.
- **What was read.** One sentence counts the panels that are on the page as
  a chart or a row, and a second one everything that is not: `6 of 11 panels
  read, 1 only in part. 2 cannot be drawn here, 2 were not answered, 1
  matched no series (not a zero).` A dashboard shows; it finds nothing, and
  the sentence does not either: its caveat is `A reading, not a finding.`
- **One main chart.** The first line chart of the dashboard, at full size,
  with its legend on one line, the line with the highest peak first; what
  the line has no room for is counted (`+2 more`). A chart of more than four
  series draws the four with the highest peak and says so (`4 highest of 6
  series`); a line keeps the letter it has in the grid and in the zoomed
  panel. `n` (or `Tab`, `↓`) makes the next panel the main chart and the one
  that was a row; `Shift-N`, `Shift-Tab` and `↑` go back. Any drawn panel
  can be the main chart: a stat or gauge is drawn as the series behind it.
- **Every other panel is one row** on the time axis of the chart: its title,
  its strip and its number.
  - A `stat` or `gauge` of one series is its value with the author's
    threshold word. When the panel has a threshold that reads as a warning
    or a failure, the strip is drawn against it: the baseline (`▁`) where
    the value was under it, and a level in the row's colour where it was
    over. When the window held another step than the value does, the row
    says so beside the number: `max 47.9s red`, or `max red` where the
    screen has no room for the number.
  - Any other panel is its series with the highest peak (a level that never
    moves, such as a limit, stands for the panel only when nothing else was
    read): the name, the last sample, and the peak when it is more than
    twice the last sample. At 120 columns and more the row also says how
    many series it is not (`+5 more`). The strip is drawn from zero to that
    series' own highest sample, so the peak is its scale.
  - `×` in a strip is a cell without a sample. `partial` after a row marks
    a panel that was drawn from some of its queries; as the main chart, and
    zoomed, it names the query that is missing.
- **A dashboard row is a group**: its title in capitals over its panels.
  Panels under no row stand under `ALSO`.
- **What is not on the page** is one line per kind under the axis, with each
  panel's reason in a word or two: `NOT DRAWN` (the definition or the
  selection rules it out), `NOT READ` (its queries were not answered) and
  `NO SERIES MATCHED`. These lines are never left out, and `s` has every
  reason in full.
- **Two key lines**: the likely next steps in words, and the rest. `←` `→`
  move a time cursor through the chart and every row; the legend and the
  numbers then read the sample taken at that time, and the line over the
  chart says which (`at 04:29:00Z`). Only the numbers change: what stands
  beside a number (`peak 40`, `max 47.9s red`) is the window's and stays,
  and every number column is as wide as the widest number of the window, so
  the chart, the strips and the axis stay in the columns they have and a
  sample is read in one cell throughout. The cursor's first position
  (`Home`) is one step into the window: a sample is drawn over the step
  that ends at it, so the one at the window's start has no column on this
  page. `Enter` zooms the main chart.
- Nothing on this page is read for it: it is laid out from the one read the
  page already made.

### All detail: the grid of every panel (`a`)

`a` shows the dashboard as a grid of every panel, with each tile's minimum
and maximum, every legend entry, the variables and the expression of the
selected panel. `a` again, or `Esc`, returns to the calm page with the
selected panel as its main chart.

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
  37.5%  green                                  3.81%  green · max 87.7% red
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
←/→ ↑/↓ panel · Enter zoom · 1-4 window · r re-read · s queries · Shift-D Grafana · a calm layout · Esc
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

How each type is drawn in the grid of every panel (on the calm page each is
the main chart or one row, as above):

| Grafana type | Drawn as |
| --- | --- |
| `timeseries`, `graph` | A chart over the window. One series is a filled area of eighth blocks; several series are solid stepped lines, each ending in its letter, which also repeats along the line. `b` draws braille dots instead ([graphs.md](graphs.md#the-page)). The legend names each line by the panel's `legendFormat` (`{{label}}` is replaced) or, without one, by the labels that differ, and gives the line's last sample. |
| `stat`, `singlestat` | The value by the panel's reducer (`lastNotNull`, `last`, `first`, `mean`, `max`, `min`, `sum`), the threshold word, a sparkline of the window in eighth blocks (`×` for a cell without a sample) and the window's minimum and maximum. |
| `gauge` | The value, the threshold word, a thin bar against the panel's `min` and `max`, and the sparkline. When the window's maximum falls into another threshold step than the value, the value line adds it: `3.82%  green · max 87.7% red`. |
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
   two minutes, the range `:slow`, `:graph` and `:impact` take a rate over
   for the same step. Grafana derives it from the datasource's scrape interval,
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

On the calm page a panel that draws nothing is named in one of the lines
under the axis, with its reason in a word or two (`NOT DRAWN  Pods and nodes
(table)`); a line counts what it has no room to name. In the grid it takes
one line in its place, selectable like any panel: `Pods and nodes · NOT
DRAWN: panel type "table" is not drawn here`. The reasons are:

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
  keeps at most 80 panels and 40 variables. The calm page says how many
  panels are over the limit of one read, the status line of the grid counts
  what a limit cut, and `s` lists it.

`s` lists every panel with the expression that was sent and what came back,
every variable, and everything that was not drawn with its reason.

When the definition cannot be read (denied, absent, timeout, over 1 MiB, not
a dashboard), or no Prometheus source can be asked, or no query was
answered, the page says which source failed and how, draws nothing, and
points to `:sources`. Queries that failed in different ways are counted by
state (`Prometheus answered none of the 11 queries: 8 timeout, 3 not read`).
One read of a page has 45 seconds in all; a query that could not be sent
before that is `not read`, and `r` reads again. On such a page `Shift-D`
still offers the dashboard, or for a query page the expression in Explore.

### Keys

| Key | Action |
| --- | --- |
| `n` `Shift-N`, `Tab` `Shift-Tab`, `↓` `↑` | Calm page: the next or previous drawn panel becomes the main chart, round the dashboard. Grid: `Tab` `Shift-Tab` select the next or previous panel, `↑` `↓` the panel above or below |
| `←` `→` | Calm page: the time cursor, through the chart and every row (`Shift` moves ten points). Grid: the previous or next panel |
| `Home` `End` | Calm page: the window's start or end. Grid: the first or last panel |
| `Enter` | Zoom the main chart or the selected panel; `Esc` returns to where it was zoomed from |
| `a` | All detail: the grid of every panel. `a` again or `Esc` returns to the calm page |
| `1` `2` `3` `4` | Window: 15m, 1h, 6h, 24h (reads the panels once) |
| `r` | Read the panels again |
| `s` | Queries, variables and what was not drawn |
| `Shift-D` | Open the same dashboard in Grafana with the window drawn here and the variables the selection filled |
| `b` | Chart style: filled areas and solid lines (the default) or braille dots; the choice holds for the session and is the same in `:graph` and `:slow` |
| `Esc` | Back: out of the `s` page, the zoomed panel or the grid first, then out of the page |

In a zoomed panel `←` `→` move the cursor along the sample grid (`Shift`
moves ten points, `Home` and `End` go to the window's ends) and `↑` `↓` change
the panel. The legend then holds the samples at the cursor, never an
interpolation. Below the plot are the expression of each query as it was
sent, its state, series and sample counts, the panel's notes and the author's
description. A stat or gauge is zoomed as the series behind it.

## The query bar (`:query`)

| Typed | Result |
| --- | --- |
| `:query` | Every named query for the selection, laid out like a dashboard: the first at full size, the others as one row each under the name they are run by; `a` shows one chart each |
| `:query <name>` | That named query alone |
| `:query <expression>` | The expression alone; anything that is not a known name is taken as PromQL |

The page of one query:

```
Query  throttled · last 1h                                             read 02:17 · 1 of 1 queries

  3 series; highest …hx4mt, peak 90% at 01:27.                           A reading, not a finding.

  CPU THROTTLED SHARE BY POD  percent
       100% ┤
            │          ╭╮                   ╭─╮                         ╭╮
            │          ││                   │ │                         ││
            │         ╭╯╰╮                 ╭╯ ╰╮                        ││
        50% ┤         │  │                 │   │                        ││
            │         │  │                ╭╯   │                       ╭╯╰╮
            │         │  │                │    │                       │  │
            │        ╭╯  ╰╮               │    ╰╮                      │  │
            │        │    │               │     │                      │  │
          0 ┤────────╯────╰─a────b────c───╯─────╰─────────a────b────c──╯──╰─cba

  SERIES BY PEAK                                                                 last
  c  …hx4mt  ▁▁▁▁▁▁▁▁▅███▆▃▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▅████▆▃▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▃▆▇▆▄▁▁▁▁  4.17%  peak 90%
  b  …8wzpd  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  1.17%  peak 1.26%
  a  …2bq7k  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  1.16%  peak 1.26%
             ──┬──────────┬──────────┬──────────┬──────────┬──────────┬────────
  UTC        01:20      01:30      01:40      01:50      02:00      02:10

  next  e edit the expression    s queries, and how to save this one    D open in Grafana

  ←→ time · 1-4 window · a all detail · b dots · r re-read · Esc back
```

- The title line names the query, or the expression as it was typed. The
  sentence under it reads and does not judge: how many series came back,
  which stands highest and when (`1 series: p99, 6ms throughout.` for one
  that never moves). It adds how many series a limit left undrawn and
  whether the answer was cut.
- The chart is at full size. With several series, each is one row under it,
  the highest peak first, on the chart's time axis and on its scale: the
  letter that ends its line in the chart, its name, the last sample (the
  sample at the cursor once `←` `→` moved it: the word over the numbers
  then reads `at ^`, the caret on the axis, and the line over the chart has
  the time), its peak and its strip. The rows do not move with the cursor. A
  chart of more than four series draws the four with the highest peak; the
  rows list every series, and one that is not drawn has no letter.
- An answer without series is the sentence itself (`No series matched:
  missing data, not a zero.`), with the caveat `Nothing found is not proof
  of health.` and no axis; a rejected expression or a failed read is the
  sentence in the warning colour, with `Not read is not healthy.`
- The expression as it was sent, its state and sample counts, the variables
  and what the query is about are under `s`, with the YAML that would save
  the query. `a` shows the page as it was before the calm layout, with the
  cursor line, the expression, the description and the variables under the
  chart.

Names complete in the prompt (`:query thr` then `Tab`). In a query page `e`
puts the expression back into the prompt to change it; a `:query` typed while
a query page is open replaces what the page draws and keeps its selection.
`Shift-D` opens Grafana Explore with the expression as it was sent, when the
Grafana declaration has `publicURL` and `datasource`. The command is also
"Query: chart PromQL or a named query" in the `Ctrl-O` palette.

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

Shipped, for looking into a request that hangs. Each one is a signal of a
request-path profile ([request-path.md](request-path.md)): the reading
`:slow` makes of that hop, kept apart by one more label and written with
`$namespace`, `$pod` and `$__rate_interval` in place of the object and the
range. No query text is kept for them. The text is written from the profile
by the builder `:slow` uses, so a shipped query and `:slow` ask the same
metric with the same matchers, and their numbers agree for the same window.

| Name | Chart | Profile and signal | Kept apart by |
| --- | --- | --- | --- |
| `throttled` | Share of CPU periods in which a container was throttled at its limit | `container-cpu` / `throttled` | Pod |
| `ingress-499` | Requests per second the client closed while waiting (HTTP 499) | `ingress-nginx` / `499` | Ingress of the namespace |
| `puma-queue` | Connections waiting for a free Puma thread | `gitlab-webservice` / `queued` | Pod |
| `dns-p99` | 99th percentile of DNS request duration at CoreDNS, cluster-wide | `coredns` / `p99` | nothing |
| `hubble-drops` | Packets per second dropped on the way to the selected Pods | `cilium-hubble` / `drops` | Pod and reason |

The profile is the one the context reads that hop with by default, when it
has a signal of that name, else the one in the table: with
`requestPath.defaults.ingress: [ingress-nginx-exported]`, `ingress-499`
selects by `exported_namespace` as `:slow` does. A profile declared under the
same name (`extends`) changes the shipped query with it. A scope field that
`:query` has no variable for (the Service, the Ingress) does not narrow the
query. `hubble-drops` and the three per-Pod queries need a namespace; without
a selection they say which variable has no value, and `dns-p99` is still
drawn. The `About` line of a shipped query names its profile and signal.

Nothing checks that the metrics exist: a query that matches nothing says so.

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

The calm page gives up room in the order every calm page does
([presentation.md](presentation.md#what-a-smaller-screen-gives-up)), and a
dashboard adds three things of its own:

1. Under 120 columns a row no longer says how many series it is not, and of
   a maximum only the word: `max red` for a value whose window held another
   step, `peak 40` for a line that ends low. These are never left out of a
   row: `346ms green` alone would hide the stall.
2. The strips and the chart shorten together, because they share the axis.
   When they would fall under 20 columns, or a row would lose `max red` or
   its peak, the rows are not drawn narrower: every group folds to one line
   and the chart takes the width with its own axis. This is the page at 60
   columns. A folded line names first the panel whose window held the worst
   step of its author's rule, and of a panel first what its number would
   hide (`REQUESTS  Slowest pod p99 max red, 346ms green +4 more`), and
   counts the rest.
3. On a short page the first group keeps its rows and the groups under it
   fold; then rows are left out and counted, and the page ends as the chart
   alone with the lines of what was not drawn or read, which stay. A page
   with no room for the chart (ten rows or fewer for the dashboard above)
   shows the title, the sentence of what was read and the key for all
   detail.

At 80x24 the GitLab dashboard of the fixture shows its chart four rows high,
the five rows of its first group with strips of 30 columns, and its second
group and the undrawn panels as one line each; the second key line is given
up. The page of one query keeps its chart and every series row at 80x24 and
at 60 columns.

In the grid of every panel (`a`), at 80x24 a page holds one or two charts,
or four value tiles and a chart; at 60 columns one chart, or two value tiles
side by side, with the readout. A zoomed panel keeps the cursor line, the
expression and the keys at 60 columns.

With `ui.noIcons` the pages use plain characters. The calm page: `_` for the
baseline of a strip and `.:-=+*#` for its levels, `x` for a cell without a
sample, `:` and `^` for the cursor, `|` between the parts of a line, `...`
for a shortened name. The grid: `-- title --` for a section, `=` and `-`
for a bar, the chart package's plain line glyphs, and `Left/Right`,
`Up/Down` in the key line.

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
  24h); a panel's own `interval` or `maxDataPoints` is not used. One sample
  is read per step, so something shorter than the step can fall between two
  samples of a gauge and not show at all, and a rate over
  `$__rate_interval` (20m at 6h, 1h at 24h) flattens a short burst. A flat
  line in the 6h or 24h window is therefore not proof that nothing happened:
  a stall of a few minutes is looked for in the 15m and 1h windows. The `s`
  page states the step and the rate interval of the read.
- An expression with a literal `$` after substitution is not sent.
- Only expressions of timeseries, graph, stat, gauge and bargauge panels are
  read; logs, traces and SQL are not.

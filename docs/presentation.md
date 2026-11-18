# Presentation and compact terminals

k9+ uses neutral text for names and values, separate category colors for source identity, and health accents for status. Status words, readiness counts, restart counts and an ASCII `>` selection marker carry meaning even without color. Selected rows keep their status foreground on a subdued surface. Resource identity in the model remains complete when a displayed name is shortened.

![Actual 80×24 Pod table capture in stock true-color mode](evidence/ui-2026-10-04/pods-80x24-stock-true-color.png)

This actual PTY capture uses synthetic names and a disposable loopback API fixture. The [12-capture evidence index](evidence/ui-2026-10-04/README.md) includes all three skins, both terminal color modes, full destination panels, raw terminal text and the unchanged manifest. Its recorded binary hash is `a28a1f48b72d255348ad72bc4124d5973cc003a4070520c81743bd668aa57747`; the clean final capture was built from source commit `76ac522ff90ce3ddf25d320e911c55c7720b818a` and includes the queued-filter correction.

## Terminal size and actions

`k9s.ui.headerMode` supports `auto` (the default), `compact`, and `full`. Automatic mode displays compact chrome below 120 columns or 34 rows. `Ctrl+E` makes a manual choice that remains in force across resizes. The destination strip stays visible in both modes, in prompts, and while borderless detail/log views expand within the content area. `F2` reveals the complete destination. Compact hints retain complete primary labels and `Ctrl+O` opens searchable actions; `?` opens help. Omitting a hint does not remove its action.

Pod tables reserve full status, readiness and restarts before allocating space to names. Namespace is retained when viewing all namespaces. Metrics, age and node appear when there is room. Names shorten first, and width left over after the other columns are placed is returned to the name. `Ctrl+W` restores the complete column set for horizontal inspection, and resizing recovers the initial column position while preserving selected resource identity.

## Health summary and problems first

A resource list with a status column states how many rows need attention beside its row count, for example `pods(all)[6] 2 failing 1 degraded 1 in progress`. The counts use the same classification that colors the cells. A status word decides a row; a readiness count decides only when no status column speaks; restart counts are history and are not counted. Nothing is added when no row needs attention, and parts are dropped from the least severe when the frame is too narrow.

Failing, then degraded, then in-progress rows are listed ahead of the rest by default, keeping the chosen column order inside each group and the selected resource. The title states `problems first` while a list has rows needing attention. `Shift+X` switches every list to plain column order for the session, and back.

## Cause and trend

Pod lists add a `WHY` column: one phrase taken from the Pod status for a pod that is not running cleanly — the previous exit behind a back-off (`api: last OOMKilled exit 137`), a waiting or scheduling message, an eviction message, or the container that is running but not ready. It reports status evidence only and is empty for a healthy pod. The column is shown from 96 columns when at least one row has a cause; it claims a readable minimum, lets age and usage be placed, then grows into unused width. `Ctrl+W` always shows it in full.

### WHY for other workload kinds

The same column, with the same name, is on the Deployment, StatefulSet, DaemonSet, ReplicaSet, Node, Job, CronJob, PersistentVolumeClaim and Service lists. Each phrase is read from that one object's own spec and status in the list response that is already on screen; no events, pods or other objects are fetched for it.

| List | What WHY reports |
| --- | --- |
| Deployments | `ReplicaFailure` or a `Progressing` condition that is False, as `Reason: message` (`ProgressDeadlineExceeded: …`); a generation the controller has not observed; a paused rollout that is not fully updated (a paused Deployment with nothing left to roll out reads `paused` and is not counted); an `Available` condition that is not True; `rollout: 1 of 3 updated`; `1 of 3 unavailable`. |
| StatefulSets, DaemonSets, ReplicaSets | `ReplicaFailure`; an unobserved generation; rollout progress; replicas not created, not scheduled, misscheduled, unavailable or not ready, as counts. |
| Nodes | The `Ready` condition's message when it is False or Unknown (its reason when there is no message), `NetworkUnavailable`, each `…Pressure` condition that is True, and `cordoned`. |
| Jobs | The `Failed` or `FailureTarget` condition as `Reason: message`; `suspended`; `2 pod failures, backoff limit 6` while a job is still retrying. A completed job is empty. |
| CronJobs | `suspended`; `last scheduled run has no recorded success` when nothing is active and the recorded success is older than the last schedule. |
| PersistentVolumeClaims | `no volume bound` (and whether a storage class is set) while Pending, the lost volume name when Lost, and any condition that is True, such as a pending file system resize. |
| Services | `no load balancer ingress assigned` for type LoadBalancer with an empty ingress list. |

The renderer also gives the row a verdict of failing, degraded or in progress, which is what the title summary and problems-first order use for these rows. A Deployment whose rollout passed its deadline is therefore counted as failing although its readiness count alone would only read as degraded, and a failed Job is counted although the Job list has no status column. A phrase without a verdict (`suspended`, `paused`, `cordoned`) is stated but not counted. The verdict replaces the readiness count but never lowers what a visible status word already reports. The WHY text carries the meaning; its severity color and the color of the row's count cells follow the same verdict.

The column appears only while some listed row reports a cause, so healthy lists look as before. From 96 columns every column keeps its natural width and WHY takes what is left, up to 80 cells; if that is under 24 cells, a long name is elided to 24 and then optional columns are omitted from the right until it is, never name, namespace, status, readiness, Job completions or age. Below 96 columns, lists with a state column keep that column complete, elide the name down to 16 cells if needed, and show WHY when at least 18 cells remain. Lists without a state column (Jobs, CronJobs, Services) give up optional columns from the right in the same way, then the namespace, so a Job counted as failing still shows its evidence at 60 columns; if WHY still cannot have 18 cells it is left out and nothing is omitted for it. Elision is marked with an ellipsis. `Ctrl+W` shows every column unelided; the wide list is usually wider than the terminal, so scroll right with the arrow key to reach WHY.

Limits. WHY is evidence, not diagnosis: it repeats what the API object says and is empty when the object says nothing, which is not proof of health. Why a Deployment's pods fail is on the pods; why a CronJob run failed is on its Job; why a claim is Pending is usually only in events, which are not read here. A CronJob on a cluster that does not record `lastSuccessfulTime` reports nothing. Node conditions added by other agents are not interpreted. At 60 columns a Node list has no room for the column and shows only its status. The width budget is conservative, so a few cells can stay blank beside an elided phrase. Custom column views show WHY only if they include it. Counts in the title cover the rows currently listed.

While a list is open it remembers up to eight samples of each row's `CPU` and `MEM` values, taken at most every two seconds, and draws them as a sparkline before the number, scaled from zero to that row's maximum. `RESTARTS` shows the restarts gained since the list was opened as `+N`. This is session memory, not cluster history: it starts empty, is never stored, ignores unavailable values and is dropped when the resource leaves the list. With `noIcons`, the sparkline is replaced by `+`, `-` or `=` comparing the newest sample with the oldest. Sorting, filtering and export use the unchanged values.

## Next step line

When the selected row of a resource list reports a problem, the bottom edge of the table frame carries one line: `next:`, the status evidence decoded, then the checks that open more evidence, each with the key or typed command that this view really runs. For example `next: api: last exit 137: killed (SIGKILL), out of memory (OOMKilled) · p previous logs · :pressure limit vs usage`. A healthy selection leaves the border plain. The line is drawn on the frame, so it never moves a row and never touches the flash line.

The line is derived only from fields the list already holds (`STATUS`, `READY` and the pod `WHY` phrase); moving the selection reads nothing from the cluster. Exit codes are decoded by convention: 137 is SIGKILL ("out of memory" when the status says `OOMKilled`, otherwise "usually the memory limit or the node"), 143 is SIGTERM (a normal stop or a failed graceful shutdown), 139 is a segfault, 126 and 127 are a command that is not executable or not found in the image, 1 and 2 are the application's own error. Other signals are named by number and other codes are called application-defined. Scheduling guidance repeats which constraint the scheduler message names (resources, taints, affinity/selector, topology spread, volumes, cordoned nodes, host ports) and points at the message itself when it recognizes none. `:capacity` leads only when the message names resources. A Terminating pod shows no line. The wording is evidence plus a proposed check, never a diagnosis.

Covered states: pod exits and back-offs, image pull failures, container configuration and start errors, eviction, unschedulable and other Pending pods, a pod blocked while creating (only when the kubelet reports a message), running but not ready; a Pending or Lost PersistentVolumeClaim; a Deployment or StatefulSet with fewer ready replicas than desired; a NotReady node. Other resources and states show no line rather than a generic one.

Every key and command is checked against the action registry behind `Ctrl-O` for the selected row before it is printed. A check that is unbound in the view, unavailable for the selection, or disabled by read-only mode is left out; the evidence stays. The mapping itself only proposes reads (logs, describe, review commands).

When the line does not fit it drops the explanatory detail from the last check back, then the decoded wording in favour of the short evidence (`last exit 137 OOMKilled`), then trailing checks, so a narrower frame never hides a check that a wider one shows only to keep longer prose. If evidence and one check still do not fit, or the frame leaves fewer than 40 cells, nothing is shown; the line never wraps or clips. Width is measured in terminal cells. Evidence is bold in the warning or failure token and shortcuts are bold in the focus token, but the words carry the meaning; with `noIcons` the separator is `|` instead of `·`.

Limits: the guidance is only as current as the list row. It parses the `WHY` phrase, so a custom view that removes `STATUS` or `WHY` from pods loses the corresponding detail. It reports the first failing container, as `WHY` does. A deployment line is based on the ready count alone and does not read conditions such as `ProgressDeadlineExceeded`; `:rollout` shows those. There is no setting to turn the line off.

A Service, an Ingress or a workload that reports no problem keeps a plain border. The views that look into a hang (`:slow`, `:probe`) are opened on request from such a row, with `Shift-W` and `Shift-B` or from `Ctrl-O`; nothing on the list, in `:problems` or on this line points at them, because a quiet status is not evidence of a hang and the list does not read the metrics that would be.

## One keyboard for the incident and chart views

`:problems`, `:alerts`, `:timeline`, `:impact`, `:graph`, `:slow`, `:probe`, a drawn dashboard and `:query` are opened from one another. A key means one thing in all of them:

| Key | Meaning | Where |
| --- | --- | --- |
| `m` | `:graph`. From `:slow` it opens the comparison of the Pods, with the signal that set a Pod apart marked and the episodes found there as its bands | rows, `:problems`, `:alerts`, `:timeline`, `:slow`, `:probe` |
| `t` | `:timeline` | `:problems`, `:alerts`, `:impact`, `:slow`, `:probe` (on a Pod row of the resource list `t` is the upstream Transfer; type `:timeline` there) |
| `Shift-D` | Grafana hand-off for the selection and the window on screen | every one of them |
| `Shift-W` | `:slow` | rows, `:probe`, `:graph` |
| `Shift-B` | `:probe`; opening it sends nothing | rows, `:slow`, `:graph` |
| `1` to `4` | Window 15m, 1h, 6h, 24h (`:impact`: `1` to `5`, the width compared on each side of a rollout) | `:graph`, `:slow`, dashboards, `:query`, `:timeline` |
| `Left` / `Right`, with `Shift` ten steps, `Home` / `End` | The time cursor | `:graph`, `:slow`, a drawn dashboard and `:query` (in the grid of every panel under `a` they move between panels), a zoomed panel |
| `n` / `Shift-N` | Next / previous of what the view steps through: episodes in `:slow`, rollouts in `:impact`, the chart at full size in `:graph` and the marked signal on its per-Pod page (`Tab`, `↑`/`↓` do the same there). Where a view steps through nothing else, that is its chart: the next strip becomes the main chart | `:slow`, `:graph`, `:impact`; the next chart in a dashboard and `:query` |
| `Tab` / `Shift-Tab` | Next / previous main chart or panel. It is the next-chart key that is free in every one of these views, and the only one in `:slow` and `:impact`, where `n` steps through episodes and rollouts | a dashboard (next panel) today; bound to nothing else in `:slow`, `:probe`, `:graph`, `:impact` |
| `a` | All detail: every signal and the detail table the view had before the calm layout. One press shows it, the next returns to the calm layout, which is the default | every view laid out the calm way (see below); bound to nothing else in `:slow`, `:probe`, `:graph`, `:impact`, a dashboard |
| `r` | Read again. In `:probe` it is the one read there is: it sends the requests, after a confirmation | every one of them |
| `s` | The queries or sources behind the page. In `:impact`: the rule, the sample counts and the exact queries of the row under the cursor, and the full diff of the revision | `:graph`, `:slow`, `:impact`, dashboards, `:query`, `:timeline` |
| `p` | Compare the Pods of a workload or Service, and back | `:graph` only |
| `b` | Chart style: filled areas and solid lines (the default), or braille dots, which are finer and harder to read. One press switches, the next returns; the choice holds for the session in every view named here | `:graph` (overlaid, per Pod and regrouped), `:slow`, dashboards, `:query` |
| `+` / `_` | More / fewer requests per Pod | `:probe` only; the digits stay windows |

`TestHangViewsShareOneKeyboard` holds the views to this table, and `TestCalmKeysAreFreeOrMeanTheSameInEveryIncidentView` keeps `a`, `n` and `Tab` from being taken for anything else. The palette and `?` list each command once under Inspect and name the key that opens it on the same entry (`Slow requests: which hop (Shift-W)`).

## Calm layout: the standard for incident views

The incident views (`:slow`, `:graph` and its per-Pod page, `:probe`, `:impact`, a drawn dashboard, `:query`) answer one question each, and a page of theirs is laid out so that the answer and what it rests on can be told apart at a glance. Eight rules are the standard for every one of them. They are implemented once, in `internal/calm`, and a view that follows the standard builds its page from that kit instead of writing lines of its own. A view that has not been moved to it yet shows the page it had, and binds neither `a` nor the next-chart key.

1. **The answer comes first.** One or two plain sentences at the top say what was found; the first is bold. The evidential caveat (`Overlap in time, not proof of cause.`) is dim, at the right of the last line when it fits and on a line of its own when not. Everything below is evidence. The sentences state only what the data supports. When nothing was found they say how much was read, and when a source was not read they say exactly that: `calm.Nothing` and `calm.Unread` are the two forms.
2. **A thing and its timeline are one row.** Subject, what is measured, the one number that matters and its time strip are one line on a shared time axis. There is no table to match up by eye with a separate "when it happened" block.
3. **Episodes are bands.** A stall, a rollout or a fault window is a shaded band of columns running down through every row, and is marked on a marker row above the block (`▼`, `v` with `noIcons`) and drawn heavy on the axis below it (`━`, `=`). Colour is never the only signal: without a band colour the marker row and the axis carry the bands alone.
4. **One colour per subject.** The outlier Pod is one colour everywhere on the screen: its name, its number and what is over its rule in its strip. Quiet things are dim. Colours come from the semantic palette only, one token per role (see the table below).
5. **Rows are grouped by meaning.** A short label in capitals, a blank row before it: `LINES UP WITH THE STALLS`, `DIFFERS, BUT NOT DURING A STALL`, `QUIET`, `NOT READ`. A quiet thing is one dim line. Whitespace separates; there are no borders or boxes.
6. **One main chart.** A page has at most one chart at full size. Everything else is a one-row strip on the same axis, and one key makes the next strip the main chart.
7. **Two lines of keys at most.** A `next` line names the three or four likely actions in words; one dim line names the rest, ending with the way out. Both are written in the same words and the same order on every page (see [the same words on every page](#the-same-words-on-every-page)). The key hints the application draws at the top of the screen are not touched.
8. **Nothing is lost.** `a` shows all the detail the view had before: every signal, the detail table. `s` still shows the queries, the rules and the sample counts. The calm layout is the default.

An annotated page, as `:slow` reads at 104 columns (the reference page `calmtest.Slow`, invented data):

```text
Slow requests  Service gitlab-webservice-default · last 1h               read 14:57 · 35 of 35 queries   title: view, object, window; how much was read
  3 stalls: clients gave up waiting at 14:08, 14:27 and 14:53, for 2 to 4 minutes each.                 1  the answer, bold
  Each time, pod hx4mt was CPU-throttled and had no free threads. Nothing else lines up.                1  plain
  Overlap in time, not proof of cause.                                                                  1  the caveat, dim

                                                  stalls       ▼▼▼           ▼▼▼▼               ▼▼▼     3  marker row: the bands without colour

  LINES UP WITH THE STALLS                                                                              5  a group: what the verdict rests on
  ingress    clients gave up (499)  6.3%   of requests   ▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁▁▁████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁  2  subject, what, number, comparison, strip

> pod hx4mt  response time p99      47.9s  peers 0.35s   ▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁▁▁████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁  4  one colour for hx4mt; > is the selected row
             CPU throttled          88%    peers 1%      ▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁▁▁████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁     the subject is written once per run of rows
             free threads           0      of 4          ▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁▁▁████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁
             not ready              twice  up to 3m      ▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁

  DIFFERS, BUT NOT DURING A STALL                                                                       5  a secondary group: folds to one line first
  network    packets dropped        0.4/s  to pod 2bq7k  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁█▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁     its own colour: it is not the outlier

  QUIET                                                                                                 5  read, and nothing to show: one dim line each
  pods 2bq7k, 8wzpd            response time p99  0.35s  ·············································
  postgresql · redis · gitaly  latency, errors  normal   ·············································
  dns                          lookup time p99  6ms      ·············································
                                                         ┬─────━━━──┬────────━━┯━─────────┬─────━━━──┬  3  the shared axis: heavy under a band
  UTC                                                  14:00      14:15      14:30      14:45      15:00

  next  B probe the pods now    m compare the pods    Enter open hx4mt    D open in Grafana             7  the likely actions, in words
  ↑↓ row · ←→ time · 1-4 window · a show every signal · s queries · Esc back                            7, 8  the rest, dim; a is all detail
```

`:slow` is laid out this way; [slow-requests.md](slow-requests.md#the-page) shows the page a read draws, how its rows are sorted into the groups by overlap alone, and what it gives up at each size.

The band is the background of the columns under each `▼`, from the marker row down to the row above the axis: the three blocks of every row above stand inside it. `internal/calm/testdata` holds this page, the Pod graph and the Pod comparison as pinned text in the areas a view is given in terminals of 132×36, 80×24 and 60×24.

### The same words on every page

The pages are opened from one another during one incident, so a thing is written one way on all of them. A view takes these from one place and does not word them again:

| What | How it is written | Where it comes from |
| --- | --- | --- |
| A Pod | The last dash-separated word of its name: `hx4mt` for `gitlab-webservice-default-6f9c7d5b84-hx4mt`, in the answer, on its row, on the `next` line and under `a`. The ordinal of a StatefulSet Pod keeps the word before it (`gitaly-0`). The name stays whole when that word is shorter than three characters or a sibling ends in the same one: a short name never stands for two Pods | `calm.ShortName`; `:slow`, the Pod comparison, `:graph` and `:probe` call it |
| The object in the title | `Kind name`, with the namespace before the name only when it is not the one the session is in, which the first line of the screen states (`Service gitlab-webservice-default`; `Service gitlab/gitlab-webservice-default` from a session in `apps` or in all namespaces) | `calmObject` in `internal/view/calm.go` |
| A key on the `next` line | The key and what it does, in words. A capital letter is the key with `Shift`: `B probe the pods now`, `W slow requests by hop`, `D open in Grafana`. The menu at the top of the screen spells the chord (`<shift-b>`) | the `calmNext…` and `calm…KeyName` words in `internal/view/calm.go` |
| The dim key line | Always in this order: what `↑↓` move (`↑↓ row`, `↑↓ signal`, `↑↓ pod`, `↑↓ chart`), `←→ time`, the window (`1-4 window`; `1-5 window` in `:impact`, where the digits are the width compared on each side), `a` and what it shows in the view's words (`a show every signal`) unless it is on the `next` line, `s queries` unless it is there, the keys only this view has and its hand-offs (`n next stall`, `t timeline`, `D Grafana`), `r re-read`, `Esc back`. A narrow line drops parts from the right and keeps `Esc back`, the `a` part and the `s` part longest, so the key that shows everything stays named | `calmKeyLine` in `internal/view/calm.go`; `calm.RestLine` |
| Reading again | `r re-read` on the dim line, `r read again` on the `next` line of a page that could not be read, never both | the same |
| A change | `before → after` (`512Mi → 256Mi`), `->` with `noIcons` | the kit's plain words |
| The caveat | One short sentence that says what the answer does not claim: `Overlap in time, not proof of cause.` (`:slow`), `Order in time, not proof of cause.` (a Pod's graph, `:impact`), `A difference, not proof of cause.` (the Pod comparison, `:probe`), `A reading, not a finding.` (a dashboard, `:query`), `Nothing found is not proof of health.`, `Not read is not healthy.` | `calm.Nothing`, `calm.Unread` and the view |
| The marker row | The word of the episodes and `▼` over each: `stalls` in `:slow` and on the Pod comparison opened from it, the rollout in `:impact` | `calm.Bands` |

`TestCalmKeyLineIsWrittenInOneOrderInEveryIncidentView` and `TestCalmObjectNamesTheNamespaceOnlyWhenItIsAnotherOne` hold the views to this table, and `TestShortNameIsTheWordThatTellsAPodFromItsSiblings` the name.

The series of a drawn dashboard and of `:query` are the one exception to the short name: their names are legend text of any shape, cut to the room they have with the cut marked (`…hx4mt`), not Pod names the view knows.

### What a strip draws

Each glyph means one thing on every page:

| Glyph | `noIcons` | Meaning |
| --- | --- | --- |
| `▁` | `_` | sampled, and not over its rule: the quiet baseline |
| `▂` to `█` | `.:-=+*#` | over its rule; the height is the value, never lower than one step above the baseline, so it reads without colour |
| `▲` | `^` | an event, such as a restart |
| `·` | `.` | read, and nothing happened: event strips and quiet lines |
| `×` | `x` | no sample. Missing data is never drawn as quiet |
| `▼` | `v` | an episode, on the marker row |
| `┊` | `\|` | a labelled moment, such as a rollout; its key is on the axis and one line under it says what it was |
| `┆` and `^` | `:` and `^` | the cursor, through the rows and on the axis |

A sample stands for the step that ends at it, as the request-path rules state when a rule held (`slowpath.Crossings`). Strips, bands and the main chart are all drawn that way, so a strip that is over its rule during an episode stands inside the band, at any width. The clock labels are UTC and say so once.

### A page that counts instead of timing

`:probe` has no clock to share: its rows are requests, sent one after another. Such a page sets `calm.Page.Counted`, and its strips are `calm.Sequence`: one cell per thing, in order, from the left, so the first request to every Pod is the first cell of every row. The shared axis is the position, and no time axis is drawn.

| Cell | `noIcons` | Meaning |
| --- | --- | --- |
| `▁` to `█` | `_.:-=+*#` | a value, at its height on the scale all rows share; in the row's colour when it is over its rule, dim when not |
| a sign the view names (`×`, `!`, `~`, `?`, `·` in `:probe`) | the view's plain form | a thing without a value. It is in the row's colour only when the missing value is itself what stands out (a request without an answer) |
| `…` | `>` | the strip is narrower than its cells: the rest are cut, and the page counts them |

`Strip.Then` puts an earlier sequence on the same row, after a gap and dim throughout: the run before this one. A caption row over the first group names the two (`this run    previous 14:02:11Z`); one sequence needs no caption. A strip is as wide as its cells and captions need and no wider, so the number stands directly after it. A narrower screen first sets the cells side by side instead of a blank apart, lets the text beside the strip give way as on every page, and then leaves the earlier sequence out and names it in the `not shown at this size` line. The text never gives way for the blanks between cells.

Two more serve `:impact`, whose one episode is a moment and whose signals can get better by falling. `calm.Bands.Guide` draws an episode that has no length as a guide `┊` (`|`) through every row as well, in the colour of its marker: one shaded column is not seen, and a rollout divides every row in two. In a strip the guide stands in place of the level of its column, which holds samples of both sides; it never stands over an event or over a column that was not read. `Strip.Rule` gives a `Trend` a rule: the height stays the value in every column, in the row's colour where the rule holds and dim where it does not, so a signal that fell is high before the guide and low after it instead of a baseline.

Three smaller additions serve the same two views: `calm.Title.Verb` and `Unit` write the status of a page that sends instead of reading (`sent 14:02 · 15 of 15 requests`, in the warning colour when not everything was sent); `calm.Verdict.Apart` starts every sentence on a line of its own, for a second sentence that is a statement of its own kind (`Rev 2 changed the image, …`); and the group the cursor is in is given the primary role by the view, so the rows the keys act on are never folded away. `calm.Row.Aside` says that what stands beside the subject is where it is (the node and zone of a Pod), not what is measured: it is then dim on a coloured row too, so the colour of a row is its subject and its numbers.

### What a smaller screen gives up

Every calm page gives up room in the same order, and only as far as the screen needs. `calm.Lay` decides it and reports what it gave up; whatever is no longer on the screen at all is named in one line that starts with the key that shows it (`not shown at this size (a shows all): …`). A line too narrow for that says it in fewer words (`not shown (a shows all): …`) before it cuts the list, so that a 60-column screen still names the group it left rows out of.

1. The strips shorten. When they would fall under 20 columns the text beside them gives way: the text of the quiet lines is shortened, then the comparison beside the number (`peers 0.35s`) is left out, then what is measured is shortened and marked with `…`. The strips then narrow to 12 columns.
2. The blank rows around the verdict and the keys go.
3. The quiet and the not-read groups fold to one line each (`QUIET  pods 2bq7k, 8wzpd · dns`), under the axis.
4. The blank rows between subjects and before groups go; then the secondary groups fold to one line each; then the second key line goes.
5. The last blank rows go; then the last rows of the primary groups are left out and counted.
6. The folded lines of the quiet and secondary groups are left out. The line of a not-read group never is.
7. Below 12 columns of strip the strips are left out and the rows are written as text.
8. The primary groups are left out from the last: a page with a main chart ends as the chart alone on its axis, a page of rows as its first group. The `next` line is the last thing to go. A screen with no room even for that shows the title, as much of the verdict as fits, and the key for all detail.

A page whose rows are many may ask for two things within that order, and `:impact` asks for both ([impact.md](impact.md#in-an-80x24-terminal)). A secondary group that sets `calm.Group.FoldFirst` folds to one line before step 2, so the page keeps its blank rows longer than the rows the answer does not rest on. A page that sets `calm.Page.AnswerApart` gives up the last two blank rows of step 5 one at a time, the one over the keys first: with a single row to spare the answer is still set apart from the evidence, where the two rows otherwise go together and leave that row unused. Neither changes a page that does not ask for it.

An answer gives way before the evidence does, in its words and not in its rows: a view that has a shorter form of a sentence writes the fullest form that is one line at the width (`:slow` leaves the times of the stalls to the marker row, `:impact` the kinds of signal to the rows and the values of a changed limit to `s`). What is missing from a read is never in the part that is left out.

A view is given 80×21 in an 80×24 terminal: the application keeps two rows at the top and one at the bottom (and one more, 80×20, where it also draws its line of crumbs, as in the study fixture). At 80×21 the three reference pages keep every row and every strip. `:slow` folds `QUIET` to one line and gives up its blank rows; the Pod graph keeps its chart at five rows over its three strips; the Pod comparison gives up its blank rows and its second key line. At 60 columns `:slow` leaves the comparison column out and shortens what is measured, with strips of 20 columns, and the Pod comparison leaves the last peer row of two signals out and says so. Under 41 columns `:slow` has no room for strips: its rows are written as text, and the page says that the time strips are not shown.

`:graph` and its per-Pod page are laid out this way ([graphs.md](graphs.md#the-page)). In an 80×24 terminal `:graph` is given 20 rows: the strips shorten, the blank rows go and the chart gives rows up, down to three, so that restarts, CPU and network stay rows on the axis of the chart; at 60 columns, where the answer takes three lines, `ALSO` folds to one line that keeps every number; a screen shorter than that ends as the chart alone on its axis, with the guides of the moments folded into one line that is named when it is left out. The per-Pod page shows as many signals as groups as the size holds without giving anything up (three signals of three Pods at 132×36, one or two at 80×24) and names the others in `ALSO DIFFERS`; at 60 columns its strips shorten and nothing else changes. `NO DATA`, `NOT READ` and `NOT COMPARED` are never given up.

A drawn dashboard and `:query` are laid out by the same plan: the answer is how much was read (`9 of 11 panels read. 2 cannot be drawn here.`, with the caveat `A reading, not a finding.`), one panel is the main chart, every other panel is one row on its axis under its dashboard row as the group label, and what was not drawn, not read or matched no series is one line each that is never left out. A dashboard has no episodes, so it has no bands and no marker row; the cursor and the axis line its rows up. What it adds to the order above: under 120 columns a row keeps only the word of what its number would hide (`max red`, `peak 40`), and when the strips would fall under 20 columns or a row would lose that word, every group folds to one line and the chart takes the width (the page at 60 columns). [dashboards.md](dashboards.md#sizes-and-plain-characters) has both pages and their sizes.

### Colours

| Role | Semantic token | Used for |
| --- | --- | --- |
| text | `text` | names and values; the verdict |
| muted, dim | `muted` | labels and quiet rows; dim is the same token with the dim attribute: the caveat, the baseline, the second key line |
| accent | `focus` | the view name, a neutral group label, the selection marker |
| warn | `warning` | the episode markers and the axis under them, the word `next`, what lines up, a read that is not complete |
| fail | `failure` | the subject that stands out: the outlier Pod |
| info | `progress` | a subject that differs without lining up |
| mark | `category` | a labelled moment: the guide, its key, its legend |
| band | `band` | the background of the columns of an episode |

For a view: build a `calm.Page`, plan it with `calm.Lay(&page, width, height, calmOptions(app))` and draw `calm.Render(&page, &plan, opts).Lines()`. `internal/calm/calmtest` has the three reference pages to model one on. Text handed to the kit is plain; the kit escapes it and writes the tags, each naming foreground, background and attributes, because tview reads a tag as a whole. A layout change adds no request: the kit only draws what a view has already read. A time cursor moves and nothing else does: a view whose numbers are read under the cursor gives its group the widest number and note any sample of the window writes (`Group.NumberWidth`, `Group.CompareWidth`), so the columns after the strips, and with them every strip, band and mark, are the same wherever the cursor is. The cursor is drawn at every sample of the window, the first included.

## Semantic skin tokens

Existing skins remain supported. An optional `k9s.semantic` object can override `canvas`, `panel`, `text`, `muted`, `focus`, `healthy`, `warning`, `failure`, `progress`, `unknown`, `category`, `selected`, and `band`. These tokens also feed inherited prompts, dialogs, help, tables and charts. Runtime updates keep selection and stream state.

Open native dialogs update their bodies, fields, choices and focused buttons without resetting edits. Dismissal, replacement and view teardown release their listeners. The pinned terminal widget keeps native form border titles aqua; list dialog titles follow the skin.

Omitted tokens fall back to existing fields: canvas/text use body colors, panel uses dialog background, muted/unknown use completed status, focus uses focused border, healthy/warning/failure/progress use modify/pending/error/add status colors, category uses YAML key color, and selected uses the table cursor background. `band`, the surface behind the columns of an episode in the incident views, has no existing field: on the stock canvas and panel it is the stock band (`#16202a`), and a skin that changes either gets its own panel colour instead, so a band is never a stock colour on a canvas it was not made for. Explicit terminal-default colors remain owned by the terminal. On custom RGB surfaces, insufficiently contrasting foregrounds are adjusted toward black or white while retaining hue where possible.

The bundled `high-contrast` and `monochrome` skins provide additional presentations. Enable `k9s.ui.noIcons` for text-only identity and ASCII selection/mark/sort/change indicators. All presentations keep explicit status labels and action access.

Stock ordinary/selected text and repaired button, help and chart-focus pairs are tested against a 4.5:1 sRGB contrast benchmark, including 256-color quantization. The terminal controls font, palette and rendering; this benchmark is not a universal terminal accessibility certification.

## Render fixtures

The long-name Pod fixture checks 80×24, 100×30 and 120×34, selection across resizes, horizontal-scroll recovery, visible name elision, and stock/custom/high-contrast/monochrome skin changes. To export and render deterministic terminal cells:

```sh
K9S_RENDER_FIXTURE_DIR=/tmp/k9s-render-fixtures go test ./internal/ui -run TestCapturePresentationFixture
python scripts/render-presentation.py /tmp/k9s-render-fixtures
```

The export includes stock, high-contrast and monochrome presentations and runs only when that directory is explicitly configured. Each skin has separately named `true-color` and `256` JSON/PNG files; the latter approximates xterm palette quantization. These are simulation-screen fixtures, not live-cluster captures or human operator research. The renderer requires Pillow and fixed DejaVu Sans Mono font files. It adds no labels or UI text to the emitted cells.

To exercise actual terminal capability negotiation and the full destination dialog against a disposable local API fixture:

```sh
python scripts/skin-journeys.py --binary ./execs/k9plus --output /tmp/k9plus-skin-journeys
```

This helper uses `pyte` and Pillow, isolated application directories, read-only mode, no icons, and all three skins at 80×24. It explicitly enables true-color or disables it with `TCELL_TRUECOLOR=disable` for the 256-color run. Its manifest records the binary hash and each capture’s mode and source. Assertions wait for complete visible states; the PNG renderer uses only the app’s emitted terminal cells.

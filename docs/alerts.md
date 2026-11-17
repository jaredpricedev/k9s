# Alerts (`:alerts`)

`:alerts` (also **Alerts firing, tied to objects** in the `Ctrl-O` action
palette and in `?` Help) answers "what is paging, and which object is it
about?". It reads the Alertmanager that the current context declares
([observability-sources.md](observability-sources.md)) once when it opens and
again only when `r` is pressed. Nothing is polled, stored or written, and no
alert is read while browsing.

When the context declares no Alertmanager the command does not open; it says
so and points to `:sources`. Nothing is contacted.

## What the list shows

Each row is one alert, or several identical alerts folded into one:

- **SEVERITY** is the alert's `severity` label as a word, lower case. An alert
  with no severity label reads `unset`. The word carries the meaning; color only
  repeats it.
- **STATE** is `firing`, `silenced`, `inhibited` or `unprocessed`, as
  Alertmanager reports it. `firing` means the alert is active and not
  suppressed. It does not say that a notification was delivered.
- **ALERT** is the `alertname` label.
- **OBJECT** is the Kubernetes object the alert's labels name (see below).
- **FOR** is how long the alert has been firing at the time of the read, from
  the start time Alertmanager reports.
- **SUMMARY** is the `summary` annotation (or `message`, or `description`).

The list has up to three parts, in this order:

1. Firing alerts that name an object, by severity (words that usually page
   first), then most recently started first.
2. **not mapped to an object**: firing alerts whose labels name no object. They
   are listed, never dropped. A `Watchdog` alert normally lands here.
3. **silenced or inhibited: not notifying**: alerts Alertmanager holds but is
   not notifying about. They are dimmed, carry their state word, and sit under
   that heading so the state is readable without color and at any width. `z`
   hides or shows this part; the header then says how many are hidden.

Below 100 columns the summary column is dropped and kinds use their short
names. Below 72 columns the STATE and FOR columns are dropped too; severity,
alert name and object remain, and the headings still say which alerts are not
notifying. The pane under the list always states the selected alert in full
words.

## How an alert is tied to an object

Only the alert's labels are used. Nothing is guessed from a name pattern.

- A namespaced object needs a namespace label (`exported_namespace`, else
  `namespace`) and one of, in this order: `pod` (or `exported_pod`),
  `deployment`, `statefulset`, `daemonset`, `job_name`, `cronjob`,
  `persistentvolumeclaim`, `horizontalpodautoscaler`, `service` (or
  `exported_service`). The first one present wins, so an alert that carries
  both `pod` and `deployment` is about the pod.
- The plain `job` label is the Prometheus scrape job, not a Kubernetes Job. It
  is never used; only `job_name` names a Job.
- Without a namespaced object, a `node` (or `exported_node`) label names a
  Node. An `instance` label is not treated as a node name.
- A namespace label alone names the Namespace.
- Otherwise the alert is not mapped.

The detail pane names the labels the mapping used. The mapping is not checked
against what the alert rule measures: if a rule keeps the labels of the scrape
target instead of the subject (for example the `pod` of the exporter), the alert
is shown on that pod. That is a property of the rule, and the labels are shown
so it can be seen.

### The workload of an alerted pod

A pod alert is shown with the workload that controls the pod when that is
cheaply known:

- from a `deployment`, `statefulset`, `daemonset` or `job_name` label on the
  same alert, or
- from the pod's owner references. For alerted pods whose labels do not name
  a workload, the pod's metadata is read by name through a metadata-only client
  (no spec, no status), and a ReplicaSet is followed to its Deployment the same
  way. At most 30 pods are looked up per read, each read is limited to 3
  seconds and all of them to 5 seconds.

If a pod is not found at the read, the row says `(not found)` and the detail
says the pod may have been replaced. If the owner reads are denied, time out or
exceed the limit, the header carries a `NOT READ` line and those alerts are
listed by pod.

Identical alerts (same name, severity and state) on several pods of one
workload are folded into one row that names the workload and the number of
pods. `Space` (or `Right` / `Left`) expands and folds it. Pods are folded only
when their workload is known; an unmapped alert is never folded with another.

## Keys

| Key | Action |
| --- | --- |
| `Enter` | Open the mapped object in its normal list with it selected. `Esc` there returns to the alerts as they were read. On a folded row it opens the workload. |
| `t` | Open `:timeline` for the mapped object. A timeline follows a namespaced workload, so it is refused for a Node. |
| `d` | Every field of the selected alert as scrollable text: all labels, all annotations, start, update and end times, generator URL, runbook URL, receivers and fingerprint. |
| `c` | Copy the runbook URL to the clipboard (the generator URL when there is no runbook). The link is never opened or contacted. |
| `Space` | Expand or fold a row that folds several alerts. |
| `m` | Open `:graph` for the mapped object (Pod, workload or Node), when a Prometheus source is declared. |
| `Shift-D` | Grafana hand-off for the mapped object, starting shortly before the selected alert began. See [grafana-handoff.md](grafana-handoff.md). |
| `z` | Hide or show silenced and inhibited alerts. (`s` is "sources and limits" in `:timeline` and `:graph`, so it is not reused here.) |
| `/` | Filter. Every word must occur in the name, severity, state, object, workload, summary or a `label=value` pair. `Esc` clears it. |
| `r` | Read again. |

## Header and empty states

The header states the counts (firing by severity, silenced, inhibited, not
mapped), when the list was read and from which source. A third line appears
when something was not read: alerts beyond the limit of 500, or pod owners that
could not be read.

- **Source not read** (denied, timed out, unavailable): the header says
  `NOT READ` with the state and reason, and the pane says that what is firing
  is unknown. It is never shown as "0 firing".
- **Source answered with no alerts**: the pane says Alertmanager holds no
  alert at that moment and that this is what its rules reported, not a health
  check. A rule that does not exist cannot fire.
- **Filter matches nothing**, or **everything is hidden by `z`**: the pane
  says how many alerts are hidden and which key shows them.

## Alerts in `:problems`

When the context declares an Alertmanager, `:problems` reads its alerts once
beside its Kubernetes reads, inside the same total deadline. When no
Alertmanager is declared nothing is contacted and the header does not mention
alerts.

- Only alerts that name a node, or an object in the namespace being viewed,
  are used. Alerts that name no object stay in `:alerts`.
- A firing alert on a resource that is already listed adds one signal to that
  row and is named in its detail (`Alerts: name (severity, state age)`). An
  alert on a pod that is folded under a workload is named on the pod and on the
  workload. A pod that status reports healthy but whose workload is listed is
  named on the workload.
- A firing alert on an object that no row lists becomes its own row with the
  severity word `alerting`. Its cause is the alert, and its detail says that
  its own status, where it was read, reports no problem. This is the case
  status cannot show. A row that only a Warning event listed also becomes
  `alerting` when an alert is firing on it, with the alert leading the cause.
- An `alerting` row whose alert severity usually pages (`critical`, `page`,
  `error` and similar) is ranked after the failing rows; any other `alerting`
  row is ranked after the degraded rows.
- A silenced or inhibited alert is noted on a row that is already listed, with
  its state, but adds no signal and never lists a resource.
- When the source is declared but denied, timed out or unavailable, the
  `NOT READ` line names `alerts` with the state. No row changes.

## Alerts in `:timeline`

When the context declares an Alertmanager, `:timeline` reads its alerts once
beside its Kubernetes reads and places the start of each alert about the scope
as an `ALERT` entry: the selected workload, its pods, and the nodes those pods
run on; or, for a namespace timeline, every object in that namespace.

- A firing alert is a failing signal like any other, so it can be the
  `FAULT BEGAN` entry when it is the earliest one read. A silenced or inhibited
  alert is listed with its state and is not a failing signal.
- An alert entry is placed at the start time Alertmanager reports. A rule's
  `for` delay means the condition began before the alert did, and a resolved
  alert is no longer held and is not listed. Both limits are stated under `s`.
- When the source is not read, the `NOT READ` line names `alerts` and `s`
  shows the state and reason. Alert history is then unknown, not empty.

## Limits

- One `GET /api/v2/alerts` per read, through the shared bounded client: 8
  seconds, 1 MiB, at most 500 alerts kept. Alerts beyond that are counted and
  reported, not shown.
- Alertmanager holds alerts that are firing now. History of resolved alerts is
  not available from it.
- Existence is checked only for alerted pods whose owner is looked up. A
  Deployment, Service or Node named by an alert is not looked up; `Enter` opens
  its list, where a missing object is simply not there.
- A 403 from the Kubernetes API server (no permission to use the service
  proxy) and a 403 from Alertmanager itself both read `denied (HTTP 403)`.
- Generator and runbook links are shown and copied as text. URL userinfo is
  removed before a link is retained, and a link is never opened by k9+. The
  same removal applies to a URL written inside any label or annotation value
  (for example a probe's `instance` label), so `scheme://user:password@host`
  is kept as `scheme://host`. A credential placed anywhere else in alert text,
  such as a token in a query string, is not recognized and is shown as sent.
- In the detail pane a runbook link that does not fit beside the start time
  starts its own row. A link longer than the pane still wraps.
- The synthetic fixture (`scripts/operator-study-fixture.py`) serves ten
  alerts: the checkout-api error-rate, latency, crash-loop, rollout and
  pod-not-ready alerts, two node alerts on worker-b (one inhibited by the
  other), a silenced filesystem alert on worker-c and an unmapped `Watchdog`.

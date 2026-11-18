# On-demand resource investigation

PR #6 adds action discovery, resource troubleshooting and TLS investigation while
keeping the ordinary resource browser unchanged. All API reads are on demand.

## Actions

`:actions` opens a searchable menu for the selected resource. Type words in any
order, use arrows/Enter to run, Backspace to revise and Esc to return. Categories
identify built-in actions, plugins and configured shortcuts; dangerous actions
are labeled when their registrations identify them. Existing handlers retain
confirmation and read-only restrictions. The selected context/path are checked
again before invocation. No match is shown explicitly; it does not run an action.
The menu includes Troubleshoot and TLS where supported, plus existing log and
owner/reference navigation. No resource-list shortcuts were reassigned.

## Troubleshooting

`:troubleshoot` (alias `:ts`) opens a compact overview of the selected object's
current findings and affected containers. Current waiting states and previous
terminations are separate: a retained `OOMKilled` does not establish the cause of
a current `CrashLoopBackOff`. Identity, original capture time and snapshot age
remain visible while scrolling. The overview projects typed source records;
it does not infer findings from rendered report text or treat every False
condition as a fault.

Use **1–5** or **Tab / Shift-Tab** for Overview, Containers, Events, Resources and
Evidence. **6** explicitly chooses comparison baseline A. Each tab preserves its
own search and scroll position. **5** retains the full original report, messages,
owners and source limits; **m** opens that evidence and toggles message length.
Changing tabs, opening F2 destination details or returning from a related resource
does not make a new observation. Failed refreshes keep the previous observation
and its original identity and capture time, with an explicit error.

Deployment, DaemonSet, StatefulSet,
ReplicaSet and Job selectors also load up to 100 matching pods with current and
previous failure states. Empty selectors never expand to the namespace.
Selector matches are labeled as such; they are not asserted to be owned pods.

Up to 100 retained events are requested by the selected object's UID, sorted by
available event timestamps. Missing conditions/events do not establish health.
RBAC errors and truncated results are explicit. Child pod events are available by
jumping to the pod and inspecting it; the parent view does not perform an unbounded
per-pod event scan.

- **r** refreshes the snapshot (not an automatic watch).
- **g** lists owners, matching workload pods and reported references. Enter jumps
  to a resource; Esc returns to the preserved snapshot.
- **l** opens live logs for a selected Pod after checking its captured UID and
  context. For a workload, use **g** to choose its Pod first.
- Use existing actions for logs, describe and owner navigation after a jump.

Reads have a ten-second deadline and cancel on exit. Each inspector pins its
originating client transports; switching context cannot redirect a pending read
to the same resource name in another cluster. Stale callbacks never update another
screen. Failed API reads show explicit errors rather than a healthy empty result.

## TLS metadata and relationships

`:tls` supports Secrets, cert-manager Certificates, networking.k8s.io Ingresses
and Gateway API Gateways referencing TLS Secrets. Public certificate bundle
members show subject, issuer, validity dates, SANs and SHA256 fingerprints.
The inspector never displays or parses a private key.

**g** jumps to Secret/issuer/owner references. From a Secret it additionally scans
same-namespace Pods, Ingresses, Certificates and Gateways for configuration
references, bounded at 200 objects per type. Missing APIs/RBAC and truncation are
shown. Pod secret volumes, projected secret sources and container env references
are recognized. Cross-namespace consumers and controller-specific annotations are
not scanned. A configured/mounted Secret does not prove the endpoint serves its
current contents.

Cross-namespace Gateway Secret references are displayed but not automatically
fetched. Use controller conditions and ReferenceGrants to investigate acceptance;
this view does not claim a reference is authorized. Existing cert-manager
Issuance/References actions provide the issuance-chain navigation.

## Offline certificate trust verification

From a Secret's TLS inspector press **v** and provide the intended server name.
Choose system roots (default) or an explicit absolute local CA file. Alternatively,
from a selected Secret resource row:

```text
:tlsverify service.example.test
:tlsverify service.example.test ca=/absolute/path/ca.pem
```

The first certificate is treated as the server leaf; remaining bundle members are
candidate intermediates, never implicit trust anchors. Verification checks chain,
validity, server-auth usage and the requested hostname against the named trust
source. It does not inspect the private key or contact an endpoint. The k9plus
machine's clock and system roots are used, not a pod's trust store. Revocation is
not checked. CA files must be regular files, contain only CA certificate PEM
blocks, and fit within 1 MiB. Fields/paths with whitespace are not supported.

## Explicit TLS endpoint probe

Press **p** in a TLS inspector. The form states the execution location and requires
an explicit **Run TLS handshake** action. Opening/cancelling the form does no IO.
The equivalent command is:

```text
:tlsprobe example.com:443 example.com
:tlsprobe 127.0.0.1:9443 service.example.test ca=/absolute/path/ca.pem
```

The first argument is the TCP destination; the second is the identity to verify
(and SNI name for DNS names). This connects **from the machine running k9plus**, not
from a pod. It uses verified TLS, minimum version 1.2, with system roots or the
explicit CA file. There is no insecure mode or automatic plaintext downgrade.
No HTTP request is sent. Success reports peer address, protocol, cipher and public
certificate metadata; failure preserves the verification error and trust source.
**r** on a probe result explicitly repeats the handshake. No client certificate,
STARTTLS, revocation check, in-pod probe or automatic remediation is provided.

## Problems across kinds (`:problems`)

`:problems` (also "Problems across kinds, ranked" in the `Ctrl-O` action palette)
answers "what is broken here right now?" in one ranked list, with no setup. It is
the instant counterpart of the [daily workspace](daily-workspace.md): it needs no
saved scope, writes no file and keeps no history. When the context declares an
Alertmanager, its alerts are one more source of the same read: see
[alerts.md](alerts.md#alerts-in-problems). Undeclared, nothing is contacted and
the header does not mention alerts.

**What it reads.** The viewing destination: the active namespace, or all
namespaces when the app is viewing all. Opening the view performs one bounded
list per source — Pods, Deployments, ReplicaSets (only to resolve which
Deployment controls a Pod), StatefulSets, DaemonSets, Jobs,
PersistentVolumeClaims, Nodes (cluster-scoped) and Warning events from the last
hour. Each list is a single page of at most 500 objects with a 4 second timeout,
and the whole observation ends after 10 seconds. The reads run off the UI thread
against the client that was current when they started. `r` reads again on
request. Nothing is polled, watched or refreshed in the background, and the view
never writes to the cluster.

**What the header states.** The scope, the clock time of the read, what each
source returned, and a `NOT READ` line naming every source that was denied, timed
out, is not served, or was cut off at one page (`incomplete`). Rows from a
partial read are still listed. A quiet list says which sources were checked and
which were not; it never calls the destination healthy.

**Rows.** Each row has a severity word (`failing`, `degraded`, `in progress`, or
`warning` for a resource that only a recent Warning event names), the kind,
namespace/name, a cause phrase and, when the status carries a transition time,
how long the state has lasted at the time of the read (`FOR`; `-` when the
status does not say). Pod causes are the same STATUS word and `WHY` phrase as the
Pod list. Other kinds use their own status only, and say it in the same words
as the [`WHY` column](presentation.md) of their own list: a Deployment,
StatefulSet or DaemonSet leads with its available or ready count (this list has
no `READY` column) followed by its `WHY` phrase, unless that phrase would only
restate the count; a Job, a claim and a Node's conditions read exactly as in
their lists, with `NotReady` or `Ready unknown` standing in for a Node's
`STATUS`. When a status names a state but no reason (a `Pending` claim, for
example), the most frequent Warning event is quoted beside it and labelled
`event`. Rows are ordered by severity, then by the number of signals (status
findings, affected pods and distinct Warning reasons), shown as `SIG` from 100
columns.

**Folding.** Pods that report a problem are folded into the Deployment,
StatefulSet, DaemonSet or Job that controls them, matched by controller UID and
never by name. The owner's row says how many pods are affected; `Space` (or
Right/Left) expands and folds them. A pod whose owner could not be read or
verified stays on its own row. The failed pods left behind by earlier attempts
of a Job that now reports `Complete` are not listed: the Job itself has
superseded them.

**Keys.** `Enter` opens the resource in its ordinary list, using the same
navigation as related-resource jumps: the list is narrowed to that resource with
it selected, and `Esc` returns to the problems list exactly as it was read,
restoring the viewing namespace the jump changed. `d` describes, `l` opens logs
for a Pod row, `t` opens `:timeline` for the row's object, `m` opens `:graph`
for it ([graphs.md](graphs.md); refused with the reason when no Prometheus
source is declared or the kind has no graph), `Shift-D` is the Grafana hand-off
with a range that starts shortly before the time in the FOR column
([grafana-handoff.md](grafana-handoff.md)), `/` filters by words that must all occur in the severity, kind,
namespace, name or cause, and `Esc` clears a filter before it leaves the view.

**Narrow terminals.** From 100 columns all columns are shown. Below 100 the kind
is abbreviated and the signal count is dropped; below 72 only severity, name and
cause remain, the kind becomes a prefix of the name and the namespace is left to
the detail lines under the list, which always give the selected row's full
identity, complete cause and Warning events. Names are elided before the
severity word. With `noIcons` the expand markers are `+`, `-` and `|`. The
detail lines grow from three up to six (at most a third of the height under the
header) when a long cause would otherwise push the Warning events out of view;
a very long event message can still be cut there, and `d` shows it in full. When
the spelled-out key hints do not fit, the footer switches to shorter labels so
that `/ filter` and `r reread` stay on show at 80 and 60 columns. Lists of
sources wrap between sources, never between a source and its count.

**Limits.** Severity and cause are what each object's status and events reported
at the moment of the read; they are evidence, not a diagnosed root cause, and a
row's absence is not proof of health. Objects beyond the first 500 of a kind are
not classified. A node that is only cordoned, a Deployment paused with nothing
left to roll out and a suspended Job are not listed; a Deployment paused partway
through a rollout is listed as degraded, as in its own list. CronJobs, Services,
custom resources and metrics are not examined. A Warning event about an earlier
UID of a current name is listed separately as an earlier identity. Kinds named
only by an event and unknown to the view cannot be opened with `Enter`.

`scripts/operator-study-fixture.py` serves synthetic objects for every row type
(a stalled Deployment with two failing pods behind a ReplicaSet, a partly ready
StatefulSet, a failed Job, a Pending claim and Warning events) and denies this
view's cluster-wide DaemonSet read and `ops` events so the `NOT READ` line can
be seen; the DaemonSet list itself still opens.

## Timeline: what changed, and in what order

`:timeline` (also **Timeline: what changed, in order** in `Ctrl-O`) answers the
first question of an incident. When the context declares an Alertmanager, the
start of each alert about the scope is one more kind of entry
([alerts.md](alerts.md#alerts-in-timeline)). With a Pod, Deployment, ReplicaSet, StatefulSet,
DaemonSet or Job selected it follows that workload and what it depends on. With
nothing suitable selected it covers the active namespace, and `:timeline <ns>`
names a namespace directly. It needs one namespace: in all-namespaces mode with
no selection the action is listed as unavailable and says why, and
`:timeline all` is refused. For a namespace scope the namespace object is read
too: lists in a namespace that does not exist succeed with no items, so a
missing namespace is reported as not found instead of as a quiet timeline. When
the namespace object cannot be read (for example, access denied) the timeline
is still shown and the **s** page says its existence was not verified.

### The page

`:timeline` opens on a page laid out the calm way
([presentation.md](presentation.md#calm-layout-the-standard-for-incident-views)):
what happened in order first, then one row per kind of entry on one time axis,
and under the axis the entries the cursor is on. The list this view had before
is one key away (**a**), and **s** still shows sources and limits. This is the
page its tests pin for a 132×36 terminal (invented data; the test session is
in another namespace and declares neither Prometheus nor Grafana, so the
namespace is written and `m` and `D` are not offered):

```text
Timeline  Deployment apps/checkout-api · last 15m · 12 of 16 entries                                 read 12:00 · 11 of 11 sources
  Rev 2 rolled out at 11:48, after 1 other change.
  7 failing signals followed within 10m, the first after 2m: BackOff on pod x2x9q.              Order in time, not proof of cause.

                                         rev 2 rolled out 11:48              ▼
                                                                             ┊
  CHANGED                                                                    ┊
  config       checkout-config updated by…     11:46            ····▲········┊····················································
  rollouts     rev 2: image v1 → v2; env…      11:48            ·············▲····················································
                                                                             ┊
  THEN WENT WRONG                                                            ┊
> warnings     BackOff · pod x2x9q             11:50  14 times  ▁▁▁▁▁▁▁▁▁▁▁▁▁┊▁▁▁▁▁▁▁▁████████████████████████████████████████▁▁▁▁
  alerts       CheckoutHighErrorRate… +2       11:51  for 9m    ▁▁▁▁▁▁▁▁▁▁▁▁▁┊▁▁▁▁▁▁▁▁▁▁▁▁████████████████████████████████████████
  autoscaler   ScalingActive=False…            11:52  for 8m    ▁▁▁▁▁▁▁▁▁▁▁▁▁┊▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁████████████████████████████████████
  exits        api Error (exit 1) · pod x2x9q  11:58  6 times   ×××××××××××××××××××××××××××××××××××××××××××××××××××××××××▲········
  status       Progressing=False…              11:58  for 2m    ▁▁▁▁▁▁▁▁▁▁▁▁▁┊▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁█████████
                                                                             ┊
  ALSO IN THE WINDOW                                                         ┊
  annotations  ConfigMap apps/checkout-confi…  11:46            ····▲········┊····················································
  events       ScalingReplicaSet… +1           11:48  8 times   ▁▁▁▁▁▁▁▁▁▁▁▁▁┊▁▁▁█████████████████████████████████████████████▁▁▁▁
                                                                ────┬────────┯────────┬───────┬────────┬────────┬────────┬───────┬
  UTC                                                             11:46    11:48    11:50   11:52    11:54    11:56    11:58   12:00

  QUIET  scaling · nodes: none now

  WARNINGS  1 entry in the last 15m
> 11:50:00  pod/checkout-api-7d9f8c6b5d-x2x9q  Warning BackOff x14: Back-off restarting failed container api in pod
            checkout-api-7d9f8c6b5d-x2x9q
            First failing signal that could be read. The fault may have begun earlier than the evidence shows.
            Reported by: kubelet | First 2026-11-17 11:50:00Z, last 2026-11-17 11:59:00Z, 14 occurrences

  next  Enter open x2x9q    a full list
  ↑↓ row · ←→ time · 1-4 window, 0 all · s sources · n next entry · x mark · / filter · r re-read · Esc back
```

**The answer** counts and orders what was read and claims nothing else. Its
first sentence, in bold, is the moment the page starts from: `Rev 2 rolled out
at 11:48`, with the other changes of the window that preceded it. The second
says how many failing signals followed, within how long, and names the first.
What began in the second of that moment is said to have begun in the same
second, not to have followed: entries of one second have no known order.
`Order in time, not proof of cause.` stands dim at its right. When failing
signals began with no change before them in the window it says how many, when
the first began, and `No change was read in this window.` When no failing
signal followed a change it says `No failing signal was read after it.`, and
when nothing that was read began in the window it says that, and how many
entries **0** shows; the caveat of those two is `Nothing found is not proof
of health.` A read that fell short adds `Not read: nodes denied.` to
whichever answer it is.

**The moment the answer starts from** is the last rollout at or before the
first failing signal that began in the window, else the first change before
it. It is one marker (`▼`) over the strips with its words beside it, a guide
(`┊`) through every row, and a heavy tick on the axis, as the rollout is in
`:impact`. **x** makes the entry the cursor is on that moment instead (`your
mark 11:46`), and **x** on it again returns to the rollout: the earliest
warning in a namespace may belong to an older, unrelated problem.

**A row is one kind of entry.** It names the kind, what its first entry is
(`+2` when the row has two more), when that began, how often a repeated event
occurred, how often a container exited or for how long a state has held, and
its marks on the shared axis. The rows of a group are in the order they began.
What a row is about is cut to its column where the object it names begins or
between two words (`CheckoutHighErrorRate… +2`), and the count of the other
entries is never what gives way: it is on the row at every width and with
`noIcons`.

| Row | Entries | Drawn as |
| --- | --- | --- |
| `rollouts` | A Deployment revision, at the creation of its ReplicaSet, with what the pod template changed | a mark |
| `config` | The last write of each field manager to a referenced ConfigMap or Secret | a mark |
| `scaling` | The last scale time of an autoscaler | a mark |
| `warnings` | Warning events, merged by reason and kind | a span from the first to the last occurrence; a single occurrence is a mark |
| `exits` | The retained exit of a container that was not a clean completion, and beside it how often the container exited (`6 times`) | a mark; the stretch before it is not read (`×`) when the container restarted before that exit, because those restarts carry no time |
| `status` | An abnormal Deployment condition, an unschedulable Pod | a span from when it began to the capture |
| `autoscaler` | An autoscaler condition that fails | a span to the capture |
| `nodes` | An abnormal condition of a node the Pods run on | a span to the capture |
| `alerts` | An alert Alertmanager holds as firing | a span from its start to the capture |
| `events`, `clean exits`, `autoscaler ok`, `node conditions`, `muted alerts`, `annotations` | What is neither a change nor a failing signal: Normal events, clean exits, condition transitions that are not abnormal, silenced or inhibited alerts, Grafana annotations | marks; a repeated event, an annotated region and a held alert are spans |

A state that was read at the capture (a condition, an alert) holds from when it
began until then, so it is a span to the right edge of the axis. One that began
before the window and still holds is on the page too, although the list under
**a** counts only what began in the window: it does not vanish from a 15-minute
page because it began an hour ago. A span no wider than one column is drawn as
a mark.

**The groups** say how a row stands to that moment, by time alone:

- `CHANGED`: changes to desired state or configuration, in the colour of a
  change.
- `THEN WENT WRONG`: failing signals that began at or after it, in the colour
  of a failure. It is `WENT WRONG` when the window holds no change and no mark
  to start from.
- `ALREADY WRONG BEFORE IT`: failing signals that began before it. The answer
  counts them (`2 failing signals had begun before it.`), so that a change is
  never read as the start of what was already there.
- `ALSO IN THE WINDOW`: everything else, dim.
- `QUIET`: the kinds whose reads were complete and that have no entry in the
  window, as lines of names: the changes, the failing signals, the annotations.
  The kinds that are a state of the capture (`status`, `nodes`, `alerts`) are
  a line of their own, which says `none now; what ended is not kept` and is
  drawn as not read up to the column of the capture. Folded to one line the
  group says the same: `QUIET  scaling · nodes: none now`.
- `NOT READ`: every read that fell short, with its state and its reason
  (`nodes  denied: access denied…`) and the gap glyph all along. A kind that
  comes from such a read is never on a quiet line, and its row is drawn as not
  read (`×`) wherever it holds no mark: an alert that began and ended between
  two marks is not known not to have happened.
- `NO TIME`: failing status that carries no time and cannot be placed; **s**
  lists it.

**An empty column says that nothing still retained began there**, not that
nothing happened. Each source keeps its latest facts, not a history: the most
recent exit of a container, the last write of a field manager, the alerts
Alertmanager still holds. The quiet lines say `none retained` for that reason,
and **s** lists what each source keeps. Where the page knows that something is
missing it draws the stretch as not read (`×`) instead of as quiet:

- **A state of the capture that is not there now.** An abnormal status, a
  failing node condition and a firing alert are read as they stand at the
  capture. One that held ten minutes ago and ended leaves no entry: a Pod
  that `:slow` found not Ready twice in the hour is, to these sources, a Pod
  that is Ready. With none of them read the page knows the column of the
  capture and nothing before it, and the line says `none now; what ended is
  not kept`.
- **Restarts without a time.** A container status retains its last exit and
  counts the restarts before it. They happened before that exit, at moments
  nothing states, so the `exits` row draws the stretch before its mark as not
  read and writes the count beside it. After the retained exit the row is
  quiet: a later exit would have replaced it.
- **Expired events.** A window that reaches further back than Kubernetes
  commonly keeps events (an hour), or than the oldest event that was read
  when that is older, draws that stretch of an event row as not read, and a
  quiet `warnings` line then says `none since 11:00; older expired`.

A row that has a state (a firing alert, an abnormal condition) draws the
columns before it began as the baseline: that state was not the case then. It
does not say that no other state of the kind came and went before it.

**Under the axis** the page lists what the cursor is on, with the time, the
object and the text the list under **a** has:

- `↑` / `↓` select a row, and the list is the entries of that kind, oldest
  first.
- `←` / `→` (ten columns with `Shift`, `Home` / `End`) move a time cursor, and
  the list is everything drawn in its column, of every kind, with the kind
  named. The title states the time the column covers. A column in which
  nothing is drawn says so and names the entries before and after it.
- `n` / `Shift-N` step to the next or the previous entry in the order they
  began and move both cursors to it. Every entry of the window is reached this
  way, also one whose group is folded to one line.

The entry the keys act on is marked `>` and written in full where there is
room: the rest of a text that did not fit, how it stands to the first failing
signal or to the mark (`This change preceded the first failing signal by 4m.
Order is not cause.`), the detail the model carries, and the API field its
time came from. **Enter** opens its object, **m** its graph, **x** marks it.
The list has one height for a size of screen and neither cursor is drawn over
anything, so no row and no column moves when a cursor does. A list of four
rows or more gives its last row up for a blank row over the keys, so that the
`next` line never stands directly on an entry; a taller screen has both.

**The window** a read opens on is the narrowest of 15m, 1h, 6h and 24h that
holds the first failing signal and the change before it with a tenth to spare,
so that the page opens on what its answer is about instead of on every day
that was read. A change much older than the fault does not widen it; with
nothing recent it is an hour. **1** to **4** choose a window and **0** shows
everything read, from the oldest entry to the capture; over several days the
axis has a tick a day and names the day under each (`Nov12`).
A window the user chose stays through a re-read. The title counts the entries
drawn of those read (`12 of 16 entries`).

**Opened from `:slow`** (`t`) the page draws the stalls that were found there
as shaded bands through every row, marked `▼` on the marker row and heavy on
the axis, and opens on the window they were looked for in. The change is then
a labelled moment beside them, as a rollout is on a Pod's graph: its key `R`
on the axis, its guide through the rows, and one line that says what it was
(`R rev 2 rolled out 11:48 · image v1 → v2; …`). The stalls were read by
`:slow`; the timeline reads nothing for them. A window that reaches past the
hour they were looked for in says so on a `NOT READ` line (`stalls  :slow
looked for them 11:00–12:00 only`), and a window with no stall in it draws
none.

Keys, on the page's two lines in the words and the order of every incident
view:

| Key | Action |
| --- | --- |
| `Enter` | Open the object of the entry the cursor is on in its list; for an annotation, the dashboard it is attached to |
| `a` | The list this view had before, and back |
| `m` | `:graph` for that object, or for the timeline's own workload, over the timeline's window |
| `Shift-D` | The Grafana dashboard for the timeline's scope with the timeline's window |
| `↑` / `↓`, `←` / `→`, `n` / `Shift-N` | The row cursor, the time cursor, the next and previous entry |
| `1` to `4`, `0` | Window 15m, 1h, 6h, 24h; everything read |
| `s` | Sources and limits |
| `x` | Mark the entry as the moment the answer starts from; on the marked entry, where the key line reads `x clear mark`, take the mark away and return to the rollout |
| `/` | Filter by text. The rows and the list keep the entries that match; the answer stays about the window and says how many the filter left |
| `r` | Read again |
| `Esc` | Put the time cursor away, then clear the filter, then go back |

**a**, **m** and **D** are on the first key line, because a short screen gives
the second one up. What **Enter** opens gives way for them, as it does in
`:impact`: beside a name that would leave no room for the second action the
line reads `Enter open    a full list` and the list under the axis names the
entry, so the key that shows every entry is on the page at every size it is
drawn in. What the first line still has no room for is named on the second,
in the place every calm page has for it (`a full list` after the window,
`m graph` and `D Grafana` after the view's own keys).

Narrowing, filtering, marking, both cursors, **a** and **s** work on the
retained observation and read nothing.

What a smaller screen gives up, in the order of the calm layout:

- 132×36 gives up the blank rows around the answer and folds `QUIET` to one
  line, and the list under the axis takes the rows that frees: its heading,
  the entries, what it adds under the entry the keys act on, and a blank row
  over the keys.
- 80×24 keeps every row with a strip of 27 columns or more. How often or for
  how long leaves the rows (the `exits` row then draws its unread stretch
  without the count beside it; the list has it), `ALSO IN THE WINDOW` and
  `QUIET` are one line each, the second key line goes (`a` is on the first),
  and the list is one or two lines: the entry the keys act on, under its
  heading where there is a row for one. The count of entries leaves the title
  before the object or the window does. A quiet line whose words do not fit
  the column the lines share says them shorter (`none now`, `since 11:00`)
  instead of starting them elsewhere.
- 60×24 cuts what a row is about to 16 cells, with strips of 20 columns, and
  the list cuts the name of the object in its middle or leaves it out. The
  `next` line names `a` after `Enter open` where the name of the object would
  have filled it.
- The list never costs a row, a strip, a group or a key line. It folds the
  reasons of the reads that fell short to one line, which still names each
  read and its state, only when that is the only way to list anything; a page
  with no row to spare has no list, and **a** shows every entry.
- At 40 columns there is no room for a strip beside the text: the rows are
  written as text and the page names what it left out and the key that shows
  it. Width is measured in terminal cells; with `noIcons` every sign is a
  plain character (`v`, `|`, `^`, `#`, `x`).

### The list under a

**a** shows the list and the pane this view had before the calm layout, with
the entry the keys act on selected, and **a** again returns. It lists what
began in the window, newest first:

- Events, Warning and Normal, merged by type, reason and kind, with a count and
  the first and last time. A merged event is placed at its first retained
  occurrence, because that is when the condition began.
- Rollout history from the ReplicaSets a Deployment owns: creation time,
  revision, and what the pod template changed against the previous revision:
  image, environment keys added (`+`), removed (`-`) or changed (`~`), resource
  requests and limits, desired replicas, containers added or removed. Environment
  values and command lines are never printed.
- The retained exit of each container (reason, exit code, `finishedAt`).
- ConfigMaps and Secrets the pods or templates reference, from
  `metadata.managedFields`: the time and the field manager. They are read with a
  metadata-only request, so their values are not requested or shown.
- HorizontalPodAutoscaler condition transitions and the last scale time.
- Condition transitions of the nodes running the pods.
- Abnormal Deployment conditions and unschedulable pods, when they carry a time.
- Grafana annotations that name the scope by tag or text, as `ANNOT` entries,
  when Grafana is declared for the context. They are read in the same bounded
  read, are never the `FAULT BEGAN` entry, and an unreadable Grafana is stated
  on the sources line. See [grafana-handoff.md](grafana-handoff.md).

Every entry has its exact UTC time, its age at capture, its source and the
resource it concerns; the pane under the list shows these in full for the
selected entry, with the API field the time came from.

The first failing signal that could be read is marked (`▶`, or `>` with
`noIcons`) and labelled `FAULT BEGAN`. Failing signals are a Warning event, a
container exit that is not a clean completion, an abnormal condition. A rule is
drawn under it and every other entry is labelled as so long before or after.
Changes are marked `◆` (`*`), other failing signals `!`. A change listed under
the rule **preceded** the fault. That is an ordering. The view never says one
entry caused another. **x** makes the selected entry the reference moment
there too. (The mark was on **m** before `:graph` existed; **m** is now the
graph key in every view whose rows name an object.)

The pane under the list cannot scroll. Its first line (exact time, age, source,
resource) shortens its wording and then elides the middle of the name instead
of wrapping, and below about 72 columns the order sentence shortens to
"Preceded the first failing signal by 2m; order, not cause." so the full text
of the change stays visible. A very long event message or change list can
still be cut at the bottom of the pane. Below 72 columns the before/after
column becomes a signed span (`-4m`, `+8m`, `FAULT`) and the resource name
moves to the pane; the time, kind and text stay. Its key line names **a** at
every width.

### Limits

The view also states these under **s**:

- Reads happen when the view opens and on **r**, never on a refresh tick. There
  are five list reads (pods, events, ReplicaSets, Deployments, autoscalers; at
  most 500 events and 200 of each other kind, not paginated) and then at most 20
  ConfigMaps, 20 Secrets and 10 nodes. Each read has a 3s timeout inside a 12s
  total. A truncated list is reported as incomplete. The calm layout adds no
  read: it draws what this one observation holds.
- A source that was denied, timed out or failed is named on the page as not
  read. Its history is unknown, not empty. With no failing signal the page
  says so and adds that this is not proof of health.
- Kubernetes expires events, commonly after an hour. The view reports the oldest
  event it read and says older event history is unavailable; the page draws
  that stretch of its event rows as not read.
- Rollout times are ReplicaSet creation times. A rollback reuses an older
  ReplicaSet, so its time is not shown. Revisions pruned by
  `revisionHistoryLimit` are gone. StatefulSet and DaemonSet revisions are not
  read.
- Pod status keeps only the most recent exit per container, and `managedFields`
  keeps only the last write per manager. Earlier restarts and edits are not
  timed: the `exits` row draws the one exit that is, counts the others beside
  it and draws the stretch before it as not read. An earlier write by the
  same manager is not counted by its source, so the `config` row cannot show
  that one is missing.
- Conditions and alerts are read as they stand at the capture. What ended
  before it is not kept: with none now the page says so and draws nothing
  before the capture as quiet, and with one now it draws that one from when
  it began.
- Failing status that carries no time is listed under **s** and is not placed
  in the order. Entries in the same second have no known order; the page
  lists them in the order the model does.
- A row counts and draws entries; it does not say which Pod when a kind has
  entries of several. The list under the axis and **a** name each object.

`python3 scripts/operator-study-fixture.py` serves a synthetic incident for
this view: a ConfigMap edit, then a second Deployment revision, then a crash.

## Verification and operation

Focused tests cover generated PKI, wrong names, unknown roots, expired certificates,
bundle-root non-trust, cancellation and a real local TLS server that asserts zero
HTTP requests. Fake clients cover selector scope, empty selectors, permission
errors, truncation, context-pinned transports and cross-namespace non-fetching.
View tests cover literal markup and readable redraws. The iximiuz Hubble lab verified Deployment-to-pod jumps and snapshot refresh,
Ingress-to-Secret inspection, reverse consumer discovery, action no-match/Help,
offline correct/wrong hostname verification, and a verified TLS 1.3 handshake
against a disposable loopback server. Final form controls and cancellation were
checked in a dedicated test session. Certificate and Gateway CRDs were absent:
visibility errors were verified in the lab; their reference parsing is covered
by fake-client tests. Broader version/scale matrices remain unverified.

The compact daily-workspace overview has separate actual-PTY fixture checks at
80×24 and 120×34 for visible current/previous state, keyboard tabs, retained search,
F2 return and failed refresh. These use a disposable local API, not a live cluster.
The planned operator comprehension and usability study remains pending.

The functional smoke test used an isolated tmux session for capture and did not
repeat browser-rendering validation. The recommended direct launch remains the
user-verified workaround below. Disposable Ingress and TLS server were removed;
existing networking and user sessions were preserved.

In the iximiuz browser terminal, launch directly on **dev-machine**:

```sh
/home/laborant/k9plus-investigate -n hubble-test
```

The tmux/browser combination showed stale characters when opening the command
prompt; direct launch resolved this in user testing. Existing sessions and the
logging lab are preserved.

API references:
- [Go X.509 parsing and verification](https://pkg.go.dev/crypto/x509)
- [Go TLS dialer](https://pkg.go.dev/crypto/tls#Dialer)
- [Kubernetes TLS Secrets](https://kubernetes.io/docs/concepts/configuration/secret/#tls-secrets)
- [Gateway API TLS references](https://gateway-api.sigs.k8s.io/guides/user-guides/tls/)

See [ROADMAP.md](../ROADMAP.md) for the eight areas and remaining Hubble backlog.

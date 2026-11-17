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
saved scope, writes no file and keeps no history.

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
Pod list. Other kinds use their own status only: Deployment conditions and
available replicas, StatefulSet and DaemonSet ready counts, Job `Failed`
conditions and failed-pod counts, claim phase, and Node `Ready` and pressure
conditions. When a status names a state but no reason (a `Pending` claim, for
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
for a Pod row, `/` filters by words that must all occur in the severity, kind,
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
not classified. Cordoned nodes, paused Deployments, suspended Jobs, CronJobs,
custom resources and metrics are not examined. A Warning event about an earlier
UID of a current name is listed separately as an earlier identity. Kinds named
only by an event and unknown to the view cannot be opened with `Enter`.

`scripts/operator-study-fixture.py` serves synthetic objects for every row type
(a stalled Deployment with two failing pods behind a ReplicaSet, a partly ready
StatefulSet, a failed Job, a Pending claim and Warning events) and denies the
cluster-wide DaemonSet list and `ops` events so the `NOT READ` line can be seen.
## Timeline: what changed, and in what order

`:timeline` (also **Timeline: what changed, in order** in `Ctrl-O`) answers the
first question of an incident. With a Pod, Deployment, ReplicaSet, StatefulSet,
DaemonSet or Job selected it follows that workload and what it depends on. With
nothing suitable selected it covers the active namespace, and `:timeline <ns>`
names a namespace directly. It needs one namespace: in all-namespaces mode with
no selection the action is listed as unavailable and says why, and
`:timeline all` is refused. For a namespace scope the namespace object is read
too: lists in a namespace that does not exist succeed with no items, so a
missing namespace is reported as not found instead of as a quiet timeline. When
the namespace object cannot be read (for example, access denied) the timeline
is still shown and the **s** page says its existence was not verified.

The pane under the list cannot scroll. Its first line (exact time, age, source,
resource) shortens its wording and then elides the middle of the name instead
of wrapping, and below about 72 columns the order sentence shortens to
"Preceded the first failing signal by 2m; order, not cause." so the full text
of the change stays visible. A very long event message or change list can
still be cut at the bottom of the pane.

It merges into one list, newest first:

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

Every entry has its exact UTC time, its age at capture, its source and the
resource it concerns; the pane under the list shows these in full for the
selected entry, with the API field the time came from.

The first failing signal that could be read is marked (`▶`, or `>` with
`noIcons`) and labelled `FAULT BEGAN`. Failing signals are a Warning event, a
container exit that is not a clean completion, an abnormal condition. A rule is
drawn under it and every other entry is labelled as so long before or after.
Changes are marked `◆` (`*`), other failing signals `!`. A change listed under
the rule **preceded** the fault. That is an ordering. The view never says one
entry caused another, and the earliest warning in a namespace may belong to an
older, unrelated problem: **m** makes the selected entry the reference moment
instead, and **m** on it again returns to the first failing signal.

Keys: **Enter** opens the entry's resource in its list; **/** filters by text;
**1**, **2**, **3**, **4** narrow the window to the last 15m, 1h, 6h or 24h
before capture and **0** shows everything read; **s** shows sources and limits;
**r** reads again; **Esc** clears the filter, then goes back. Narrowing,
filtering and marking work on the retained observation and read nothing.

Limits, which the view also states under **s**:

- Reads happen when the view opens and on **r**, never on a refresh tick. There
  are five list reads (pods, events, ReplicaSets, Deployments, autoscalers; at
  most 500 events and 200 of each other kind, not paginated) and then at most 20
  ConfigMaps, 20 Secrets and 10 nodes. Each read has a 3s timeout inside a 12s
  total. A truncated list is reported as incomplete.
- A source that was denied, timed out or failed is named in the header as not
  read. Its history is unknown, not empty. With no failing signal the header
  says so and adds that this is not evidence of health.
- Kubernetes expires events, commonly after an hour. The view reports the oldest
  event it read and says older event history is unavailable.
- Rollout times are ReplicaSet creation times. A rollback reuses an older
  ReplicaSet, so its time is not shown. Revisions pruned by
  `revisionHistoryLimit` are gone. StatefulSet and DaemonSet revisions are not
  read.
- Pod status keeps only the most recent exit per container, and `managedFields`
  keeps only the last write per manager. Earlier restarts and edits are not timed.
- Failing status that carries no time is listed under **s** and is not placed
  in the order. Entries in the same second have no known order.
- Below 72 columns the before/after column becomes a signed span (`-4m`, `+8m`,
  `FAULT`) and the resource name moves to the detail pane; the time, kind and
  text stay.

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

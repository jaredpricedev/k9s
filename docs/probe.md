# Probe: time one read-only request to every Pod

`:probe` answers one question: **is one backend slow right now?** It sends a
small number of GET requests to each Pod behind a Service or workload, one
Pod next to the other, and shows how long each took. It is for the hang that
no status reports: nothing is crash-looping, the metrics are missing or too
coarse, and a page sometimes just waits.

It is the only command in k9+ that sends a request to an application. Every
other view reads the Kubernetes API or a declared observability source. For
that reason its rules are narrow and it asks before it sends.

## How to use it

Select a Service, Deployment, StatefulSet, DaemonSet, ReplicaSet or Pod and
press `Shift-B`, type `:probe`, or pick "Probe" in the action palette
(`Ctrl-O`).

Opening the view reads Kubernetes objects and sends nothing to any Pod. The
page is one block: "Nothing has been sent." in bold, then what `r` would
send and how often, that the application will see it, and the Pods it would
go to with the node and zone of each. The title says "nothing sent" until
you send something.

The view is laid out the calm way ([presentation.md](presentation.md#calm-layout-the-standard-for-incident-views)):
the answer first, one row per Pod, two lines of keys. `a` shows the table
and the result pane the view had before, with every column, every time and
every status code; `a` again returns to the calm page. `a` changes what is
drawn and nothing else: it sends nothing and the question before a request
is the same from either layout.

| Key | What it does |
| --- | --- |
| `r` | Run. The first time, and whenever the request changed, a dialog states what will be sent and waits for your answer. The same request runs again without a second question. |
| `+` / `_` | More / fewer requests per Pod for the next run, through 1, 3, 5 (default), 10, 20 and 30. The digits are not used: they are the window keys of `:slow`, `:graph` and the dashboards, and a probe has no window. `-` is the application's last-view key. |
| `u` | Switch between the Pods' declared readiness and liveness probe, when they declare both. Times for another path are not comparable, so the runs of one probe are put aside while the other is shown and come back when you switch back; the two are never shown together. |
| Up / Down, `j` / `k` | Move between Pods, in the order on screen: the Pods that stand out first. The first key line names the Pod the keys act on (`Enter open …-hx4mt`). With all detail (`a`) the line under the table states every time, the status codes, the node and zone and the declared probe of the Pod under the cursor. |
| `a` | All detail: the table with fastest, median, slowest, unanswered count, status codes, node and zone, the run before as a dimmer row under each Pod, every sentence of the result and the long form of what the time means. Press again for the calm page. |
| `Enter` | Open the Pod under the cursor in the Pod list. |
| `m` | Graph that Pod ([graphs.md](graphs.md)). |
| `t` | Timeline of that Pod. |
| `Shift-D` | Grafana hand-off for that Pod ([grafana-handoff.md](grafana-handoff.md)). |
| `Shift-W` | Open `:slow` ([slow-requests.md](slow-requests.md)) for the selection the probe was opened for, whichever Pod the cursor is on: the same key as on a Service row. From `:slow`, `Shift-B` opens this view. |
| `Esc` | Cancel a running probe. When nothing is running, go back. A request that was in flight at that moment has left: it is counted as sent, drawn as `?` and reported as abandoned, because the application may still handle it. Only requests that never left are "not sent". |

After a run, `r` runs again and keeps the run before it as a dimmer strip on
the same row, after this run's and named once over the rows (`this run
previous 14:02:11Z`), so that a Pod which was slow in one run and fast in
the next is visible as exactly that.

## What is sent, and what is never sent

- **Only a request the Pod declares for itself.** The path, the port and the
  scheme are those of the Pod's own `readinessProbe` or `livenessProbe`
  `httpGet`, the request the kubelet already makes every few seconds. Nothing
  is typed: there is no field for a path, a port or a header anywhere in the
  view. A named port is resolved against the ports of the same container.
- **Always GET, with no body and no header of its own.** Headers a probe
  declares (`httpHeaders`) are not read and not sent; the view says how many
  were left out. The API server removes your credential before it forwards
  the request, as it does for every proxied request.
- **A Pod without a declared httpGet probe is not contacted.** It is listed
  as "not contacted: no declared HTTP probe". The same holds for a probe that
  names its own `host`, a port name the container does not declare, a path
  that could not be sent as written, a Pod that is not Running and a Pod
  that is terminating; each is listed with its reason.
- **A declared path that could lead elsewhere is not sent.** A path with a
  `.` or `..` segment (written out or percent-encoded), a backslash, a
  fragment, a space or its own host is "a path that cannot be sent as
  written". A server or proxy that resolved such a path would turn the
  request into one for another API path, sent with your credentials; a Pod
  spec is not trusted to choose that.
- **Nothing leaves without a confirmation.** Before the first request the
  dialog states the request, the number of Pods, the number of times, the
  total, that it is an active request which the application handles and will
  see in its logs, the deadline and how to cancel. Cancel has the focus. A
  different count, path or set of Pods is a different request and is asked
  about again.
- **Nothing repeats on its own.** There is no interval and no refresh. A run
  happens when you press `r` and ends when its requests are answered,
  canceled or out of time. Leaving the view cancels a run in flight.
- **Nothing is changed.** The Pod proxy is used with GET only. The view has
  no write.

A query string in a declared path is sent as declared, because that is what
the kubelet sends, but its values are masked on screen (`/health?token=…`):
a probe path can carry a token.

## Limits

| Limit | Value |
| --- | --- |
| Pods contacted | 16, in name order. The rest are counted in the header and not listed. |
| Requests per Pod | 5 by default, at most 30 |
| Requests in flight | 4 Pods at a time; one Pod is always asked one request after another, 200ms apart |
| Deadline of one request | 5s. A request without an answer by then is counted as "over 5s". |
| Response body | At most 512 bytes are read, only to tell an API server error from an answer of the Pod, then the rest is dropped. No body is kept or shown. |
| Whole run | 5 minutes. What is unsent by then stays unsent and the result says so. |
| Building the plan | 25s in all; each Kubernetes read has its own deadline of a few seconds |

After the API server refuses `pods/proxy` once, nothing more is sent to any
Pod. A Pod the API server no longer knows is not asked again.

## What the time means

The request goes from the API server through its Pod proxy to the Pod. It
does **not** pass the ingress controller and it does **not** pass the
Service. So:

- A Pod that is slow here is slow on its own, on this path. The ingress and
  the Service cannot have slowed a request that never reached them.
- It locates a slow **Pod**, not a slow network path between clients and
  Pods. A hang in the ingress, in DNS or between nodes is not visible here.
- The proxy adds its own time to every answer. The view shows the fastest
  Pod's median as the **floor**: no reading can be lower than what the proxy
  path costs. Compare Pods with each other, not a number with the latency a
  user sees.
- The time is measured from the moment the request was written to the API
  server connection until the first byte of the answer arrived. Opening the
  connection is not part of it.
- A readiness path often does little work. A Pod that answers its readiness
  probe quickly can still be slow on real requests; a Pod that is slow even
  here is slow.

## Reading the result

The calm page states the result top to bottom:

```text
Probe  Service gitlab/gitlab-webservice-default · GET :8080/-/readiness ×5 per pod        sent 23:00 · 15 of 15 requests

  Only …-hx4mt answered slowly: median 2.71s against 23.9ms for its 2 peers; 1 of 5 had no answer within 5s.
  The other 2 Pods answered all 10 requests in 37.4ms or less.        A difference between Pods now, not proof of cause.

                                  this run    previous 23:00:29Z
  SLOWER THAN ITS PEERS                                           median
> …-hx4mt  worker-f study-zone-c  × ▅ ▇ ▄ ▆   ▄ ▆ ▇ ▅ ▆           2.71s   peers 23.9ms  OUTLIER

  LIKE THEIR PEERS
  …-2bq7k  worker-d study-zone-a  ▁ ▁ ▁ ▁ ▁   ▁ ▁ ▁ ▁ ▁           26.2ms
  …-8wzpd  worker-e study-zone-b  ▁ ▁ ▁ ▁ ▁   ▁ ▁ ▁ ▁ ▁           21.5ms

  MEASURED  API server → Pod, not ingress or Service: a slow Pod, not a slow path · full bar █ = 4s · floor 21.5ms (fastest Pod)

  next  Enter open …-hx4mt    m graph its CPU    r run again    W slow requests by hop

  ↑↓ pod · a every time and status code · +/_ ×5 per pod · u liveness probe · t timeline · m graph · D Grafana · Esc back
```

- **The answer** is the first sentence of the result, bold, and the one
  that qualifies it most: why a run ended early comes before how the other
  Pods answered. The caveat is dim at its end. While a probe is sending the
  first sentence says so and that nothing under it is final. Every sentence
  of the result is under `a`.
- **One row per Pod**: the end of its name, which tells it from its
  siblings, its node and zone, one cell per request in the order they were
  sent, its median, what the median is held against and its word. The run
  before is the dimmer strip on the same row.
- **Groups**: `SLOWER THAN ITS PEERS` (`OUTLIER`, `SPIKES`) and `NO ANSWER
  OR AN ERROR` (`NO ANSWER`, `TIMEOUTS`, `ERRORS`) come first and in their
  colour; `LIKE THEIR PEERS`, or `ANSWERED` when there are too few Pods to
  compare, are dim; `NO OUTCOME` holds Pods a canceled run sent nothing
  to or got nothing back from; `NOT CONTACTED` lists a Pod the plan sends
  nothing to with its reason; `NOT READ` is what the plan could not list
  (Pods beyond the limit, zones). While a probe is sending the Pods stay in
  one group, `ANSWERS SO FAR`, in the order of the plan, so no row moves
  under the eye.
- **`MEASURED`** is the one line that says what the time covers: the path,
  the scale of the strips and the floor, which is the median of the fastest Pod. It is never left out. The long
  form is under `a` and in [What the time means](#what-the-time-means).

What a smaller screen gives up, in order: in an 80x24 terminal nothing but
the blanks between the cells of two runs and what the median is held
against (`peers 23.9ms`); one run keeps both. At 60 columns the zone is
shortened, then the run before is left out and the page says so (`not shown
at this size (a shows all): previous 23:00:29Z`). With more Pods than rows
the last peers are left out and counted, never the Pod that stands out, and
the rows turn to keep the cursor on screen. Under about 44 columns the Pod
names shorten and the word goes; the group label still says it.

### With all detail

`a` shows the table. Each Pod has one row: a strip with one cell per request
in the order they were sent, the fastest, median and slowest answer, how
many got no answer within the deadline, the status codes, the node and
zone, and one word. The run before is a dimmer row under it.

The width is spent in a fixed order: the Pod, its median and the word always
remain; then the strip, the slowest answer, the unanswered count and the
node; then a blank between two bars of the strip, so that each request
stands alone; then the fastest answer and the status codes; then the zone
and the full Pod name. At 132 columns thirty requests keep their blanks and
the strip is headed "EACH REQUEST, IN ORDER (height = time to first byte)";
at 80 columns thirty stand side by side. Whatever a column leaves out is in
the line under the table for the Pod under the cursor: every time, every
status code, the node and the zone.

The strip uses one scale for every row, stated on the `MEASURED` line and in
the header of the table as "full bar █ = 4s". The height of a bar is the time to the first byte. A
request without an answer is drawn as `×`, one the API server could not
deliver as `!`, one that is in flight as `~`, one that was in flight when the
run was canceled as `?`, and one that was never sent as `·`; none of them is
a bar, because none of them is a time. With `noIcons` the bars are
`_.:-=+*#`, the cross is `x` and an unsent request is blank.

The "over 5s" column counts requests with an outcome ("1 of 5"). A request
in flight or abandoned was sent but has no outcome, so it is in neither
number. A Pod name that does not fit is cut at a dash and keeps its end.

| Word | Meaning |
| --- | --- |
| `OUTLIER` | The Pod's median is far above the medians of its peers. The result states both: "median 2.73s against 22ms for its 2 peers". |
| `SPIKES` | The median is like the peers', but its slowest request is far above their slowest: slow some of the time. |
| `TIMEOUTS` | Some of its requests got no answer within the deadline, and neither rule above singles it out. This is said of the Pod itself, not against its peers, so it also appears with one or two Pods and when every Pod has timeouts. |
| `ERRORS` | It answered, but not always with a 2xx or 3xx status, or some requests could not be delivered. |
| `NO ANSWER` | Requests were sent and none was answered. |
| `like peers` | Nothing separates it from its peers in this run. This is a comparison, not a statement that the Pod is healthy. |
| `answered` | It answered, and there are fewer than three Pods, which is too few to call one of them different. |
| `not sent` | Nothing has been sent to it yet. |
| `canceled` | Requests left for it and the run was canceled before any of them had an outcome. |
| `not contacted` | The plan sends nothing to this Pod; the reason follows. |

In the table a row that needs attention starts with `!`, and the selected
row with `>`. On the calm page the group label and the word carry it, so the
meaning does not depend on colour in either.

"Far above" is the peer-outlier rule of the chart package (median and median
absolute deviation, at least three Pods, at least twice the median of the group)
with an absolute floor: a median must also be 100ms above the peers, and a
slowest request 500ms above theirs. A few milliseconds between fast Pods are
never a difference. A request that timed out counts as the deadline, shown
as "≥5s" (`>=5s` with `noIcons`), which is a lower bound.

The rule compares each Pod with the median of the group, so it cannot name
one Pod when about half of them are slow. The result then says "The 4 Pods
did not answer alike", gives the range of the medians and leaves the
comparison to the rows; it does not say that they answered alike.

The result is a difference between Pods at the time of the run. It is not a
cause. Why that Pod is slow is a separate question: `m` shows its CPU against
its limit and `Enter` opens it. An intermittent stall can fall between five
requests; run again, or send more.

## Permissions

The probe needs `get` on `pods/proxy` in the namespace, in addition to the
reads the view makes to build its plan (the selected object, its Pods, and
for the zone column the Nodes).

```yaml
rules:
  - apiGroups: [""]
    resources: ["pods/proxy"]
    verbs: ["get"]
```

When the view opens it asks the API server, with a self subject access
review, whether you may do that. If the answer is no, the view says "Not
allowed", names the missing verb and resource, and `r` sends nothing. If the
review does not answer, a run is still possible; a 403 from the Pod proxy
then ends the run after the first request and names the same rule. If some
requests were answered before that refusal (a rule can be limited to Pods by
name), the result says how many and that the run is incomplete.

An API server refusal is recognised by its Kubernetes `Status` body. A 401
or 403 without one is counted as the Pod's own answer and shown under
`ERRORS`; an authenticating proxy in front of the API server can also send
such an answer, and the view cannot tell the two apart.

`pods/proxy` lets its holder send requests to any port of any Pod in the
namespace. k9+ uses it only as described above, but the permission itself is
broader; grant it deliberately.

Zones come from the Node label `topology.kubernetes.io/zone`. When Nodes
cannot be read the header says "zones not read" and the column stays empty;
that is unknown, not "no zone".

## How the Pods are found

- For a **Service**, the Pods are the backends that
  [`:network-review`](network-path-review.md) resolves for it, so the two
  views agree. Each Pod is then read again by name and is listed only if its
  UID is the one that was resolved. Because that collector is reused as it
  is, opening the probe on a Service also makes its reads of the Service's
  EndpointSlices and of the Ingresses, HTTPRoutes and NetworkPolicies of the
  namespace; nothing is done with the last three here.
- For a **Deployment**, the Pods are those controlled by ReplicaSets the
  Deployment controls. For a StatefulSet, DaemonSet or ReplicaSet they are
  the Pods it controls. A Pod that only carries the same labels is left out.
- For a **Pod**, it is that Pod. One Pod has no peers, and the result says
  so.

## Verification

The logic (which request a Pod declares, the plan and its limits, the order
and bounds of a run, the numbers, the outlier rule, the sentences, the
transport against a local HTTP server, the Kubernetes reads against a fake
client) is covered by `internal/probe/probe_test.go`. The calm page is covered by
`internal/view/probe_calm_test.go` and the table of all detail by
`internal/view/probe_test.go`, both with terminal-cell frames at 132, 120,
80 and 60 columns and with `noIcons`; the first also holds that `a` sends
nothing and that the question before a request is the same from either
layout. It was run against the synthetic loopback
fixture (`scripts/operator-study-fixture.py`, with and without
`--deny-pod-proxy`), whose third webservice Pod answers its readiness path
in seconds. It has not been run against a real cluster.

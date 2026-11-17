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

Opening the view reads Kubernetes objects and sends nothing to any Pod. It
lists the Pods, the node and zone of each, and the request that would be
sent. The header says "Nothing has been sent" until you send something.

| Key | What it does |
| --- | --- |
| `r` | Run. The first time, and whenever the request changed, a dialog states what will be sent and waits for your answer. The same request runs again without a second question. |
| `1` to `6` | Requests per Pod for the next run: 1, 3, 5 (default), 10, 20 or 30. |
| `u` | Switch between the Pods' declared readiness and liveness probe, when they declare both. Times for another path are not comparable, so the runs of one probe are put aside while the other is shown and come back when you switch back; the two are never shown together. |
| Up / Down, `j` / `k` | Move between Pods. The line under the table states every time, the status codes, the node and zone and the declared probe of the Pod under the cursor. |
| `Enter` | Open the Pod under the cursor in the Pod list. |
| `m` | Graph that Pod ([graphs.md](graphs.md)). |
| `t` | Timeline of that Pod. |
| `w` | Open `:slow` for the selection the probe was opened for, when the build has that view. Otherwise the key says so. |
| `Esc` | Cancel a running probe. When nothing is running, go back. A request that was in flight at that moment has left: it is counted as sent, drawn as `?` and reported as abandoned, because the application may still handle it. Only requests that never left are "not sent". |

After a run, `r` runs again and keeps the run before it as a dimmer row under
each Pod, so that a Pod which was slow in one run and fast in the next is
visible as exactly that.

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

Each Pod has one row: a strip with one cell per request in the order they
were sent, the fastest, median and slowest answer, how many got no answer
within the deadline, the status codes, the node and zone, and one word.

The strip uses one scale for every row, stated in the header as
"full bar █ = 4s". The height of a bar is the time to the first byte. A
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

A row that needs attention starts with `!`, and the selected row with `>`,
so the meaning does not depend on colour.

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
client) is covered by `internal/probe/probe_test.go`. The view is covered by
`internal/view/probe_test.go` with terminal-cell frames at 132, 120, 80 and
60 columns and with `noIcons`. It was run against the synthetic loopback
fixture (`scripts/operator-study-fixture.py`, with and without
`--deny-pod-proxy`), whose third webservice Pod answers its readiness path
in seconds. It has not been run against a real cluster.

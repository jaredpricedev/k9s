# Grafana hand-off

k9+ finds what is wrong; Grafana shows it over time. The hand-off takes you
from a selected object to the right Grafana page with its variables filled and
the time range set to the incident. It uses only the Grafana source declared
for the context ([observability-sources.md](observability-sources.md)).

There are three parts: a link for the selection (`Shift-D`), Grafana
annotations in `:timeline`, and a dashboard list (`:dashboards`).

## What it does and does not contact

- Building a link contacts nothing. A link is made from the declared
  `publicURL`, validated identifiers and escaped values.
- `:dashboards` makes one bounded dashboard search when it opens, and again
  only when you press `r`.
- `:timeline` reads annotations inside the same bounded read it already makes
  for the cluster, when it opens and when you press `r`.
- Nothing is read while browsing or on a refresh tick, and nothing is ever
  written: no annotation is created and the configuration is never edited.
- A link never contains a token. The token variable, the token and the
  `service` or `url` k9+ itself uses to reach Grafana are not inputs to a link.

## Open in Grafana: Shift-D

`Shift-D` is bound in the Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet,
Job, CronJob, Node and namespace lists and in `:timeline`. The same action is
**Open in Grafana** in the `Ctrl-O` palette and in `?` help for any view with a
selection; there it states the reason when it is unavailable.

| Selection | Dashboard kind | Variables filled |
| --- | --- | --- |
| Pod | `pod` | namespace, pod, and the node the pod runs on |
| Deployment, StatefulSet, DaemonSet, CronJob | `workload` | namespace, workload |
| ReplicaSet, Job | `workload` | namespace, and the owning Deployment or CronJob as workload when the cached object names one, otherwise its own name |
| Node | `node` | node |
| Namespace | `namespace` | namespace |
| Any other namespaced object (palette only) | `namespace` | its namespace; the dialog says the kind has no dashboard of its own |

Only variables the dashboard declares are sent, as `var-<name>`. A declared
variable with no value for this selection is left out, so the dashboard keeps
its own default, and the dialog names it as not filled.

### The time range

The range starts ten minutes before the moment the object's own status reports,
and ends at Grafana's `now`, so an open incident keeps moving. The moment comes
from the object already held by the list (no request is made):

- Pod: its start time when it has restarted more than once and started within
  the last 24 hours, because pod status keeps only the latest exit and every
  restart is after the start. Otherwise the earlier of the last container exit
  and the earliest condition that is False.
- Deployment: the last rollout progress (`Progressing.lastUpdateTime`) when it
  came within an hour before the fault, otherwise the fault (Available=False,
  Progressing=False or ReplicaFailure=True). With no fault, a rollout in the
  last 24 hours.
- Node: the earliest of Ready not True and any pressure condition that is True.
- Everything else, a healthy object, a condition without a time, or an object
  that is not in the list cache: the last hour.

The dialog always states the range in UTC and why it was chosen, for example
`05:41:32 to now UTC · 10m before the last container exit 05:51:32` or
`last hour: pod status reports no fault`. A moment older than seven days gives
the last seven days and says so.

From `:timeline` the range is the timeline's own window, ending at its capture
time instead of `now`, so the dashboard shows what the timeline shows. A view
with its own window can do the same by implementing `GrafanaWindow`.

### Delivery

In this order:

1. The system browser is asked to open the link (`xdg-open`, `open` on macOS,
   the URL handler on Windows) when all of these hold: the app is not in
   read-only mode, this is not an SSH session, a display exists (`DISPLAY` or
   `WAYLAND_DISPLAY` on Linux), the opener is installed, and
   `K9PLUS_BROWSER` is not `off`. The link is passed as one argument, never
   through a shell, and only an `http` or `https` link without credentials is
   ever handed over.
2. Otherwise the link is copied: to the native clipboard, and if that is not
   reachable to the terminal's clipboard with OSC 52, which also works over
   SSH. `K9PLUS_CLIPBOARD=native|osc52` narrows this as for other copies.
3. In every case a small dialog shows what was done, why the browser was not
   launched if it was not, and the whole link, cut exactly at the dialog's
   width with no indentation so selecting the rows yields the link.

Read-only mode starts no local program, so there the link is copied. OSC 52
cannot be confirmed: the terminal decides whether to accept it (tmux needs
`allow-passthrough`), which is why the link is always shown as well. "Asked
the system to open it" means the opener started, not that a page loaded.

Dialog keys: `o` open in the browser, `c` copy, `e` Explore metrics, `l`
Explore logs, `d` back to the dashboard, `a` the full dashboard list, `Esc`
close.

### Explore links

`e` opens Grafana Explore on the declared Prometheus datasource (`datasource`)
with one query for the selection, and `l` opens it on the declared Loki
datasource (`lokiDatasource`) with a stream selector. Both carry the same time
range. Links use the Explore `panes` format (Grafana 10 and later).

| Selection | Metrics query | Log selector |
| --- | --- | --- |
| Pod | `container_memory_working_set_bytes{namespace,pod}` | `{namespace="…",pod="…"}` |
| Deployment / StatefulSet / DaemonSet | available or ready replicas from kube-state-metrics | `{namespace="…",pod=~"<name>-.+"}` |
| Other workloads | restarts by pod for pods named `<name>-…` | the same |
| Node | `kube_node_status_condition{node,status="true"}` | not offered: node log labels differ between installations |
| Namespace | restarts by pod in the namespace | `{namespace="…"}` |

The metric and label names are the kube-state-metrics, cAdvisor and common
Loki defaults. k9+ does not check that they exist in your installation; the
query is a starting point to edit in Explore. Queries are built by the typed
builder, so a name with quotes or regex characters cannot change the query.
The Loki datasource is never queried by k9+.

### When no dashboard is declared for the kind

`Shift-D` opens `:dashboards` instead, with a notice. Choose a dashboard and
press `Enter`: the link opens without variables, because the variable names of
an undeclared dashboard are unknown, and the dialog shows the YAML that
declares it for that kind, with the path of the context configuration file.
`y` copies the YAML. The proposed variable names are the usual ones
(`namespace`, `pod`, `workload`, `node`); check them against the dashboard.
Nothing is written for you.

## Annotations in the timeline

When Grafana is declared, `:timeline` reads the annotations of the 24 hours
before capture (one GET, at most 200, eight-second timeout) in the same read as
the cluster. Deploy markers, alert state changes and manual notes appear as
entries of kind `ANNOT` with `grafana/<dashboard uid>` as their resource. An
alert state change reads `Alert <name>: <previous> to <new>`. The pane under
the list shows the tags, how the annotation was matched, and for a region its
end.

An annotation is shown only when it names the timeline's scope, by tag or by
text:

- Workload scope: a tag equal to the workload's name or of the form
  `key:name` / `key=name`, or the name as a whole word in the text. For a Pod
  the names of its controllers found by the cluster read count too.
- Namespace scope: a tag `namespace:<ns>` (also `ns` or
  `kubernetes_namespace`, with `:` or `=`), `<ns>/` or `namespace=<ns>` in the
  text, or a `node:` / `instance:` tag for a node already in the timeline.
- An annotation tagged for another namespace is never shown.

Annotations that match nothing are counted, not shown; the `s` page states how
many were read, matched and left out. Matching uses tags and text only. Nothing
is inferred from time, so an untagged deploy marker is not attributed to the
workload.

An annotation is what its author wrote down. It is never the `FAULT BEGAN`
entry and is not marked as a change; it is labelled before or after the fault
like any other entry, which is order, not cause. `Enter` on an annotation that
is attached to a dashboard hands off to that dashboard, fifteen minutes either
side of the annotation.

If the read fails, the timeline's sources line says so (for example
`NOT READ: grafana annotations denied`) and the `s` page gives the state and
reason and says the annotations are unknown, not absent. If Grafana is not
declared, the `s` page says no annotations were read and points to `:sources`;
that is not counted as a gap, because nothing was asked.

## :dashboards

`:dashboards` (also **Grafana dashboards for the selection** in `Ctrl-O`)
lists, for the selection or the active namespace:

1. `declared`: the dashboard declared for the selection's kind, then the other
   declared dashboards, each with the variables it can fill.
2. `explore`: the metrics and log Explore links, or why one is unavailable.
3. `search`: dashboards Grafana's search returned that are not declared. Those
   sharing a word with the selection (its kind, namespace or name parts) come
   first; that ordering claims nothing about the dashboard.

The search is one GET of `/api/search` limited to 100 dashboards.
`:dashboards <words>` sends the words as Grafana's title query, which is the
way to narrow a Grafana with more dashboards than the limit; the header says
`INCOMPLETE` when the limit was reached. `/` filters the rows already read.
`Enter` opens or copies the selected link as above and shows the dialog; for a
`search` row it also shows the YAML that pins it. `r` reads the search again.

If the search is denied, times out or cannot be reached, the header says
`NOT READ` with the state and reason and points to `:sources`; the declared
dashboards and Explore links still work, because they need no read.

## When Grafana is not usable

| Situation | What you see |
| --- | --- |
| Grafana not declared | A dialog: not declared for this context, `:sources` shows how to add it (`s` opens it). Palette entries carry the same reason. |
| Declared without `publicURL` | A dialog: no link can be built without the address your browser uses. |
| Declaration invalid | A dialog with the validation reason. |
| No dashboard for the kind | `:dashboards` opens as a chooser. |
| No `datasource` / `lokiDatasource` | The Explore link names the setting to add. |
| Search or annotations denied, timed out, unreachable | The view's own status line says `NOT READ` and why, and points to `:sources`. |

## Configuration added by this feature

```yaml
k9s:
  observability:
    grafana:
      lokiDatasource: loki-main   # Grafana UID of a Loki datasource; links only
```

Everything else is the existing declaration: `publicURL`, `datasource` and
`dashboards`.

## Limits

- Links target `/d/<uid>` and Explore `panes` with `schemaVersion=1`. An
  Explore link needs Grafana 10 or later.
- Variable values are set as given. A dashboard whose variable expects
  something else (an instance with a port, a cluster-qualified name) needs a
  dashboard that accepts the Kubernetes name.
- The workload a Pod belongs to is not filled on a `pod` or `workload`
  dashboard link built from a Pod; open the workload itself for that.
- StatefulSet, DaemonSet, Job and CronJob status carries no fault time, so
  their range is the last hour.
- The incident range is a starting point from one object's status. It is not a
  claim about when the fault began; `:timeline` is the view for that.
- Organization selection (`orgId`) is not set; Grafana uses your current one.
- k9+ cannot know whether the browser or the terminal accepted the link.

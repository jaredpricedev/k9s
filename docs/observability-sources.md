# Declared observability sources

k9+ can read Prometheus, Alertmanager and Grafana for the current context, but
only sources you have declared, and only when you open a feature that needs
one. This page describes the declaration, the two ways a source is reached, the
limits on every read, and what is never done.

## What is never done

- Nothing is discovered or contacted in the background. Browsing, refreshing
  and opening `:sources` send no request to any source. A journal of the
  synthetic fixture shows zero service-proxy requests until a feature is
  opened or `p` is pressed in `:sources`.
- Nothing is written: every request is a GET, and k9+ never edits the
  declaration for you. A suggestion is text to copy.
- No token, Authorization header, URL userinfo or response body is stored,
  logged or shown. A token is named by the environment variable that holds it;
  the value is read for one request and never kept.
- Redirects are not followed, and a direct URL source does not inherit a proxy
  from the environment.
- A source that answers is not evidence that a workload is healthy. An empty
  query result or an empty alert list is missing data, not a healthy reading.

## Declaration

Sources are declared per context in the context configuration file
(`<config dir>/clusters/<cluster>/<context>/config.yaml`, the file that already
holds the namespace and view settings for that context). `:sources` shows the
exact path. All three sources are optional.

```yaml
k9s:
  observability:
    prometheus:
      service: monitoring/prometheus-operated:9090
    alertmanager:
      service: monitoring/alertmanager-operated:9093
    grafana:
      url: https://grafana.example.com
      tokenEnv: GRAFANA_TOKEN          # variable NAME, never the token
      publicURL: https://grafana.example.com
      datasource: prom-main            # Grafana UID of its Prometheus datasource
      lokiDatasource: loki-main        # Grafana UID of a Loki datasource; links only
      dashboards:
        pod:       {uid: k8s-pod, namespaceVar: namespace, podVar: pod}
        workload:  {uid: k8s-workload, namespaceVar: namespace, workloadVar: workload}
        node:      {uid: k8s-node, nodeVar: instance}
        namespace: {uid: k8s-namespace, namespaceVar: namespace}
```

Each source has exactly one of `service` or `url`.

An optional `red:` block under `k9s.observability` maps the HTTP request,
error and duration metric and label names used by `:impact`; it is described
in [impact.md](impact.md) and contacts nothing.

An optional `requestPath:` block under `k9s.observability` maps the hops of a
Service's request path (ingress, app, dependencies, DNS, network) onto metric
and label names, with named profiles; it is described in
[request-path.md](request-path.md) and contacts nothing.

`service: namespace/name:port` (or `namespace/https:name:port` for a Service
that serves TLS; the port may be a number or a port name). The source is
reached through the Kubernetes API server service proxy,
`/api/v1/namespaces/NS/services/[scheme:]NAME:PORT/proxy/...`, on the
connection the app already holds, with your existing kube credentials. No
extra credential is needed and none is sent to the source itself. You need
RBAC permission to `get` `services/proxy` for that Service; without it reads
report `denied`. This is the recommended path.

`url: https://host[/path]`, a direct base URL. Userinfo, a query and a
fragment are refused. `tokenEnv` optionally names an environment variable whose
value is sent as a bearer token (a Grafana service-account token, for
example). `tokenEnv` requires `https`, except for a loopback address, and is
refused on a `service` source. If the variable is unset when a read is made,
the read reports `unavailable` and names the variable.

Grafana also accepts:

- `publicURL`: the browser-facing address. It is used only to build links; it
  is never contacted. Without it no links are offered.
- `dashboards`: a dashboard UID for `pod`, `workload`, `node` and `namespace`,
  with the names of that dashboard's template variables. A link sets
  `var-<name>` for each declared variable and the time range.
- `datasource`: the UID of Grafana's Prometheus datasource. If no `prometheus`
  source is declared, Prometheus queries go through Grafana's datasource proxy
  (`/api/datasources/proxy/uid/<uid>/...`) instead.
- `lokiDatasource`: the UID of a Grafana Loki datasource. It is used only to
  build Explore log links and is never queried.

What these are used for (`Shift-D`, `:dashboards`, annotations in `:timeline`)
is described in [grafana-handoff.md](grafana-handoff.md).

`queries` optionally saves PromQL expressions under a name for the `:query`
bar: `queries: {<name>: {expr, title, unit, legend}}`, at most 32. Saving one
contacts nothing. See [dashboards.md](dashboards.md).

The file is validated against the context JSON schema. A declaration that
cannot be used (both `service` and `url`, a malformed reference, a token over
plain HTTP) is reported in `:sources` and by the feature that needs it; it is
never guessed at. Edit the file while the app is closed, or restart afterward:
the running app keeps the declaration it loaded and writes it back when it
saves the context.

## `:sources`

`:sources` (also in the Ctrl-O palette) lists what is declared for the current
context: how each source is reached and which credential is used, by name
only. Opening it contacts nothing.

- `p` probes each declared source once: Prometheus
  `/api/v1/status/buildinfo`, Alertmanager `/api/v2/status`, Grafana
  `/api/health`. The result is one of `reachable`, `denied`, `absent`,
  `timeout` or `unavailable`, with the reason and the time of the read.
  `reachable` means the source answered; it says nothing about what the source
  monitors. Results are not refreshed; press `p` again to read again.
- `s` lists Services once (all namespaces, at most 500) and matches well-known
  names: `prometheus-operated`, `kube-prometheus-stack-prometheus`,
  `prometheus-k8s`, `prometheus-server`, `prometheus`,
  `alertmanager-operated`, `kube-prometheus-stack-alertmanager`,
  `alertmanager-main`, `alertmanager`, `kube-prometheus-stack-grafana`,
  `grafana`. It prints the YAML to add. A match is a name match, not a
  verified source, and nothing is written. If the list is denied or cut at 500
  the view says so; finding nothing does not show that nothing is installed.

A reply that arrives after the view is closed or the destination changed is
discarded.

`:history-review` uses the declared Prometheus source by default: the form is
prefilled with it and Request is still what contacts it. Typing a base URL in
its place works as before. See [historical-observability.md](historical-observability.md).

`:graph` draws charts for a selected Pod, workload or Node from the declared
Prometheus source, with fixed query templates and one read per request. See
[graphs.md](graphs.md).

`v` in `:dashboards` draws a Grafana dashboard's panels in the terminal, and
`:query` charts a typed or named expression, both from the declared
Prometheus source and read once per request. See [dashboards.md](dashboards.md).

## States

Every read returns one state, and features show it in words:

| State | Meaning |
| --- | --- |
| `declared` | Resolved from configuration; nothing contacted yet. |
| `complete` | The source answered and the reply fit every limit. |
| `incomplete` | The source answered, but a byte, series, sample or item limit cut what was kept. |
| `denied` | HTTP 401 or 403, from the source or from the API server proxy. |
| `absent` | HTTP 404: no such Service, path or datasource. |
| `timeout` | No answer within the deadline, or HTTP 408/504. |
| `unavailable` | No response, another HTTP status, a refused redirect, an unusable declaration, an unset token variable, or a cancelled read. |
| `not-configured` | The context declares no such source. |

## Limits

- One GET per read, eight-second deadline including the body, cancellable.
- 1 MiB per response; a larger body is `incomplete` and nothing from it is kept.
- Prometheus: at most 32 series and 4096 samples per query. Series beyond the
  limit are counted and the result is `incomplete`. The range window is at
  most seven days and the step is the smallest of 15s, 30s, 1m, 2m, 5m, 10m,
  15m, 30m, 1h, 2h, 6h that asks for at most 120 sample times. Samples that
  are not finite, not ascending or outside the window are dropped and counted.
- Queries are built from typed parts (metric name, label matchers, `rate`,
  `increase`, `max/min/avg_over_time`, `sum/avg/max/min/count by`, `topk`,
  `histogram_quantile`, `+ - * /`). Names are validated and matcher values are
  always written as escaped string literals, so a Pod or namespace name cannot
  change the shape of a query. The one exception is text a person wrote as a
  whole, wrapped with `AuthoredExpr` after its variables are substituted: the
  query of a panel on a dashboard of the declared Grafana, and the expression
  typed into `:query` or saved under `queries`. It is bounded, and it is never
  text assembled from cluster names: a name put in place of a variable is
  escaped for the place it lands in (see [dashboards.md](dashboards.md) and
  [request-path.md](request-path.md)).
- When Prometheus rejects a query (HTTP 400 or 422), its own error text is
  kept, without control characters and at most 400 characters long, so that
  a typed expression can be corrected. Nothing else of a failed answer is
  kept.
- Alertmanager: `GET /api/v2/alerts`, at most 500 alerts kept.
- Grafana: `GET /api/annotations` (at most 200, window at most seven days),
  `GET /api/search` (at most 100 dashboards) and `GET
  /api/dashboards/uid/<uid>` (one dashboard definition, used whole or not at
  all). Grafana cuts silently at its limit, so a full page is reported
  `incomplete`. Annotation author identity is not kept.
- Labels, annotations and other text are stripped of control characters and
  bounded (32 labels, 256 characters per label value, 1024 per annotation).

## Features that read a declared source

- `:alerts` reads Alertmanager once and ties each alert to the object its
  labels name; `:problems` and `:timeline` add the same alerts to their own
  single read when Alertmanager is declared. See [alerts.md](alerts.md).
- `v` in `:dashboards` reads one dashboard definition from Grafana once and
  its panels from Prometheus; `:query` reads Prometheus only. See
  [dashboards.md](dashboards.md).

## For feature code

Views call `observabilitySource(app, kind)` (internal/view/sources.go) to
resolve the client for the active context, then call the typed reads in
`internal/observability` from a goroutine with a cancellable context:
`Client.Query`, `Client.QueryRange`, `Client.Alerts`, `Client.Annotations`,
`Client.SearchDashboards`, `Client.Probe`, and `GrafanaSource.DashboardLink`.
Links with a live range, Explore links and the pin snippet are in
`internal/observability/grafana_links.go` (`DashboardURL`, `DashboardUIDURL`,
`ExploreMetricsURL`, `ExploreLogsURL`, `PinSnippet`).
Text from a source is cleaned but must still be escaped for tview.

## Synthetic fixture

`scripts/operator-study-fixture.py` serves the three sources through the
service-proxy paths for `monitoring/prometheus:9090`,
`monitoring/alertmanager:9093` and `monitoring/grafana:3000`, and writes a
context configuration that declares them, so the features work with no setup.
`--deny-source prometheus|alertmanager|grafana` (repeatable) makes that source
answer HTTP 403. The data (scripts/observability_sources_fixture.py) is
generated relative to the fixture start time and follows the checkout-api
story: healthy request, error and latency series before the revision 2
rollout twelve minutes before start, failing ones after it; memory climbing to
the 256Mi limit and restarting; a node whose node-exporter series stop when it
goes silent. For `:graph` it also serves per-Pod network counters and
`kube_node_status_allocatable`; no Pod of the checkout story has a CPU limit,
so CPU limit and throttling read `no data` there. `--no-icons` starts the
application with `ui.noIcons`. The fixture evaluates the query forms the typed
builder can produce, and the few more a dashboard panel needs.

A second scenario, namespace `gitlab`, is an intermittent hang in which
nothing crash-loops: one webservice Pod is CPU throttled at its limit in
three stalls, with ingress, Puma, dependency, DNS and Hubble series, a Pod
proxy that answers slowly for that Pod (`--deny-pod-proxy` answers 403) and
two Grafana dashboards with panel JSON. It is described in
[request-path.md](request-path.md). Fixtures show bounds and wording, not
production behavior.

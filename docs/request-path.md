# Request path: hops, profiles and the metrics they need

A request to a Service passes several hops: the ingress controller, the
Service's own Pods, the Services those Pods call (a database, a cache), the
cluster DNS and the network in between. When a page sometimes just waits and
nothing is crash-looping, the evidence is in the metrics of those hops: which
one was slow, whether one Pod differs from its peers, and when.

This page describes the optional declaration that tells k9+ which metrics
describe each hop, the built-in profiles, the metrics each one needs, how to
enable the Hubble metrics the Cilium profile reads, the drawing primitives
built for these views, and the synthetic scenario used to exercise them.

It is a foundation: the declaration, the resolver and the primitives exist;
the views that use them are described in their own pages when they land.

## What is never done

- Declaring a request path contacts nothing. Resolving a hop builds query
  text and sends nothing; a view sends the queries only when you open it.
- A dependency is never guessed from names, labels or traffic. It is read
  only when you name the Service.
- A hop that has no mapping, cannot be pinned to the object, or answers with
  no series is reported as **not read**, with the reason. It is never drawn as
  zero and never described as healthy.
- A signal fills a fixed query shape with metric names and label matchers.
  The declaration cannot carry query text, and every value taken from the
  cluster is written as an escaped string literal.
- Two things that happen in the same minutes overlap. Nothing here says that
  one caused the other.

## Declaration

The declaration lives next to the observability sources, in the context
configuration file (`<config dir>/clusters/<cluster>/<context>/config.yaml`)
under `k9s.observability.requestPath`. Every part is optional.

```yaml
k9s:
  observability:
    prometheus:
      service: monitoring/prometheus-operated:9090
    requestPath:
      defaults:                      # for every Service without its own entry
        ingress: [ingress-nginx-exported]
      services:
        gitlab/gitlab-webservice-default:      # namespace/name of the Service
          app: [gitlab-webservice, container-cpu]
          network: [none]                      # turn one hop off
          dependencies:                        # explicit Service references
            - service: gitlab/gitlab-postgresql
              profile: postgres-exporter
            - service: gitlab/gitlab-redis-master
              profile: redis-exporter
              match: ['pod=~"gitlab-redis-master-.*"']
            - service: gitlab/gitlab-gitaly
              profile: grpc-server
      profiles:
        ingress-nginx:               # same name as a built-in: replaces it
          extends: ingress-nginx
          match: ['controller_class="k8s.io/ingress-nginx"']
        queue:
          hop: dependency
          scope: {namespace: namespace, pod: pod}
          signals:
            - {name: lag, title: seconds behind, kind: gauge, metric: queue_lag_seconds, aggregate: max, unit: seconds}
            - {name: errors, kind: ratio, metric: queue_jobs_total, match: ['status="failed"']}
```

- `services` is keyed by `namespace/name`. Each entry lists profile names for
  the hops `ingress`, `app`, `dns` and `network` (at most four per hop), and
  `dependencies` (at most 16). A hop that is left out keeps the entry of
  `defaults`, and then the built-in default. The single entry `none` turns a
  hop off; it is then reported as not read.
- A dependency names a Service (`namespace/name`), the profile that reads it
  and optional extra matchers for a namespace that holds more than one
  instance of the exporter. Without Pod names the profile reads every matching
  series in the dependency's namespace, and says so.
- `profiles` adds profiles (at most 32). `extends` starts from another
  profile: `scope` entries and `match` are added to it, and `signals`, when
  given, replace its signals. A profile may extend the built-in whose name it
  takes.

Built-in defaults when nothing is declared:

| Hop | Default profiles |
| --- | --- |
| ingress | `ingress-nginx` |
| app | `red`, `container-cpu` |
| dependency | none: declare each one |
| dns | `coredns` |
| network | `cilium-hubble` |

### Profile fields

- `hop`: `ingress`, `app`, `dependency`, `dns` or `network`.
- `scope`: which label carries which Kubernetes name. Keys are `namespace`,
  `service`, `ingress`, `pod` and `node`; values are label names. When a hop
  is resolved, every scope key the object has a name for becomes a matcher. A
  key the object has no name for is left out and listed, so a view can say
  what did not narrow the query. A profile without a scope reads cluster-wide
  series (the cluster DNS) and is marked as such. If a profile has a scope and
  nothing binds, or the app or network hop has no Pod names, the profile is
  not read: reading every series would not be about this object.
- `match`: label matchers added to every signal, written `label="value"`,
  `label!="value"`, `label=~"regex"` or `label!~"regex"`.
- `signals` (at most 16), each with:
  - `name` (short identifier) and optional `title` (plain words);
  - `kind`:

    | Kind | Query shape |
    | --- | --- |
    | `rate` | `sum(rate(metric{...}[w]))` |
    | `ratio` | `sum(rate(metric{..., match}[w])) / sum(rate(divisor{..., divisorMatch}[w]))`; without `divisor` the metric itself, which gives the share that matches |
    | `quantile` | `histogram_quantile(q, sum by (le) (rate(metric{...}[w])))`; `metric` is the `_bucket` series, `quantile` defaults to 0.99 |
    | `gauge` | `aggregate(metric{...})`; `aggregate` is `sum` (default), `max`, `min` or `avg` |

  - `metric`, `match`, and for a ratio `divisor` and `divisorMatch`;
  - `by`: up to four extra labels kept apart (for example `reason`);
  - `unit`: `seconds`, `ratio`, `perSecond`, `count`, `bytes` or `cores`;
  - `worse`: `high` (default), `low` or `none`, the direction that counts as
    bad when Pods are compared.

Each signal resolves to two queries: one for the whole object and, when the
profile has a `pod` scope label, one grouped by that label so Pods can be
compared. The range `w` is the rate window the view chooses for its step.

## Built-in profiles and the metrics they need

Label names below are the ones each exporter attaches by default with the
usual Kubernetes service discovery (`namespace`, `pod`). If your Prometheus
uses other names, extend the profile and change its `scope`.

### `ingress-nginx` (ingress)

Selected by `namespace`, `ingress` and `service` (the Ingress, and the
backend Service named in it).

| Signal | Metric | Meaning |
| --- | --- | --- |
| `requests` | `nginx_ingress_controller_requests` | requests per second |
| `499` | `nginx_ingress_controller_requests{status="499"}` | requests the client closed while waiting, per second |
| `5xx` | `nginx_ingress_controller_requests{status=~"5.."}` over all | share answered with 5xx |
| `p99` | `nginx_ingress_controller_request_duration_seconds_bucket` | request duration at the ingress |
| `upstream-p99` | `nginx_ingress_controller_response_duration_seconds_bucket` | time the backend took |

A rise in 499 without a rise in 5xx is what "the page just waits" looks like
at the ingress: clients give up, nothing is refused. The default histogram
ends at 10s, so a longer wait reads as 10s.

`ingress-nginx-exported` is the same profile for a Prometheus that scrapes
the controller without `honorLabels` and therefore renames the controller's
own labels: it selects by `exported_namespace` and `exported_service`.

### `gitlab-webservice` (app)

The GitLab chart's webservice Pods, selected by `namespace` and `pod`.

| Signal | Metric | Meaning |
| --- | --- | --- |
| `p99` | `http_request_duration_seconds_bucket` | Rails request duration |
| `requests` | `http_requests_total` | requests finished by Rails |
| `5xx` | `http_requests_total{status=~"5.."}` over all | share answered with 5xx |
| `queued` | `puma_queued_connections` | connections waiting for a free Puma thread |
| `free-threads` | `puma_pool_capacity` | threads free to take a request (low is bad) |
| `busy-threads` | `puma_active_connections` | threads working on a request |
| `threads` | `puma_max_threads` | threads configured |

These are exposed by the webservice container's metrics endpoint
(`gitlab.webservice.metrics.enabled`, on by default in the chart) and need a
scrape that keeps the `pod` label.

### `container-cpu` (app)

cAdvisor series from the kubelet, selected by `namespace` and `pod`, without
the pod cgroup and the pause container.

| Signal | Metric | Meaning |
| --- | --- | --- |
| `throttled` | `container_cpu_cfs_throttled_periods_total` over `container_cpu_cfs_periods_total` | share of CPU periods throttled at the limit |
| `cpu` | `container_cpu_usage_seconds_total` | cores used |

The CFS counters exist only for containers with a CPU limit.

### `red` (app)

The generic HTTP profile. Its metric and label names come from the `red:`
mapping described in [impact.md](impact.md); without one it reads
`http_requests_total` (label `code`) and
`http_request_duration_seconds_bucket`, selected by `namespace` and `pod`,
and by the mapping's `workloadLabel` when one is set. Signals: `p99`, `p50`,
`requests`, `errors`.

### `coredns` (dns)

CoreDNS server metrics, read for the whole cluster DNS: they describe what
CoreDNS answered to everyone, not what one Pod experienced.

| Signal | Metric | Meaning |
| --- | --- | --- |
| `p99` | `coredns_dns_request_duration_seconds_bucket` | request duration at CoreDNS |
| `servfail` | `coredns_dns_responses_total{rcode="SERVFAIL"}` over all | share of SERVFAIL answers |
| `nxdomain` | `coredns_dns_responses_total{rcode="NXDOMAIN"}` over all | share of NXDOMAIN answers (normal with search domains) |
| `requests` | `coredns_dns_requests_total` | requests per second |

### `cilium-hubble` (network)

Hubble metrics, selected by `destination_namespace` and `destination_pod`.

| Signal | Metric | Meaning |
| --- | --- | --- |
| `drops` | `hubble_drop_total`, by `reason` | packets dropped on the way to the Pods |
| `rst` | `hubble_tcp_flags_total{flag="RST"}` over `{flag="SYN"}` | resets per new connection |
| `dns-errors` | `hubble_dns_responses_total` with an `rcode` other than `No Error` and `Non-Existent Domain`, over all | share of DNS answers to the Pods that were an error |
| `flows` | `hubble_flows_processed_total` | flows processed |

Hubble metrics are off by default and carry no Pod labels unless asked. The
profile needs the metrics `drop`, `tcp`, `dns` and `flow` with the label
context below. With the Cilium Helm chart:

```yaml
hubble:
  enabled: true
  metrics:
    enabled:
      - "drop:labelsContext=source_namespace,source_pod,destination_namespace,destination_pod"
      - "tcp:labelsContext=source_namespace,source_pod,destination_namespace,destination_pod"
      - "flow:labelsContext=source_namespace,source_pod,destination_namespace,destination_pod"
      - "dns:query;labelsContext=destination_namespace,destination_pod"
    serviceMonitor:
      enabled: true        # or scrape port 9965 of the cilium agents yourself
```

`labelsContext` produces the labels `source_namespace`, `source_pod`,
`destination_namespace` and `destination_pod`. For a DNS answer the
destination is the Pod that asked. Per-Pod labels multiply the number of
series; if you use `destination_workload` instead, extend the profile with
`scope: {pod: destination_workload}` and expect workload names, not Pod
names, in per-Pod answers. The older `sourceContext=`/`destinationContext=`
options produce `source` and `destination` labels instead and need their own
profile. These metrics are separate from the live flow views in
[hubble.md](hubble.md), which read Hubble Relay and keep no history.

### Dependency profiles

Each is selected by `namespace` and, when the Pods behind the dependency
Service are known, by `pod`.

| Profile | Signals | Metrics |
| --- | --- | --- |
| `postgres-exporter` | `up`, `connections`, `active`, `max-connections` | `pg_up`, `pg_stat_activity_count` (label `state`), `pg_settings_max_connections` |
| `redis-exporter` | `up`, `latency`, `clients` | `redis_up`, `redis_commands_duration_seconds_total` over `redis_commands_total`, `redis_connected_clients` |
| `grpc-server` | `p99`, `errors`, `requests` | `grpc_server_handling_seconds_bucket`, `grpc_server_handled_total` (label `grpc_code`) |

`grpc-server` reads the go-grpc-prometheus server metrics, which is what
Gitaly exposes.

## Limits

- At most 64 services, 32 declared profiles, 16 signals per profile, four
  profiles per hop, 16 dependencies per Service, 32 matchers per list.
- Pod names are matched exactly. Names that would make one matcher longer
  than about 1000 bytes are left out and counted, so a view can say how many
  Pods were not asked for.
- Every query a view sends from a resolved hop goes through the declared
  Prometheus client: one GET with the limits in
  [observability-sources.md](observability-sources.md) (8 second deadline,
  1 MiB, 32 series, 4096 samples, 120 sample times per series).
- A profile cannot express joins, offsets or subqueries. That is deliberate:
  a signal is one of four shapes that can be explained in a sentence.

## For feature code

`internal/observability/requestpath.go`:

- `Sources.ResolveHop(hop, "namespace/name", HopTarget{...}, rateWindow)`
  returns a `HopPlan`: the profiles used, one `HopQuery` per signal
  (`Expr` for the whole object, `ByPod` per Pod, `PodLabel`, the `Scope`
  text, `Unbound`, `ClusterWide`, `PodsOmitted`) and `NotRead` entries with
  reasons.
- `Sources.ResolveDependencies("namespace/name", pods, rateWindow)` returns
  one plan per declared dependency, or one plan whose `NotRead` says that
  none is declared. `Sources.ResolveDependency` resolves a single one.
- `HopQuery.Missing(result)` turns an answer that is not a reading (not
  asked, denied, timed out, or answered with no series) into a `NotRead`.
- `Sources.HopProfiles`, `Sources.PathDependencies`, `Sources.Profile`,
  `Sources.ValidateRequestPath`, `BuiltinProfileNames` and `ParseMatcher`.

The configured path itself (Ingress, Service, EndpointSlices, Pods) comes
from the `:network-review` collector, `networkpath.Collect`; see
[network-path-review.md](network-path-review.md). Its snapshot supplies the
Ingress name and the Pod names a `HopTarget` needs.

`observability.AuthoredExpr` is the one way to send query text that was not
built from typed parts. It exists for the query of a panel on a Grafana
dashboard the context declares, after its variables are substituted; it
refuses control characters, a leftover `$` variable and more than 4096
bytes. It must not be given text assembled from cluster names or input.

### Drawing primitives (`internal/chart`)

- `Series.Stroke`: `Thin` (unchanged default), `Solid` (continuous: every
  dot column a line crosses is filled from where the line enters it to where
  it leaves, so a steep sawtooth is one stroke; `Dash` is ignored) and `Bold`
  (solid, two dots thick). A line is never joined across a hole in the
  samples.
- `Series.Marker` repeats a glyph along a line (`MarkerFor(n)` gives eight
  distinct ones) so lines can be told apart without colour or dashes. In the
  plain character form only a plain character marker is drawn; there every
  line also has its own character.
- `Spark` draws a one-cell-high trend: a thin joined braille line
  (`SparkLine`) or eighth-block bars (`SparkBars`), with `×` (`x` without
  icons) for a missing sample. `SparkValues` folds samples into cells by the
  largest value, so a short peak is kept, and leaves a cell without a sample
  missing.
- `Lanes` draws labelled rows on one time axis: a mark where the lane's
  condition held, `·` where it did not, `×` where the lane has no data, a
  shared tick row and a cursor column. `Frame.Marked`, `Frame.MarkedCount`
  and `Frame.ColumnSpan` give the same cells as numbers and times.
- `Grid` lays mini charts out side by side with one Y scale and one time
  axis per column, and reports how many did not fit (`GridLayout.NotShown`).
- `Outliers` compares one number per peer (one per Pod). It uses the median
  and the median absolute deviation: a value deviates when its modified
  z-score is at least 3.5 (or more than half of the peers share one value and
  it is not that value), it is at least twice the median (or at most half of
  it), it is at least `MinDelta` away from the median, and it lies on the side
  asked for. Fewer than three finite values is "not enough peers" and nothing
  is reported. A deviation is a difference between peers; it is not a cause.
- Units `Seconds` (`310ms`, `41s`) and `PerSecond`, and `UnitNamed` for the
  unit names of signals.

`:graph` uses the solid stroke, with each line's letter repeated along it
when a chart has several lines, in place of the dotted lines it drew before.
Its per-Pod comparison (`p`, [graphs.md](graphs.md#which-pod-is-different-p))
uses the grid, the sparkline and the outlier rule, and resolves the app hop
of the Service in front of the Pods to add the profile's per-Pod signals
(latency, queue, thread pool) to its own. For that page the grid gained a
`Ceiling`, so that the scale stays the same from page to page; a count axis
now ends on a whole number with a whole half, a duration axis past a minute
on whole minutes or hours, and a row that carries a level line is labelled
with that level's value.
`:impact` keeps its eight-level block trend: a four-level line would lose
detail in the six to twelve cells it has per side.

## Synthetic scenario: GitLab intermittent hang

`scripts/operator-study-fixture.py` adds namespace `gitlab`
(scripts/gitlab_hang_fixture.py), modelled on the official GitLab Helm chart
on a cluster whose networking is Cilium. Nothing is crash-looping and every
Pod is Ready with zero restarts; no alert fires. Status views are quiet on
purpose. Everything is generated relative to the fixture start and only
exists on loopback.

Objects (UIDs are the fixed strings `study-<kind>-<name>`):

- Ingress `gitlab-webservice-default` (class `gitlab-nginx`, host
  `gitlab.study.example.test`) to Service `gitlab-webservice-default:8181`,
  Deployment `gitlab-webservice-default` with three Pods on nodes `worker-d`,
  `worker-e` and `worker-f` (containers `webservice` with a 1500m CPU limit
  and a readiness probe on `/-/readiness` port 8080, and `gitlab-workhorse`).
- Deployment `gitlab-nginx-ingress-controller` (two Pods) with its
  LoadBalancer Service; StatefulSets and Services `gitlab-postgresql`,
  `gitlab-redis-master` and `gitlab-gitaly`; `coredns` and Service `kube-dns`
  in `kube-system`; an EndpointSlice per Service whose targets are the Pod
  UIDs.

The story, in seconds before fixture start: three stalls at 52 to 49
minutes, 33 to 29 minutes and 7 to 5 minutes; the hour before is quiet.
During each stall only `gitlab-webservice-default-6f9c7d5b84-hx4mt` (on
`worker-f`) differs: its CPU is throttled in about 86% of periods at its
limit, all its Puma threads are busy, connections queue, and its Rails p99
goes from about 0.35s to tens of seconds. The other two Pods stay at about
0.35s. The ingress sees HTTP 499 rise from near zero to about 2 per second
and almost no 5xx. PostgreSQL, Redis, Gitaly and DNS stay flat. In the two
longer stalls the Pod fails its readiness probe from 90 seconds in until 20
seconds after the stall (`kube_pod_status_ready`); it is Ready at start. One
unrelated Hubble drop blip (`POLICY_DENIED`, about 0.8 per second for one
minute) happens 41 to 40 minutes before start, between two stalls, towards a
different Pod (`…-2bq7k`): a view must not tie it to the stalls.

Pod proxy, for a probe: `GET
/api/v1/namespaces/gitlab/pods/<pod>:8080/proxy/-/readiness` (and
`/-/liveness`) answers 200 with a small JSON body. The outlier answers after
1.5 to 4 seconds and about one request in eight after 5.5 to 7.5 seconds;
the other two answer in 5 to 40 milliseconds. Another path answers 404, a
database or gRPC Pod 503, an unknown Pod 404. `--deny-pod-proxy` makes every
`pods/proxy` read answer 403 and makes an access review for it say no.

Grafana (`/api/search`, `/api/dashboards/uid/<uid>`) adds `gitlab-webservice`
("GitLab / Webservice overview": rows, stat, gauge, timeseries, a table and
a Loki logs panel, with `$datasource`, `$namespace`, `$pod`,
`$__rate_interval`, `legendFormat` and units) and `gitlab-ingress` (stat,
gauge, timeseries and a heatmap). The other listed dashboards return one text
panel that says they are placeholders. So that a panel query can be evaluated
once its variables are substituted, the fixture's PromQL subset also accepts
`[5m]`-style ranges, `sum(...) by (...)` and unparenthesised `+ - * /`, and
serves `/api/v1/label/<name>/values` with `match[]`.

The generated context configuration declares the request path shown at the
top of this page, without the `defaults`, `network` and `profiles` parts.
`python3 scripts/operator-study-fixture.py --smoke` checks the scenario.
Fixtures show bounds and wording, not production behavior.

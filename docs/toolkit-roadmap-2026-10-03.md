# K9plus daily Kubernetes toolkit roadmap

Proposed direction, 3 October 2026. Baseline: default-branch revision `1978cb74caab53f74403e9e776d18bb2975e01e4`. The Horizon 1 implementation note records the first delivered slice; the remaining proposals do not create delivery commitments.

Make k9plus a daily workspace for Kubernetes engineers: understand an application, review a change, follow a release, perform maintenance, and leave useful evidence. Keep the fast keyboard resource browser underneath those tasks. Start by making investigations readable at a glance, then connect existing tools into complete workflows.

The first sequence should be **investigation overview → saved application workspace → daily work queue → rollout review → desired-state and GitOps review**. Storage, access, capacity and maintenance follow. Fleet, security, cost and backup providers should be added when actual users need them. The order below is a recommendation based on reviewed code, this user's feedback and selected community reports, rather than a survey or promised schedule.

## What already exists

The fork already includes resource browsing/editing, shells, port-forwards, metrics/Pulse, Helm history/values/rollback, node cordon/drain, RBAC rule views, CronJob trigger/suspend, PVC consumers, native Flux and certificate workflows, configurable custom jumps and optional CLI plugins. It also has structured JSON/logfmt logs, multi-Pod streams, source lanes, noise profiles, frozen/history observations and recording.

Recent delivery added selected-resource identity guards, searchable actions, explicit metric availability, responsive guarded restart/scale/delete, retained investigation and relationship navigation, capability diagnostics, A/B API observations, resource pressure, and reviewed/offline evidence bundles. Extend these foundations. Reintroducing JSON logs, Helm rollback or a generic RBAC viewer as new features would duplicate shipped behavior.

Use the current [delivery matrix](https://github.com/jaredpricedev/k9s/blob/1978cb74caab53f74403e9e776d18bb2975e01e4/ROADMAP.md) and [log workbench](https://github.com/jaredpricedev/k9s/blob/1978cb74caab53f74403e9e776d18bb2975e01e4/docs/log-workbench.md) for the baseline. The recorded 10,000-Pod filtering p95 remains 107.274 ms against a proposed 100 ms target. Operator usability, current live Relay/Gateway compatibility and broader platform verification remain open validation work.

## What community reports suggest

Research covered public GitHub Issues and Flux community Discussions, including primary posts and selected human replies. Older and closed reports describe useful tasks, not necessarily unfixed bugs in current software. Reddit and Stack Overflow were outside this environment's configured research access. Reactions are not a measure of Kubernetes-wide prevalence.

| Evidence | Concrete pain | Product implication |
| --- | --- | --- |
| [K9s multi-namespace request](https://github.com/derailed/k9s/issues/3899), opened 2026; earlier [request](https://github.com/derailed/k9s/issues/1679) | Several application namespaces are useful; switching repeatedly is tedious and all namespaces are noisy. | Named scopes that carry through browsing, logs, inspection and review. |
| [kubectx session contexts](https://github.com/ahmetb/kubectx/issues/12) and [repeated current-context requests](https://github.com/ahmetb/kubectx/issues/127) | Different terminal sessions need distinct contexts and visible destinations. | Session isolation and destination-aware action previews/receipts. Much of the display foundation already exists. |
| [Restricted namespace navigation](https://github.com/ahmetb/kubectx/issues/83) | A user can access a known namespace but cannot list namespaces; discovery failure gets mistaken for absence. | Permit explicit namespace entry and show denied/unknown separately from missing. |
| [Flux long status columns](https://github.com/fluxcd/flux2/issues/2385) and [community reconciliation confusion](https://github.com/fluxcd/flux2/discussions/2663) | Long messages hide scan columns; opaque progress text does not explain what is being waited on. | Findings and progress stages first, full evidence on demand. |
| [Flux community request for a plan](https://github.com/fluxcd/flux2/discussions/1617) | Practitioners want to see changes and pruning before a GitOps update removes services. | Creates/changes/deletions, affected resources, source revision and honest preview fidelity. |
| [Argo deployment-history diff](https://github.com/argoproj/argo-cd/issues/2355) and [read-only kubectl diff limitations](https://github.com/kubernetes/kubectl/issues/981) | Engineers want to understand rollback effects; server-side diff may require PATCH permission. | Release review with clearly separated local comparison and explicit server dry-run. |
| [Flux application-version discussion](https://github.com/fluxcd/flux2/discussions/4718) and [suspended-resource queue](https://github.com/fluxcd/flux2/discussions/2494) | Chart versions may not identify application releases; paused resources are hard to find across kinds. | App/image/Git revision timelines and paused/stale/failed work queues. |
| [JSON logs](https://github.com/derailed/k9s/issues/364) and [replica logs](https://github.com/derailed/k9s/issues/827) | Users leave the app for parsed logs and combined replica streams. | Preserve the fork's existing strengths; make them easy to reach from a workspace and release review. |
| [PVC expansion](https://github.com/derailed/k9s/issues/3812) and [PVC utilization](https://github.com/derailed/k9s/issues/1538) | Storage reviews require YAML edits and separate usage tools. | Workload-to-volume navigation, source-aware usage and reviewed expansion plans. |
| [Drain semantics report](https://github.com/derailed/k9s/issues/2129), [rollback destination report](https://github.com/derailed/k9s/issues/2689) and [expired credentials](https://github.com/derailed/k9s/issues/2677) | Routine operations must preserve shutdown behavior, target identity and recoverable connections. | Extend operation lifecycle safeguards before expanding the mutation toolkit. These reports do not establish current fork bugs. |

Scope, release review and presentation have direct community support. A cost workspace, backup review and broader fleet workflow are useful product hypotheses with less direct evidence in this research. Validate demand before committing to those larger integrations.

## Investigation redesign first

The recorded pressure view spends much of its first viewport on identity and unit explanations before showing container numbers. Comparison repeats A/B metadata before exposing changed fields. Troubleshooting repeats container status in summary and detail. The existing `m` toggle shortens messages, but cannot fix that hierarchy.

The first viewport should answer: **which object, what is happening now, which container or dependency is affected, what was observed previously, and what useful evidence can I open next?**

| View | Proposed default | Detail retained on demand |
| --- | --- | --- |
| Overview | Compact identity/access strip; observation age/coverage; ranked observed findings; container table; most relevant retained events. | Full condition messages, raw field pointers and collection errors. |
| Containers | Ready/current state/restarts/last termination in aligned rows. Collapse normal rows when helpful. | Previous logs, exit code/time, image, resources and full status. |
| Events | Latest retained warnings grouped by identity/reason, occurrence count and available timestamps. | Full messages and exact event records; explicitly not a complete history. |
| Changes | Changed-field count and short path/value table; compact A/B identity/time strip. | Full values, normalization rules and provenance. Keep A fixed. |
| Resources | Request/limit/usage table with units and visible N/A reasons. | Init/sidecar/Pod-level budget semantics, sample source/window and constraints. |
| Evidence | Selected findings, notes and previewed content first. | Full source/time/scope, excluded content, redaction limits and export review. |

At 80×24, use one content pane and selected-row detail. At 120×34, offer a findings pane with a selected-evidence pane. Collapse the large logo and extended header within investigation screens. Keep context, namespace, access mode, observation age and incomplete/denied/stale badges visible. Full destination remains available through F2. Plain words and markers must work in monochrome and no-icons modes.

Illustrative compact overview, not an app capture:

```text
[RO] demo-dev / apps       Pod investigation-api
Snapshot 12s ago · API     Coverage: metrics unavailable
Overview  Containers  Events  Changes  Resources  Evidence

CURRENT  api waiting: CrashLoopBackOff · not ready
PREVIOUS api termination: OOMKilled · exit 137

CONTAINER READY CURRENT           RESTARTS LAST STATE
api       no    CrashLoopBackOff      7    OOMKilled
sidecar   yes   Running               0    —

RETAINED FailedScheduling ×2 · open timestamp/message
NEXT     Previous logs | Events | Resource pressure

Enter detail   Actions   r refresh   g related   Esc back
```

Do not infer that the previous OOM caused the current crash, that an old scheduling warning is active now, or that missing evidence means healthy. Rank conditions using known resource semantics; `False` is not universally a failure for CRDs. Label relations as owned-by, selector match, references or reported backend. Show hypotheses as proposed next checks, separate from reported facts.

Break this into six reviewable increments:

1. Typed investigation findings/sections and coverage records independent of rendered strings; preserve current bounded readers and identity checks.
2. Overview-first renderer, compact investigation header and deduplicated container/status rows.
3. Retained tab/detail navigation, contextual logs/pressure/relationship actions and search.
4. Pressure and comparison tables with unavailable-state badges and reversible noise controls.
5. Evidence preview and provenance drawer, preserving full export fidelity and visible limitations.
6. Operator evaluation against the current view on identical tasks.

Keep [#22](https://github.com/jaredpricedev/k9s/issues/22) and [#23](https://github.com/jaredpricedev/k9s/issues/23) open until their operator checks are actually measured. Proposed study: six to eight engineers, mixed experience, randomized old/new order; CrashLoop, scheduling, missing metrics/RBAC, rollout changes and Service-to-Pod tasks. Measure correct interpretation, next action, time, scroll distance and destination awareness. Provisional goals are fault/container recognition within ten seconds and correct next evidence action within thirty seconds; these are unverified design targets, not shipped claims.

## Ordered delivery horizons

The horizons express dependency and priority, not calendar dates. S is a focused extension; M is a composed workflow/new model; L spans several resource types or providers. These are relative scope estimates, pending implementation design. Start with universal Kubernetes workflows; optional controllers and external providers must explain their prerequisites.

### Horizon 1 Make everyday navigation and investigation coherent

The first implementation of TK01–TK05 is included in this change. It provides
typed investigation tabs, saved scopes, current-state queue, scoped inventory,
private pins/searches/layout and explicit connection checks. See
[the working guide](daily-workspace.md). Findings are current API observations;
historical daily deltas, missed-run history and refreshing the running session's
client handles remain later extensions. Diagnostic retry rebuilds its own
clients. Operator usability evaluation remains pending.

| ID | Workflow | Next useful delivery | Scope |
| --- | --- | --- | --- |
| TK01 | Investigation overview | Deliver the six UX increments above; facts before paragraphs, full details preserved. | M |
| TK02 | Saved application workspaces | Explicit context, multiple named namespaces, label/kind filters, pinned resources and saved layout. Carry scope to logs, inspection and review; existing favorites alone do not compose an app. | M |
| TK03 | Daily work queue | An explicitly opened app/namespace briefing for unready workloads, stuck rollouts, failed jobs, paused GitOps, expiring certificates and quota concerns. Open each finding into its evidence. | M |
| TK04 | Scoped search and inventory | Search discovered resources within the selected workspace; saved searches, clear kind/namespace fields and permission/coverage indicators. Enter a known namespace when discovery is forbidden. | M |
| TK05 | Connection and session health | Explain expired credentials, API unreachable, TLS, discovery and denied permission separately. Offer explicit retry/refresh while retaining workspace state. Keep session destination independent of global kubeconfig changes where supported. | M |

First release slice: TK01 overview plus one single-context workspace and a small work queue. Do not introduce fleet access or every provider in the initial slice. The queue may describe observations since opening; it cannot fabricate historical activity before collection started.

### Horizon 2 Review changes and operate releases

| ID | Workflow | Next useful delivery | Scope |
| --- | --- | --- | --- |
| TK06 | Desired-state review | Select a manifest, Kustomize rendering, Helm rendering or named Git revision. Show semantic adds/changes/deletes, explicit ownership and source identity alongside live state. Existing A/B API snapshots remain a separate comparison type. | L |
| TK07 | Rollout and recovery review | Image/digest/config revision, old/new ReplicaSets, desired/ready counts, blocking conditions and elapsed progress. Follow accepted writes to observed controller outcome. Preview the actual rollback target and resulting changes. | M |
| TK08 | GitOps ownership and progress | Resource → Flux/Argo application → reconciler → source revision. Show suspended, outdated, failed and waiting stages. Native read-only Argo view first; source-aware action adapters later. | M–L |
| TK09 | Reviewed change sets | Preview scope/destination and per-resource changes, then explicit execution with progress and receipts. Route GitOps-owned resources toward Git review or controller-native actions. Partial outcomes remain visible; a Kubernetes batch is not atomic. | L |
| TK10 | Configuration review | Effective env/config references, mount targets, missing keys, consumers and likely rollout needs. Diff metadata/key names by default; do not fetch or display Secret values just to build a preview. | M |
| TK11 | Application activity | Join observed revision/image changes, rollout progress, GitOps reconciliation, jobs and retained events into an app timeline. Distinguish chart version, app version, tag and resolved image digest. | M |

Dependency: extend the existing operation framework to inherited actions before TK09. Helm rollback, node maintenance and CLI providers need destination pinning, cancellable background work, appropriate preconditions and per-target results. Request cancellation does not undo an accepted write. Do not retry an unknown mutation outcome automatically.

TK06 must separate read-only local comparison from an explicit server dry-run that invokes API admission/defaulting and may require write permissions. Mark local previews as partial fidelity where admission, defaults, ownership or controller pruning cannot be reproduced. Never call a selected-resource subset an exhaustive GitOps prune plan.

### Horizon 3 Make routine engineering work complete

| ID | Workflow | Next useful delivery | Scope |
| --- | --- | --- | --- |
| TK12 | Access explanation | Answer a concrete identity/verb/resource/namespace question. Join authorization-review evidence with visible roles/bindings and rule provenance; distinguish invalid input, denied request and incomplete visibility. | M |
| TK13 | Storage review and expansion | Pod → PVC → PV → StorageClass → CSI/VolumeAttachment. Binding, attach/mount/resize states, topology and support for expansion; capacity and actual usage remain different facts. Preview batch expansion and observe progress. | M–L |
| TK14 | Node maintenance planner | Before drain, show affected workloads, PDB/eviction blockers, grace periods, local data, DaemonSets and relevant scheduling constraints. Respect existing eviction semantics; observe drain/recovery progress. | M |
| TK15 | Capacity and autoscaling review | Requests/limits, quotas/LimitRanges, allocatable resources, HPA targets/current status and Pending constraints. Explain missing metrics. Add a historical provider only when configured. | M |
| TK16 | Scheduled and one-off work | CronJob timezone/schedule, recent Jobs, success/failure, deadlines and concurrency. Compose existing trigger/suspend with monitored outcomes and scoped logs. Future schedule previews must state their calculation assumptions. | S–M |
| TK17 | Network path review | DNS configuration → Service → EndpointSlice → Pod, plus Ingress/Gateway attachment conditions. Show ports, target readiness, allowedRoutes/ReferenceGrants where available; optional probes and Hubble observations are explicit. | L |
| TK18 | Managed local sessions | One visible registry for port-forwards, debug shells/containers and selected endpoint checks: destination, local binding, lifecycle, logs, cancellation and cleanup. Compose existing actions and plugins. | M |
| TK19 | Tool and provider discovery | Show installed CLI versions, available APIs, required permissions and scoped actions. Standardize typed inputs, result summaries and context handling; reuse existing plugins and custom jumps. | M |
| TK20 | Runbooks and handoffs | Save repeatable read-only checks, selected scopes, unresolved questions and evidence links. Extend existing bundles with task-oriented review summaries, retained source identity and replay limits. | M |

Maintenance previews cannot prove future schedulability from aggregate free CPU/memory. Storage usage needs a real source and observation time. A configuration path or NetworkPolicy candidate does not prove successful traffic or authorization. Preserve these distinctions in concise badges and inspectable detail.

Fold the deferred [Hubble backlog](../BACKLOG.md) into these workflows: TK17 covers drop grouping, endpoint/conversation pinning, Service entry points, reported DNS queries/answers/latency and policy-attribution links; TK18 owns optional Relay setup and port-forward lifecycle; TK20 composes frozen log/flow captures with resource evidence. Keep collector loss, local eviction and observation windows distinct. Reviewed policy proposals belong after network evidence and TK23 policy review; draft/export for review before considering execution. Do not enable L7 visibility automatically or treat a captured traffic window as exhaustive policy evidence.

### Horizon 4 Add providers when demand is demonstrated

| ID | Workflow | Next useful delivery | Scope |
| --- | --- | --- | --- |
| TK21 | Fleet workspace | Explicitly selected bounded read-only contexts; compare app versions, workload faults, certificates and configured drift sources. Retain independent RBAC/errors and strong cluster identity. | L |
| TK22 | Upgrade readiness | Cluster/node/controller versions, deprecated APIs and CRD compatibility. Integrate kubent/pluto where useful; distinguish discovered resources from observed API usage and inaccessible kinds. | M |
| TK23 | Security and policy review | Workload privilege, capabilities, host mounts, service-account use, image provenance and available admission-policy results. Optional Trivy/Kyverno/OPA integrations with evidence, exceptions and policy version. | M–L |
| TK24 | Backup and restore review | Velero/CSI/operator adapters: schedules, failures, covered resources, exclusions and restore-test evidence. A successful backup does not establish recoverability. | M |
| TK25 | Cost and waste review | Optional OpenCost/Kubecost model with billing window, source and allocation assumptions. Join actionable requests/idle-resource reviews; current CPU usage alone is not cost. | M |
| TK26 | Historical observability | Named Prometheus/log/trace providers with selected time windows and coverage. Link historical observations to app/release evidence; avoid building a competing observability backend. | L |
| TK27 | Operator workflow adapters | Extend existing configurable jumps with semantic status/relationship adapters, typed outputs and modern identity/lifecycle guards. Use generic CRD fallback when semantics are unknown. | M |
| TK28 | Application dependency review | Explicit grouping plus observed traffic/configuration links and ownership. Cross-cluster links require a configured identity source; label incomplete observation windows. | L |

These are options, not a requirement to install every integration. Prefer a small provider interface and workspaces that remain useful with ordinary Kubernetes RBAC and metrics-server absent.

## Implementation and validation

Keep small task domains for workspace, investigation, rollout, review and maintenance. Use `internal/inspect` for observations, typed findings, provenance and evidence formats; the TUI renders these structures rather than parsing existing text. Reuse `action_catalog.go` for discovery and labels, `inspection_connection.go` for captured clients, and `operation_runner.go` for destination-bound operations. Preserve Flux, Hubble and logstream ownership boundaries. Consult [maintenance guidance](maintenance.md) before broad changes to inherited code.

A provider should return evidence, observed time, resource identity, coverage/error state, capabilities and suggested available actions. It should not own the app's global context or automatically start another tool. Renderers need bounded retained data and request budgets. Ordinary browsing must not perform cluster-wide provider discovery, security scans or historical telemetry queries.

Use native read paths for universal Kubernetes workflows; native read-first adapters for Flux, Argo and cert-manager; explicit Helm/Kustomize rendered output for review; optional providers for history, cost, security and backup. Preserve Secret exclusions and recorded redaction limits when opening or exporting detail.

Each increment needs a concrete engineer task and meaningful validation:

| Work | Acceptance evidence |
| --- | --- |
| Overview | At 80×24, identity, active fault/container and missing coverage are visible without scrolling. Current/previous/event facts remain distinguishable. Back/refresh/resize retain identity and selection. |
| Workspace/queue | Several explicit namespaces work without an all-namespace fetch/filter shortcut. Denied namespace discovery still permits a known namespace. No missed source is presented as an empty healthy view. |
| Change/rollout review | Sources/revisions and target destination are explicit; local/server preview fidelity differs visibly; accepted, progressing, complete, failed and unknown outcomes remain distinct. |
| Operations | Slow APIs leave filtering/resize/quit responsive. Context changes cannot redirect captured targets. Grace/PDB/identity and partial outcomes are preserved; abandoned callbacks cannot replace another screen. |
| Providers/fleet | Missing APIs, stale samples and RBAC gaps remain scoped. Retention/list/request bounds and cancellation are tested. No Secret fixture values enter default views or exports. |
| Product usability | Run the operator study and report measured baseline/new-view results. Automated screenshots and fixtures establish behavior, not learnability or production diagnosis time. |

The existing 100 ms performance goal remains a target until a controlled measurement meets it. New workspace watches and background tasks need performance budgets and measured bounds before they expand scope.

## What to build next

Deliver TK01 first, followed by a minimal TK02/TK03 slice. Then TK07 with read-only TK08 ownership/progress; introduce TK06 once source fidelity and review models are defined. Extend operation lifecycles before larger change sets or maintenance execution. Storage, access and capacity are the strongest next routine-toolkit additions. Choose Horizon 4 integrations from actual user demand.

Avoid an autonomous remediation engine, another GitOps controller, a cloud billing backend, an always-on fleet scanner or a replacement terminal/IDE. The toolkit's value is scoped visibility, understandable review, predictable actions and useful retained evidence.

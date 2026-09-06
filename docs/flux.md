<!-- Modified for k9+; see NOTICE. -->

# Native Flux workflows

This fork adds native Flux resource views using the current k9+ Kubernetes connection. The native features require no Flux CLI or additional Go dependencies.

## Open and navigate

| Command or key | Behavior |
| --- | --- |
| `:flux` | Combined view of discovered, supported Flux resources in the current namespace |
| `:flux flux-system` | Combined view in `flux-system` |
| `:flux all` | Combined view across namespaces, subject to your permissions |
| `Enter` in `:flux` | Open the selected resource in its native kind view |
| `:ocirepositories`, `:kustomizations`, `:helmreleases` | Open native per-kind views directly, using discovered aliases |
| `/` | Use normal k9+ filtering; search status, kind, name or revision |
| `i` in either Flux view | Open the complete status message for the selected row |
| `g` in either Flux view | Choose the source or an explicit `dependsOn` dependency and navigate to it |
| `y`, `d` in a native kind view | Standard k9+ YAML and describe |
| `Shift-R` in either Flux view | Confirm an inline reconciliation request for a supported resource |
| `Shift-T` in either Flux view | Confirm an inline suspend or resume operation for a supported resource |

Normal namespace shortcuts, context switching and custom column settings remain available. Open the concrete resource before editing or deleting it; the combined view does not expose those actions on its synthetic rows.

Compact columns emphasize namespace, name, status, suspension, revision, source and age. KIND stays visible in the combined view and moves to wide mode in per-kind views. MESSAGE is available in wide mode; press `i` for the complete selected status without widening the table. Custom column settings can show either field in compact mode. The combined view defaults to NAME sorting, so status changes leave rows in a stable order. Selection follows the resource identity across refreshes, including when you choose a changing column such as STATUS. STATUS sorting puts failed and restricted resources first, with healthy resources last; reversing the sort reverses that order. Health colors follow the active skin. Source navigation understands direct HelmRelease OCI `chartRef`, legacy chart source references, generated HelmChart references and explicit cross-namespace references. It resolves the resource group and available API version in the current context.

The combined view lists one served version per resource kind and reuses the existing watch cache. It does not launch a CLI per row or fetch each object's full state on each refresh. Initial cache synchronization may briefly produce an empty view. A denied kind appears as a `Restricted` row while accessible kinds remain visible. Missing CRDs are skipped; discovery/RBAC limitations can affect which kinds are visible.

## Health and mutations

Suspended resources are identified before interpreting old Ready conditions. Active reconciliation, terminal failures and pending generations have distinct states. A Ready condition from an older generation does not imply that the current desired state has reconciled. The message column retains the relevant controller message or reason.

Native mutations support Kustomizations, HelmReleases, GitRepositories, OCIRepositories, HTTP/S HelmRepositories, HelmCharts and Buckets. An OCI-type HelmRepository is a static source: it displays Ready with an explanatory message and cannot be reconciled or suspended here. Reconcile its HelmChart, or use the OCIRepository API for watched OCI sources. They are absent in read-only mode and recheck that setting at execution. A confirmation identifies the context, kind and namespace/name. The selected dynamic client is captured, requests have a timeout, Kubernetes authorizes GET/PATCH, and UID/resource-version checks protect against replacing or concurrently changing the selected object. Selection reads only the existing list/watch cache. The GET and PATCH run in the background; confirmation dismisses immediately, so you can filter, navigate, or select another resource even while the API is responding. A successful request means the API accepted the change, not that Flux finished. Resume suspended resources before requesting reconciliation.

An unacknowledged `reconcile.fluxcd.io/requestedAt` token displays **Reconciling**, with a message that the request is waiting for the controller. Once Flux acknowledges it through `status.lastHandledReconcileAt`, controller conditions determine whether it is still reconciling, Ready, Failed or Pending. Acknowledgement alone never forces Ready. These are Flux's [documented request semantics](https://fluxcd.io/flux/components/helm/helmreleases/#triggering-a-reconcile). If a controller is unavailable, the request stays visible as waiting; k9+ does not pretend it completed or keep a CLI process waiting. Requests being submitted or still unacknowledged are deduplicated. Failures submitting the API request appear inside k9+.

The combined dashboard resolves the selected row's concrete API kind, namespace and UID before offering a write. Restricted/unavailable rows and kinds without native action support cannot be mutated. Generic editing and deletion still require opening the concrete view.

Flux image automation and Flux Operator resources are also displayed. Operator suspension is read from its reconciliation annotation. Their specialized writes remain in the optional CLI plugin. Native reconcile does not force a Helm upgrade, reset failures or reconcile the source first; those are separate CLI options.

## Inline demo and large-list performance

[Watch the actual TUI handling 10,000 HelmReleases](../assets/flux-inline/inline-reconcile.mp4). The [reproduction script and checks](../assets/flux-inline/README.md) use a disposable localhost API, an intentionally held PATCH response, and explicitly signaled controller updates. The demo verifies native shortcut precedence even with a legacy overriding plugin installed, cancellation without writes, duplicate suppression, filtering and row movement while waiting, and both Ready and Failed completion. No real Flux controller or live cluster is involved.

Status extraction reads individual annotation fields instead of copying the complete annotation map for every release. The [five-round microbenchmark](flux-status-benchmark-2026-09-06.txt) compares the previous fork commit with the final implementation using an identical fixture. With 10,000 releases and 64 annotations per release, the median status scan fell from 110.452 ms to 10.007 ms; allocations fell from 51,440,000 bytes and 80,000 allocations to zero. Four-annotation scans fell from 18.689 ms to 8.101 ms. These timings vary on the shared host and measure only status extraction, not a complete table refresh or controller latency.

## Optional community plugins

Copy only the plugin files you want into the `plugins` directory reported by `k9plus info`. Install their external tools separately. This repository does not install them automatically.

The updated `plugins/flux.yaml` offers Flux CLI reconcile flags, suspend/resume, ownership trace, a Kustomization tree (`Shift-Y`), resource controller logs (`Shift-L`) and suspended-resource lists (`Shift-S`). It passes the selected context and kubeconfig as data, propagates pipeline failures and marks mutations dangerous so k9+ disables them in read-only mode. For core Flux resources, **`Shift-R` and `Shift-T` stay native**, including when an older plugin file sets `override: true`. Other plugin keys and other resource views retain normal override behavior. Replace an existing copy of `flux.yaml` to move optional CLI reconciles and their force/reset/source inputs to `Shift-Z`, and HelmRelease/Kustomization CLI suspend toggles to `Shift-U`. These optional actions are explicitly labeled CLI and may leave the TUI; use the native keys for the inline workflow.

The upstream [community plugin catalog](https://github.com/derailed/k9s/tree/master/plugins) also includes:

| File | Useful workflow | External tool |
| --- | --- | --- |
| `log-stern.yaml` | Tail logs across related pods | stern |
| `helm-diff.yaml` | Inspect Helm changes | Helm diff |
| `cloudnative-pg.yaml` | PostgreSQL operator diagnostics and maintenance | kubectl-cnpg |
| `cert-manager.yaml` | Certificate troubleshooting | cmctl |
| `external-secrets.yaml` | External Secret operations | kubectl |
| `dive.yaml` | Inspect image layers | dive |

These are relevant existing community examples, not a claimed popularity ranking. Review each example's commands and dependencies before enabling it. Native image scanning already exists upstream, so this change does not introduce a second image scanner.

## Scope

The [flux9s project](https://github.com/dgunzy/flux9s) inspired the combined view and relationship workflows. This is an extension of k9s, not a complete flux9s port: there is no native graphical dependency tree, persisted favorites or unified historical reconciliation database. The existing YAML/describe views expose controller-recorded history; the optional Flux tree action handles Kustomization inventory. Runtime behavior against a real cluster still needs an operator smoke test in an appropriate test environment.

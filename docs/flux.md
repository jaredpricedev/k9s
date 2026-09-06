# Native Flux workflows

This fork adds native Flux resource views using the current k9s Kubernetes connection. The native features require no Flux CLI or additional Go dependencies.

## Open and navigate

| Command or key | Behavior |
| --- | --- |
| `:flux` | Combined view of discovered, supported Flux resources in the current namespace |
| `:flux flux-system` | Combined view in `flux-system` |
| `:flux all` | Combined view across namespaces, subject to your permissions |
| `Enter` in `:flux` | Open the selected resource in its native kind view |
| `:ocirepositories`, `:kustomizations`, `:helmreleases` | Open native per-kind views directly, using discovered aliases |
| `/` | Use normal k9s filtering; search status, kind, name or revision |
| `g` in a native kind view | Choose the source or an explicit `dependsOn` dependency and navigate to it |
| `y`, `d` in a native kind view | Standard k9s YAML and describe |
| `Shift-R` in a supported native kind view | Confirm a reconciliation request |
| `Shift-T` in a supported native kind view | Confirm an explicit suspend or resume operation |

Normal namespace shortcuts, context switching and custom column settings remain available. Open the concrete resource before editing or deleting it; the combined view does not expose those actions on its synthetic rows.

Columns include namespace, name, kind, status, suspension, revision, source, controller message and age. Source navigation understands direct HelmRelease OCI `chartRef`, legacy chart source references, generated HelmChart references and explicit cross-namespace references. It resolves the resource group and available API version in the current context.

The combined view lists one served version per resource kind and reuses the existing watch cache. It does not launch a CLI per row or fetch each object's full state on each refresh. Initial cache synchronization may briefly produce an empty view. A denied kind appears as a `Restricted` row while accessible kinds remain visible. Missing CRDs are skipped; discovery/RBAC limitations can affect which kinds are visible.

## Health and mutations

Suspended resources are identified before interpreting old Ready conditions. Active reconciliation, terminal failures and pending generations have distinct states. A Ready condition from an older generation does not imply that the current desired state has reconciled. The message column retains the relevant controller message or reason.

Native mutations support Kustomizations, HelmReleases, GitRepositories, OCIRepositories, HelmRepositories, HelmCharts and Buckets. They are absent in read-only mode and recheck that setting at execution. A confirmation identifies the context, kind and namespace/name. The selected dynamic client is captured, requests have a timeout, Kubernetes authorizes GET/PATCH, and UID/resource-version checks protect against replacing or concurrently changing the selected object. A successful request means the API accepted the change; watch STATUS for controller completion. Resume suspended resources before requesting reconciliation.

Flux image automation and Flux Operator resources are also displayed. Operator suspension is read from its reconciliation annotation. Their specialized writes remain in the optional CLI plugin. Native reconcile does not force a Helm upgrade, reset failures or reconcile the source first; those are separate CLI options.

## Optional community plugins

Copy only the plugin files you want into the `plugins` directory reported by `k9s info`. Install their external tools separately. This repository does not install them automatically.

The updated `plugins/flux.yaml` offers Flux CLI reconcile flags, suspend/resume, ownership trace, a Kustomization tree (`Shift-Y`), resource controller logs (`Shift-L`) and suspended-resource lists (`Shift-S`). It passes the selected context and kubeconfig as data, propagates pipeline failures and marks mutations dangerous so k9s disables them in read-only mode. Its explicitly configured overrides replace native `Shift-R`/`Shift-T` actions when installed.

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

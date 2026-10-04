<!-- Modified for k9+; see NOTICE. -->

# First investigation

Build from the current default branch using the [README](../README.md), then run
`./execs/k9plus --readonly --context your-context -n your-namespace`. Configuration
lives under the k9plus application directories listed by `k9plus info`; the YAML
root remains `k9s:`. See [migration](migration.md) before copying upstream settings.

The default `master` branch includes every journey below: shared actions and help,
retained troubleshooting and workload relationships, log/Hubble workbenches,
comparison, pressure, diagnostics and portable evidence. Optional integrations
require their corresponding APIs and permissions.

Check the context, namespace and access mode before working. Read-only mode blocks
native mutations; Kubernetes permissions still determine which data is available.
Use `F2` for the complete destination, `Ctrl-O` to discover actions, `?` for help,
`:` for commands, Esc to return and `:quit` to exit.

| Task | Journey | Evidence and limits |
| --- | --- | --- |
| Check the destination | `F2`; read context, namespace, connection, access mode and metrics visibility | The compact strip stays visible during prompts, errors and investigations. |
| Find the next action | `:pods`, select a row, `Ctrl-O`, type a label, Enter | Existing fast keys still work. Unavailable actions explain their prerequisite. |
| Investigate a failing workload | `:deployments`, select a row, `:troubleshoot`; `g` for related references, Esc to return | Read-only snapshots, namespace-scoped selector matches and UID-scoped retained events. Missing evidence does not establish health. |
| Follow workload relationships | From an inspector, `g`, select a related resource, Enter; Esc returns | Links state their reference/selector evidence and known UID. Target checks use the original context; Back retains source query and scroll. See [relationships](workload-relationships.md). |
| Read logs | `:pods`, select a pod, `l`; `?` for query, freeze and source controls | Logs are bounded observations; freeze changes the display, not the collector. Recording and export are explicit. See [log workbench](log-workbench.md). |
| Inspect GitOps | `:flux all`, select a row, `i` for full status, `g` for references | Requires Flux CRDs and read permissions. `Shift-R` reconcile and `Shift-T` suspend/resume require write mode, permission and confirmation. See [Flux](flux.md). |
| Inspect certificate evidence | `:certificates all`, select a row, `:tls`; from a Secret, `v` verifies against a named trust source | Requires cert-manager CRDs for certificate lists; Secret TLS metadata needs Secret read permission. A configured reference does not prove a served certificate. See [investigation](resource-investigation.md). |
| Compare two observations | Select a resource, `:compare`; `r` explicitly captures B; `n` toggles API noise | A remains fixed; sources, times, visibility and recreated UID are explicit. See [comparison](resource-comparison.md). |
| Investigate pressure | Select a Pod or workload, `:pressure` | Requests, limits, optional measured usage, OOM last state and retained events; missing metrics remain N/A. See [pressure](pressure.md). |
| Check an optional capability | `:diagnostics`, choose one source | Only the chosen check runs. Absent, denied, stale and unavailable evidence are distinct. See [capabilities](capabilities.md). |
| Share bounded evidence | Select a resource or retained observation, `:evidence`; review, add a note, then `s` | Export is explicit and private; `:evidence-open /absolute/path.json` reviews offline without cluster writes. See [bundles](evidence-bundles.md). |
| Inspect network flows | `:cilium` checks Relay configuration; `Shift-H` from a pod/workload opens scoped flows | Requires Cilium/Hubble, a reachable Relay and verified TLS configuration. No port-forward starts automatically. See [Hubble](hubble.md). |

An absent API, denied permission or unavailable metrics sample is a visibility limit.
Keep its error with your evidence. The [feature matrix](../ROADMAP.md) is the source
of truth for shipped work, recorded validation limits and remaining proposals.
Endpoint probes run from the machine running k9plus and require an explicit action.

<!-- Modified for k9+; see NOTICE. -->

# Daily Kubernetes workspace

A workspace saves one context, an explicit set of namespaces, optional label and
kind filters, pinned resource identities, saved searches and the selected workspace
tab. It opens a retained inventory and findings queue for everyday review. It
does not change the application's Kubernetes destination when selected.

## Start with commands

Choose the intended context through `:ctx`, then save an application scope:

```text
:workspace save checkout checkout,checkout-jobs --selector=app=checkout
:daily
:inventory kind:Pod status:CrashLoopBackOff
:workspace search save crashing-pods kind:Pod status:CrashLoopBackOff
```

The save command captures the app's current context. Its namespace argument is a
comma-separated list of names; `all`, `*` and empty namespace selections are
rejected. The label selector applies to every selected kind and namespace, so
choose labels that the resources you want to review actually carry. Omit the
selector to include every label within those named namespaces.

Use `:workspace` to reopen the active scope and its saved tab, or to choose/create
a scope when none is active. `:workspace use checkout` opens a named scope. A
scope in another context asks you to switch explicitly with `:ctx`; opening it
never switches context for you. Commands use whitespace-separated arguments;
use the forms below for selectors or workspace names containing spaces.

To choose resource kinds explicitly:

```text
:workspace save checkout checkout,checkout-jobs --selector=app=checkout --kinds=pods,deployments,jobs,resourcequotas
```

Default kinds are Pods, Deployments, StatefulSets, DaemonSets, Jobs, CronJobs and
ResourceQuotas. Optional integrations are selected explicitly, for example
`--kinds=kustomizations,helmreleases,certificates`. Use plural resource names for
known APIs, or a full GVR such as `example.io/v1/widgets` for another namespaced
API. The inventory does not enumerate every discovered API automatically.
Secret and known cluster-scoped resource kinds are excluded.

## Workspace controls

| Control | Action |
| --- | --- |
| `1` | Daily findings queue |
| `2` | Inventory |
| `3` | Coverage for attempted reads |
| `4` | Pins |
| `5` | Saved scopes |
| `Enter` | Investigate the selected resource, or open the selected scope |
| `r` | Obtain a new bounded snapshot |
| `/` | Search the retained rows; Enter accepts, Esc cancels editing |
| `n` / `e` | Create / edit a workspace in a form |
| `p` | Pin or unpin the selected resource |
| `s` / `S` | Save the current search / open a saved search |
| `l` | Open live logs for a selected Pod after verifying its captured identity |
| `d` in Scopes | Confirm removal of local scope metadata |
| `Ctrl+O` | Open searchable actions, including pressure, comparison and evidence |
| `Esc` | Return to the previous view |

The create/edit form accepts a name, context, comma-separated namespaces, optional
label selector and optional comma-separated kinds. Saving preserves pins and
searches when they still satisfy the revised scope; out-of-scope pins require an
explicit correction. Editing scope selection clears its old observation before
reading the revised scope. Switching tabs saves that layout locally.

Search is case-insensitive and literal. Every whitespace-separated token must
match; `kind:`, `ns:`, `name:` and `status:` restrict matching to one field. For
example, `kind:Pod ns:checkout status:crash` filters already retained rows. It
does not issue new API reads or broaden the saved namespace/label scope. Unknown
fields and empty field values are rejected while the prior query remains usable.

Pins retain the context, GVR, namespace, name and observed UID. A replacement with
the same name is labelled as an identity change, and inspection verifies the
captured UID. Remove the old pin and select/pin the current Inventory row to
adopt the replacement explicitly. An absent pin remains visible as not observed;
it is not silently redirected to a different object.

## Read the queue with Coverage

The queue ranks current API findings as critical, warning or informational.
Supported observations include container waiting/termination faults, unready or
unscheduled Pods, workload replica shortages and rollout conditions, controller
generations awaiting observation, failed Jobs, suspended Jobs/CronJobs, and quota
usage at or above 90% of its reported hard limit. Previous `OOMKilled` termination
is labelled as historical evidence, separately from a current container fault.

Explicit Flux kinds add suspended, stalled and unready reconciliation observations.
Explicit Certificate kinds add unready certificates and expiry within fourteen
days, based on reported certificate status. Explicit PVC inventory can report
Pending or Lost claims. These readers do not fetch TLS Secret contents, inspect
every controller integration, infer incident causes or reconstruct activity that
preceded the observation. Condition meanings are interpreted for supported kinds;
an arbitrary CRD's `False` condition is not automatically classified as failure.

Coverage records each attempted kind/namespace read as complete, truncated,
denied, API absent, unavailable, canceled or invalid. Known namespace names can
be entered directly without listing every namespace first. A resource-list denial
still remains visible in Coverage. An empty queue with missing coverage does not
establish that the application is healthy.

Opening an active scope reads its initial snapshot; subsequent collection is
explicit with `r`. Reads use the captured client handle and selected namespaces
and selector. The workspace UI imposes a ten-second deadline. Pagination requests
at most 200 objects per page and four pages per kind/namespace pair, with at most
2,000 retained resources and 192 kind/namespace queries overall. Narrow a truncated
scope before treating its inventory as complete. Collection across different API
reads is not an atomic snapshot.

Leaving or covering the workspace cancels pending collection. Returning preserves
its selected row, search, tab and retained observation. A failed refresh retains
the previous successful observation and its original age; Coverage describes the
latest attempted reads. Partial refreshes can update the readable subset, so
review gaps before interpreting missing resources.

## Investigate without losing the workspace

`Enter` opens the selected identity in an overview with current findings,
container status, visibility gaps and proposed next checks first. The destination,
UID and observation time remain visible. Investigation controls are:

| Control | View |
| --- | --- |
| `1` | Overview |
| `2` | Containers, with current and previous states separated |
| `3` | Retained Events |
| `4` | Resources, including pressure evidence when collected |
| `5` | Full retained Evidence |
| `Tab` / `Shift+Tab` | Next / previous investigation tab |
| `6` | Open resource comparison and explicitly choose baseline A |
| `r` / `g` | Refresh the snapshot / inspect related resource references |

Tabs preserve their searches and scroll positions. `m` toggles compact/full
messages in the retained text view. Full evidence and provenance remain available
when the overview shortens messages. A failed refresh leaves the successful
snapshot and its original capture time in place.

Pressure opens a table of container configuration and available usage; unavailable
metrics remain explicit. Comparison opens with changed fields first: `o` switches
between its overview and all retained evidence, `n` reversibly toggles API noise,
and `r` captures B while keeping A fixed. These presentation controls make no
new observation by themselves. See [pressure](pressure.md),
[comparison](resource-comparison.md) and [evidence export](evidence-bundles.md)
for their individual bounds and protections.

Pod log entry verifies the captured UID before starting the native live workbench,
and verifies it again in its first pod-list observation before opening streams.
Context or navigation changes cancel the pending entry. Returning from inspection,
logs, comparison or evidence leaves the saved workspace available on the view
stack. Evidence capture/export retains its existing explicit preview and save
flow; compact display does not replace the retained evidence source.

## Connection and session checks

Run `:connection` or `:connection-health` from any view, including when ordinary
browsing cannot connect. The result keeps the selected context and namespace and
shows separate categories for rejected/unavailable credentials, denied permission,
TLS verification failure, unreachable destination, unavailable API/discovery,
deadline, cancellation and unknown client setup. Raw credential errors and
kubeconfig values are not displayed.

Only `GET /version` and a pod-list read with `limit=1` in one selected namespace
are issued. An all-namespace selection skips the namespace permission check.
A readable version endpoint can be anonymous, so its success alone does not prove
the configured credentials or workload permissions are valid. No integration
scan, namespace enumeration, Secret read, port-forward or login is initiated.

`r` reloads the configured actor's settings for the same named context and builds
fresh, private diagnostic clients. This can observe renewed credentials or fixed
TLS settings without changing global kubeconfig's current-context or discarding
the workspace. It does **not** replace the running browsing clients or reconnect
resource watches. To reconnect ordinary browsing, open `:ctx` and select the
intended context with Enter, then reopen the saved workspace. A failed retry
retains the previous readable observation at its original time and marks it as
retained rather than current.

Checks and their visible result share an eight-second boundary. A configured
credential helper may ignore cancellation; its late result is isolated and
discarded. Diagnostics disables interactive credential prompts and does not run
an additional authentication command. Changing destination, closing the view or
starting another retry prevents the older result from updating the current view.

## Local persistence and release limits

`workspaces.yaml` lives in the k9+ application configuration directory. It stores
scope metadata, active scope, layout, pins and saved searches, rather than API
snapshots or copied kubeconfig credentials. Writes validate the whole store and
replace it atomically with mode `0600`; malformed or unsupported data is reported
instead of silently replaced. Symlink store paths are refused. Removing a scope
removes only local metadata.

The store supports at most 64 workspaces, 16 explicit namespaces and twelve kinds
per workspace, 64 pins and 32 saved searches per workspace, and a 1 MiB file.
This release uses one context per scope, retained explicit observations and a
small queue of supported resource findings. Fleet access, historical activity
before collection, arbitrary CRD health inference and automatic remediation need
separate workflows. The proposed operator evaluation for investigation usability
remains pending; the new presentation does not establish the roadmap's human
recognition-time targets.

See [the delivery validation record](daily-workspace-validation-2026-10-04.md)
for reproducible checks and actual terminal captures.
